import { useEffect, useRef, useState, type ReactNode } from 'react';
import { api } from '../api';
import { useActions } from '../actions';
import type { MenuEntry } from '../components/ActionMenu';
import { DataTable, type Column, type Filter } from '../components/DataTable';
import { DiffViewer, TopViewer } from '../components/ContainerViews';
import { InspectViewer, LogViewer } from '../components/LogViewer';
import { Icon } from '../components/icons';
import { Badge, Bar, Button, Card, ErrorLine, Skeleton } from '../components/ui';
import { attentionReason, containerStatus, fmtBytes, fmtDuration, fmtPct, fmtPort, shortHashes } from '../format';
import type { Poll } from '../hooks';
import type { Container } from '../types';

const statusRank = (c: Container) => (attentionReason(c) ? 0 : c.state === 'running' ? 1 : c.state === 'paused' ? 2 : 3);

export const containerFilters: Filter<Container>[] = [
  { id: 'all', label: 'All', test: () => true },
  { id: 'running', label: 'Running', test: (c) => c.state === 'running' },
  { id: 'stopped', label: 'Stopped', test: (c) => c.state === 'exited' || c.state === 'created' || c.state === 'dead' },
  { id: 'attention', label: 'Needs attention', test: (c) => attentionReason(c) != null },
];

type Viewer = { kind: 'logs' | 'inspect' | 'top' | 'diff'; c: Container };

/**
 * The row menu for containers, plus the viewers its items open. Shared with
 * the Overview's attention list: render `viewers` once, and pass `menu` to a
 * DataTable's `actions`.
 */
export function useContainerMenu(refresh: () => Promise<void>): { menu: (c: Container) => MenuEntry[]; viewers: ReactNode } {
  const { run, readOnly, isBusy } = useActions();
  const [viewer, setViewer] = useState<Viewer | null>(null);
  const close = () => setViewer(null);
  const viewers = viewer && (
    <>
      {viewer.kind === 'logs' && <LogViewer id={viewer.c.id} name={viewer.c.name} onClose={close} />}
      {viewer.kind === 'inspect' && <InspectViewer title={`Inspect · ${viewer.c.name}`} load={() => api.inspectContainer(viewer.c.id)} onClose={close} />}
      {viewer.kind === 'top' && <TopViewer id={viewer.c.id} name={viewer.c.name} onClose={close} />}
      {viewer.kind === 'diff' && <DiffViewer id={viewer.c.id} name={viewer.c.name} onClose={close} />}
    </>
  );
  const menu = (c: Container): MenuEntry[] => {
    const act = (action: 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'kill', past: string) =>
      run(() => api.containerAction(c.id, action), { success: `${c.name} ${past}`, after: refresh, key: `${c.id}:${action}` });
    const busy = (a: string) => isBusy(`${c.id}:${a}`);
    const live = c.state === 'running' || c.state === 'restarting' || c.state === 'paused';
    const remove = () =>
      run(
        async () => {
          // Stop gracefully (SIGTERM, then the stop timeout) before removing,
          // rather than letting a forced remove SIGKILL the process.
          if (live) await api.containerAction(c.id, 'stop');
          return api.removeContainer(c.id, c.state === 'dead');
        },
        {
          confirm: {
            title: `Remove ${c.name}?`,
            body: (
              <p>
                The container and its writable layer are deleted. Named volumes are kept.
                {live && ' It is running, so it is stopped gracefully first; removal waits for it to exit.'}
              </p>
            ),
            confirmLabel: 'Remove',
            danger: true,
          },
          success: `${c.name} removed`,
          after: refresh,
        },
      );
    return [
      { label: 'Logs', icon: 'logs', onClick: () => setViewer({ kind: 'logs', c }) },
      { label: 'Inspect', icon: 'inspect', onClick: () => setViewer({ kind: 'inspect', c }) },
      c.state === 'running' && { label: 'Processes (top)', icon: 'processes', onClick: () => setViewer({ kind: 'top', c }) },
      { label: 'File changes (diff)', icon: 'diff', onClick: () => setViewer({ kind: 'diff', c }) },
      !readOnly && (c.state === 'exited' || c.state === 'created' || c.state === 'dead') && {
        label: busy('start') ? 'Starting…' : 'Start',
        icon: 'play',
        busy: busy('start'),
        onClick: () => act('start', 'started'),
      },
      !readOnly && c.state === 'paused' && { label: 'Unpause', icon: 'play', busy: busy('unpause'), onClick: () => act('unpause', 'resumed') },
      !readOnly && c.state === 'running' && {
        label: busy('restart') ? 'Restarting…' : 'Restart',
        icon: 'restart',
        busy: busy('restart'),
        onClick: () => act('restart', 'restarted'),
      },
      !readOnly && c.state === 'running' && { label: 'Pause', icon: 'pause', busy: busy('pause'), onClick: () => act('pause', 'paused') },
      !readOnly && live && {
        label: busy('stop') ? 'Stopping…' : 'Stop',
        icon: 'stop',
        busy: busy('stop'),
        onClick: () => act('stop', 'stopped'),
      },
      !readOnly && { label: 'Remove', icon: 'trash', danger: true, onClick: remove },
    ];
  };
  return { menu, viewers };
}

/** The daemon's rule for container names (at least two characters). */
const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]+$/;

/**
 * A container's name. Unless the dashboard is read-only, clicking it turns it
 * into an input; Enter or the check mark asks for confirmation, then renames.
 */
function RenameableName({ c, refresh }: { c: Container; refresh: () => Promise<void> }) {
  const { run, readOnly, isBusy } = useActions();
  const [draft, setDraft] = useState<string | null>(null); // null: not editing
  const input = useRef<HTMLInputElement>(null);
  const wrap = useRef<HTMLSpanElement>(null);
  // The confirmation dialog takes focus, which must not count as leaving the field.
  const confirming = useRef(false);
  // After a failed or cancelled rename the field is usable again once the
  // busy state clears (a disabled input can't take focus before that).
  const [retry, setRetry] = useState(0);
  const busy = isBusy(`${c.id}:rename`);
  useEffect(() => {
    if (retry > 0 && !busy) input.current?.focus();
  }, [retry, busy]);

  if (readOnly) return <span className="key">{c.name}</span>;

  if (draft === null) {
    return (
      <button type="button" className="rename-name key" title="Click to rename" onClick={() => setDraft(c.name)}>
        {c.name}
        <Icon name="edit" />
      </button>
    );
  }

  const name = draft.trim();
  const invalid = name !== '' && !NAME_RE.test(name);
  const canSubmit = name !== c.name && NAME_RE.test(name) && !busy;

  const submit = async () => {
    if (!canSubmit || confirming.current) return;
    confirming.current = true;
    const done = await run(() => api.renameContainer(c.id, name), {
      confirm: {
        title: `Rename ${c.name}?`,
        body: (
          <p>
            <b>{c.name}</b> becomes <b>{name}</b>. Anything that reaches it by the old name, such as other containers on a shared network, links or
            scripts, has to use the new one.
            {c.project && ' Compose may give it back its old name the next time the stack is recreated.'}
          </p>
        ),
        confirmLabel: 'Rename',
      },
      success: `${c.name} renamed to ${name}`,
      after: refresh,
      key: `${c.id}:rename`,
    });
    confirming.current = false;
    if (done) setDraft(null);
    else setRetry((n) => n + 1);
  };

  return (
    <span
      ref={wrap}
      className="rename-edit"
      onBlur={(e) => {
        if (!confirming.current && !wrap.current?.contains(e.relatedTarget as Node | null)) setDraft(null);
      }}
    >
      <input
        ref={input}
        className={`rename-input${invalid ? ' invalid' : ''}`}
        value={draft}
        autoFocus
        spellCheck={false}
        disabled={busy}
        aria-label={`New name for ${c.name}`}
        aria-invalid={invalid || undefined}
        style={{ width: `${Math.max(12, draft.length + 2)}ch` }}
        onFocus={(e) => e.currentTarget.select()}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            void submit();
          } else if (e.key === 'Escape') {
            e.preventDefault();
            setDraft(null);
          }
        }}
      />
      <button type="button" className="btn icon-only" aria-label="Confirm rename" title="Rename…" disabled={!canSubmit} onClick={() => void submit()}>
        <Icon name={busy ? 'spinner' : 'check'} />
      </button>
      <button type="button" className="btn icon-only" aria-label="Cancel rename" title="Cancel" disabled={busy} onClick={() => setDraft(null)}>
        <Icon name="x" />
      </button>
      {invalid && <span className="small bad-text">Letters, digits, _ . - ; at least 2 characters</span>}
    </span>
  );
}

export function ContainersPage({ poll, filter, onFilter }: { poll: Poll<Container[]>; filter: string; onFilter: (f: string) => void }) {
  const { run, readOnly } = useActions();
  const rows = poll.data ?? [];
  const { menu, viewers } = useContainerMenu(poll.refresh);
  const maxCpu = Math.max(100, ...rows.map((c) => c.cpu_pct ?? 0));

  const columns: Column<Container>[] = [
    {
      key: 'name',
      label: 'Name',
      sort: (c) => c.name,
      render: (c) => (
        <div className="name-cell">
          <RenameableName c={c} refresh={poll.refresh} />
          {c.project && <span className="muted small">{c.project}</span>}
        </div>
      ),
    },
    { key: 'image', label: 'Image', secondary: true, sort: (c) => c.image, render: (c) => (
        <span className="mono muted clip" title={c.image}>
          {shortHashes(c.image)}
        </span>
      ),
    },
    {
      key: 'status',
      label: 'Status',
      sort: statusRank,
      render: (c) => {
        const [tone, text] = containerStatus(c);
        const why = attentionReason(c);
        return (
          <span title={why ?? c.status}>
            <Badge tone={tone}>{text}</Badge>
          </span>
        );
      },
    },
    {
      key: 'cpu',
      label: 'CPU',
      sort: (c) => c.cpu_pct ?? -1,
      render: (c) =>
        c.cpu_pct != null ? <Bar frac={c.cpu_pct / maxCpu} label={fmtPct(c.cpu_pct)} tone={c.cpu_pct >= 80 ? 'bad' : 'accent'} /> : <span className="muted">—</span>,
    },
    {
      key: 'mem',
      label: 'Memory',
      sort: (c) => c.mem_bytes ?? -1,
      render: (c) => {
        if (c.mem_bytes == null) return <span className="muted">—</span>;
        if (!c.mem_limit_set || !c.mem_limit) return <span className="small">{fmtBytes(c.mem_bytes)}</span>;
        const frac = c.mem_bytes / c.mem_limit;
        return <Bar frac={frac} label={`${fmtBytes(c.mem_bytes)} / ${fmtBytes(c.mem_limit)}`} tone={frac >= 0.85 ? 'bad' : 'accent2'} />;
      },
    },
    {
      key: 'ports',
      label: 'Ports',
      secondary: true,
      sort: (c) => c.ports.map(fmtPort).join(' '),
      render: (c) => (c.ports.length ? <span className="mono small">{c.ports.map(fmtPort).join(', ')}</span> : <span className="muted">—</span>),
    },
    {
      key: 'uptime',
      label: 'Uptime',
      align: 'right',
      secondary: true,
      sort: (c) => (c.state === 'running' && c.started_at ? -Date.parse(c.started_at) : null),
      render: (c) => (c.state === 'running' ? fmtDuration(c.started_at) : <span className="muted">—</span>),
    },
    {
      key: 'restarts',
      label: 'Restarts',
      align: 'right',
      sort: (c) => c.restart_count,
      render: (c) => <span className={c.restart_count > 0 ? 'bad-text' : undefined}>{c.restart_count}</span>,
    },
  ];

  const stopped = rows.filter((c) => c.state === 'exited' || c.state === 'created' || c.state === 'dead').length;

  return (
    <Card id="containers-card">
      {poll.error && <ErrorLine>Couldn’t load containers: {poll.error}</ErrorLine>}
      {poll.loading && !poll.data ? (
        <Skeleton height={320} />
      ) : (
        <DataTable
          rows={rows}
          rowKey={(c) => c.id}
          columns={columns}
          searchText={(c) => [c.name, c.image, c.project, c.status, c.networks.join(' '), c.ports.map(fmtPort).join(' ')].join(' ')}
          searchPlaceholder="Search name, image, port, network…"
          filters={containerFilters}
          filter={filter}
          onFilter={onFilter}
          initialSort={{ key: 'status', dir: 'asc' }}
          rowClass={(c) => (attentionReason(c) ? 'attention' : undefined)}
          actions={menu}
          actionsLabel={(c) => `Actions for ${c.name}`}
          toolbar={
            !readOnly && (
              <Button
                disabled={stopped === 0}
                onClick={() =>
                  run(api.pruneContainers, {
                    confirm: {
                      title: 'Remove all stopped containers?',
                      body: <p>{stopped} stopped containers will be deleted. This can’t be undone.</p>,
                      confirmLabel: 'Prune',
                      danger: true,
                    },
                    success: (r) => `Removed ${(r as { ContainersDeleted?: string[] }).ContainersDeleted?.length ?? 0} containers`,
                    after: poll.refresh,
                  })
                }
              >
                Prune stopped
              </Button>
            )
          }
        />
      )}
      {viewers}
    </Card>
  );
}
