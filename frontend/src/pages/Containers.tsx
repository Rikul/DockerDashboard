import { useState } from 'react';
import { api } from '../api';
import { useActions } from '../actions';
import { DataTable, type Column, type Filter } from '../components/DataTable';
import { InspectViewer, LogViewer } from '../components/LogViewer';
import { Badge, Bar, Button, Card, ErrorLine, Skeleton } from '../components/ui';
import { attentionReason, containerStatus, fmtBytes, fmtDuration, fmtPct, fmtPort } from '../format';
import type { Poll } from '../hooks';
import type { Container } from '../types';

const statusRank = (c: Container) => (attentionReason(c) ? 0 : c.state === 'running' ? 1 : c.state === 'paused' ? 2 : 3);

export const containerFilters: Filter<Container>[] = [
  { id: 'all', label: 'All', test: () => true },
  { id: 'running', label: 'Running', test: (c) => c.state === 'running' },
  { id: 'stopped', label: 'Stopped', test: (c) => c.state === 'exited' || c.state === 'created' || c.state === 'dead' },
  { id: 'attention', label: 'Needs attention', test: (c) => attentionReason(c) != null },
];

/** Buttons for one container, by state. Shared with the Overview's attention list. */
export function ContainerActions({ c, refresh, onLogs, onInspect }: { c: Container; refresh: () => Promise<void>; onLogs: () => void; onInspect?: () => void }) {
  const { run, readOnly, isBusy } = useActions();
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
  return (
    <>
      <Button onClick={onLogs}>Logs</Button>
      {onInspect && <Button onClick={onInspect}>Inspect</Button>}
      {!readOnly && (
        <>
          {(c.state === 'exited' || c.state === 'created' || c.state === 'dead') && (
            <Button onClick={() => act('start', 'started')} disabled={busy('start')}>
              {busy('start') ? 'Starting…' : 'Start'}
            </Button>
          )}
          {c.state === 'paused' && <Button onClick={() => act('unpause', 'resumed')}>Unpause</Button>}
          {c.state === 'running' && (
            <>
              <Button onClick={() => act('restart', 'restarted')} disabled={busy('restart')}>
                {busy('restart') ? 'Restarting…' : 'Restart'}
              </Button>
              <Button onClick={() => act('pause', 'paused')}>Pause</Button>
            </>
          )}
          {(c.state === 'running' || c.state === 'restarting' || c.state === 'paused') && (
            <Button onClick={() => act('stop', 'stopped')} disabled={busy('stop')}>
              {busy('stop') ? 'Stopping…' : 'Stop'}
            </Button>
          )}
          <Button danger onClick={remove}>
            Remove
          </Button>
        </>
      )}
    </>
  );
}

export function ContainersPage({ poll, filter, onFilter }: { poll: Poll<Container[]>; filter: string; onFilter: (f: string) => void }) {
  const { run, readOnly } = useActions();
  const [logs, setLogs] = useState<Container | null>(null);
  const [inspect, setInspect] = useState<Container | null>(null);
  const rows = poll.data ?? [];
  const maxCpu = Math.max(100, ...rows.map((c) => c.cpu_pct ?? 0));

  const columns: Column<Container>[] = [
    {
      key: 'name',
      label: 'Name',
      sort: (c) => c.name,
      render: (c) => (
        <div className="name-cell">
          <span className="key">{c.name}</span>
          {c.project && <span className="muted small">{c.project}</span>}
        </div>
      ),
    },
    { key: 'image', label: 'Image', secondary: true, sort: (c) => c.image, render: (c) => <span className="mono muted">{c.image}</span> },
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
          actions={(c) => <ContainerActions c={c} refresh={poll.refresh} onLogs={() => setLogs(c)} onInspect={() => setInspect(c)} />}
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
      {logs && <LogViewer id={logs.id} name={logs.name} onClose={() => setLogs(null)} />}
      {inspect && <InspectViewer title={`Inspect · ${inspect.name}`} load={() => api.inspectContainer(inspect.id)} onClose={() => setInspect(null)} />}
    </Card>
  );
}
