import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import type { LogLine } from '../types';
import { Modal } from './ui';

const MAX_LINES = 5000;

/** Keeps the last MAX_LINES of a, in place. */
function trimBuffer(a: LogLine[]) {
  if (a.length > MAX_LINES) a.splice(0, a.length - MAX_LINES);
}

/** Live container logs over Server-Sent Events. */
export function LogViewer({ id, name, onClose }: { id: string; name: string; onClose: () => void }) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [tail, setTail] = useState(200);
  const [follow, setFollow] = useState(true);
  const [timestamps, setTimestamps] = useState(false);
  const [filter, setFilter] = useState('');
  const [status, setStatus] = useState<'connecting' | 'live' | 'ended' | 'error'>('connecting');
  const [endError, setEndError] = useState('');
  const [autoScroll, setAutoScroll] = useState(true);
  const boxRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setLines([]);
    setStatus('connecting');
    setEndError('');
    const es = new EventSource(api.logsUrl(id, tail, follow, timestamps));
    let buf: LogLine[] = [];
    // Batch incoming lines into one render per frame.
    let raf = 0;
    const flush = () => {
      raf = 0;
      const add = buf;
      buf = [];
      trimBuffer(add);
      setLines((prev) => {
        const next = prev.concat(add);
        return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
      });
    };
    // After a dropped connection EventSource reconnects by itself, and the
    // server replays the last `tail` lines, so start over instead of
    // appending them a second time.
    let opened = false;
    es.onopen = () => {
      if (opened) {
        buf = [];
        setLines([]);
      }
      opened = true;
      setStatus('live');
    };
    es.onmessage = (e) => {
      try {
        buf.push(JSON.parse(e.data) as LogLine);
        // Animation frames pause in hidden tabs while events keep coming,
        // so bound the pending buffer here too, not only on flush. Trim in
        // chunks to keep the per-line cost constant.
        if (buf.length > MAX_LINES * 2) trimBuffer(buf);
        if (!raf) raf = requestAnimationFrame(flush);
      } catch {
        /* ignore malformed */
      }
    };
    es.addEventListener('end', (e) => {
      // The server sends reason "end" on EOF and the read error otherwise.
      let reason = 'end';
      try {
        reason = (JSON.parse((e as MessageEvent<string>).data) as { reason?: string }).reason ?? 'end';
      } catch {
        /* treat malformed as a normal end */
      }
      if (reason === 'end') {
        setStatus('ended');
      } else {
        setStatus('error');
        setEndError(reason);
      }
      es.close();
    });
    es.onerror = () => {
      // EventSource reconnects by itself; after an 'end' it's closed.
      if (es.readyState === EventSource.CLOSED) setStatus((s) => (s === 'ended' ? s : 'error'));
    };
    return () => {
      es.close();
      cancelAnimationFrame(raf);
    };
  }, [id, tail, follow, timestamps]);

  useEffect(() => {
    const el = boxRef.current;
    if (el && autoScroll) el.scrollTop = el.scrollHeight;
  }, [lines, autoScroll]);

  const shown = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return f ? lines.filter((l) => l.text.toLowerCase().includes(f)) : lines;
  }, [lines, filter]);

  const blobs = useRef(new Set<string>());
  useEffect(() => {
    const pending = blobs.current;
    return () => {
      pending.forEach((u) => URL.revokeObjectURL(u));
      pending.clear();
    };
  }, []);

  const download = () => {
    const blob = new Blob([lines.map((l) => l.text).join('\n')], { type: 'text/plain' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `${name}.log`;
    a.click();
    // Revoking right away can cancel the download in some browsers, so wait
    // a little; anything still pending is revoked when the viewer closes.
    const url = a.href;
    blobs.current.add(url);
    setTimeout(() => {
      if (blobs.current.delete(url)) URL.revokeObjectURL(url);
    }, 10_000);
  };

  return (
    <Modal title={`Logs · ${name}`} onClose={onClose} wide>
      <div className="tools">
        <input className="search" type="search" placeholder="Filter lines…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter log lines" />
        <label className="inline">
          Last
          <select value={tail} onChange={(e) => setTail(Number(e.target.value))}>
            {[100, 200, 500, 1000, 5000].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
          lines
        </label>
        <label className="inline">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Follow
        </label>
        <label className="inline">
          <input type="checkbox" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} /> Timestamps
        </label>
        <span className="spacer" />
        <span className={`pill ${status === 'live' ? 'ok' : status === 'error' ? 'bad' : 'muted'}`} title={endError || undefined}>
          {status === 'live' ? (follow ? 'live' : 'loaded') : endError ? 'stream failed' : status}
        </span>
        <button type="button" className="btn" onClick={download} disabled={!lines.length}>
          Download
        </button>
      </div>
      <div
        className="logs"
        ref={boxRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          setAutoScroll(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
        }}
      >
        {shown.length === 0 ? (
          <div className={endError && !lines.length ? 'bad-text' : 'muted'}>
            {status === 'connecting'
              ? 'Connecting…'
              : endError && !lines.length
                ? `Couldn't read logs: ${endError}`
                : filter
                  ? 'No matching lines'
                  : 'No output'}
          </div>
        ) : (
          shown.map((l, i) => (
            <div key={i} className={l.stream === 'stderr' ? 'stderr' : undefined}>
              {l.text || ' '}
            </div>
          ))
        )}
      </div>
      <p className="sub">
        {shown.length.toLocaleString()} lines{filter ? ` matching (of ${lines.length.toLocaleString()})` : ''} · stderr in red
        {lines.length >= MAX_LINES ? ` · keeping the newest ${MAX_LINES.toLocaleString()}` : ''}
        {endError && lines.length ? ` · the log stream failed, so output may be incomplete: ${endError}` : ''}
      </p>
    </Modal>
  );
}

/** Pretty-printed JSON of an inspect call. */
export function InspectViewer({ title, load, onClose }: { title: string; load: () => Promise<unknown>; onClose: () => void }) {
  const [text, setText] = useState('Loading…');
  useEffect(() => {
    load().then(
      (d) => setText(JSON.stringify(d, null, 2)),
      (e: unknown) => setText(e instanceof Error ? e.message : String(e)),
    );
    // Load once when opened, not on every parent re-render.
  }, []);
  return (
    <Modal title={title} onClose={onClose} wide>
      <pre className="logs json">{text}</pre>
    </Modal>
  );
}
