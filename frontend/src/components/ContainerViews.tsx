import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import type { ContainerTop, FsChange, FsChangeKind } from '../types';
import { Modal } from './ui';

/** How often the process list reloads while Auto-refresh is on. */
const TOP_REFRESH_MS = 3000;

/**
 * Runs `load` now and whenever `reload` is called. The latest result stays on
 * screen while a reload is in flight or fails (the error is reported next to it).
 */
function useLoader<T>(load: () => Promise<T>) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const seq = useRef(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  const reload = useCallback(async () => {
    const mine = ++seq.current;
    setLoading(true);
    try {
      const d = await loadRef.current();
      if (mine !== seq.current) return; // a newer request is on its way
      setData(d);
      setError('');
    } catch (e) {
      if (mine !== seq.current) return;
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    void reload();
    return () => {
      seq.current++; // ignore anything still in flight once closed
    };
  }, [reload]);
  return { data, error, loading, reload };
}

/** The processes of a running container (docker top). */
export function TopViewer({ id, name, onClose }: { id: string; name: string; onClose: () => void }) {
  const { data, error, loading, reload } = useLoader<ContainerTop>(() => api.containerTop(id));
  const [auto, setAuto] = useState(true);
  const [filter, setFilter] = useState('');

  useEffect(() => {
    if (!auto) return;
    const t = window.setInterval(() => void reload(), TOP_REFRESH_MS);
    return () => window.clearInterval(t);
  }, [auto, reload]);

  const rows = useMemo(() => {
    const f = filter.trim().toLowerCase();
    const all = data?.processes ?? [];
    return f ? all.filter((r) => r.join(' ').toLowerCase().includes(f)) : all;
  }, [data, filter]);

  return (
    <Modal title={`Processes · ${name}`} onClose={onClose} wide>
      <div className="tools">
        <input className="search" type="search" placeholder="Filter processes…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter processes" />
        <label className="inline">
          <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} /> Auto-refresh
        </label>
        <span className="spacer" />
        <span className={`pill ${error ? 'bad' : 'muted'}`}>{error ? 'failed' : loading && !data ? 'loading' : auto ? 'live' : 'loaded'}</span>
        <button type="button" className="btn" onClick={() => void reload()} disabled={loading}>
          Refresh
        </button>
      </div>
      <div className="logs">
        {error && <div className="stderr">{data ? `Couldn’t refresh: ${error}` : `Couldn’t list processes: ${error}`}</div>}
        {!data ? (
          !error && <div className="muted">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="muted">{filter ? 'No matching processes' : 'No processes'}</div>
        ) : (
          <table className="ps">
            <thead>
              <tr>
                {data.titles.map((t) => (
                  <th key={t}>{t}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={`${r[1] ?? ''}:${i}`}>
                  {r.map((cell, j) => (
                    <td key={j}>{cell}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      <p className="sub">
        {rows.length.toLocaleString()} processes{filter && data ? ` matching (of ${data.processes.length.toLocaleString()})` : ''} · as listed by the host’s ps
      </p>
    </Modal>
  );
}

const KIND_LABEL: Record<FsChangeKind, string> = { added: 'Added', modified: 'Changed', deleted: 'Deleted' };
const KIND_LETTER: Record<FsChangeKind, string> = { added: 'A', modified: 'C', deleted: 'D' };
const KINDS: FsChangeKind[] = ['added', 'modified', 'deleted'];

/** Files added, changed or deleted since the container was created (docker diff). */
export function DiffViewer({ id, name, onClose }: { id: string; name: string; onClose: () => void }) {
  const { data, error, loading, reload } = useLoader<FsChange[]>(() => api.containerDiff(id));
  const [filter, setFilter] = useState('');
  const [kind, setKind] = useState<FsChangeKind | 'all'>('all');

  const counts = useMemo(() => {
    const c: Record<FsChangeKind, number> = { added: 0, modified: 0, deleted: 0 };
    for (const ch of data ?? []) c[ch.kind]++;
    return c;
  }, [data]);

  const shown = useMemo(() => {
    const f = filter.trim().toLowerCase();
    return (data ?? []).filter((c) => (kind === 'all' || c.kind === kind) && (!f || c.path.toLowerCase().includes(f)));
  }, [data, filter, kind]);

  return (
    <Modal title={`File changes · ${name}`} onClose={onClose} wide>
      <div className="tools">
        <input className="search" type="search" placeholder="Filter paths…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter paths" />
        <button type="button" className="chip" aria-pressed={kind === 'all'} onClick={() => setKind('all')}>
          All<span className="chip-count">{data?.length ?? 0}</span>
        </button>
        {KINDS.map((k) => (
          <button key={k} type="button" className="chip" aria-pressed={kind === k} onClick={() => setKind(k)}>
            {KIND_LABEL[k]}
            <span className="chip-count">{counts[k]}</span>
          </button>
        ))}
        <span className="spacer" />
        <button type="button" className="btn" onClick={() => void reload()} disabled={loading}>
          Refresh
        </button>
      </div>
      <div className="logs">
        {error && <div className="stderr">{`Couldn’t read file changes: ${error}`}</div>}
        {!data ? (
          !error && <div className="muted">Loading…</div>
        ) : shown.length === 0 ? (
          <div className="muted">{data.length === 0 ? 'No changes: the filesystem matches the image.' : 'No matching paths'}</div>
        ) : (
          shown.map((c) => (
            <div key={c.path} className={`diff-${c.kind}`}>
              <span className="diff-kind" title={KIND_LABEL[c.kind]}>
                {KIND_LETTER[c.kind]}
              </span>{' '}
              {c.path}
            </div>
          ))
        )}
      </div>
      <p className="sub">
        {shown.length.toLocaleString()} paths{shown.length !== (data?.length ?? 0) ? ` shown (of ${(data?.length ?? 0).toLocaleString()})` : ''} · A added, C
        changed, D deleted, against the image; mounted volumes aren’t included
      </p>
    </Modal>
  );
}
