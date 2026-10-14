import { useState, type ReactNode } from 'react';
import { DataTable, type Column } from '../components/DataTable';
import { CpuChart, MemChart } from '../components/charts';
import { LogViewer } from '../components/LogViewer';
import { Badge, Card, ErrorLine, Kpi, Skeleton } from '../components/ui';
import { attentionReason, containerStatus, fmtBytes, reclaimableImages, sumSize, unusedVolumes } from '../format';
import type { Poll } from '../hooks';
import type { Container, Image, StatsResponse, SystemInfo, Volume } from '../types';
import { ContainerActions } from './Containers';

interface Props {
  system?: SystemInfo;
  containers: Poll<Container[]>;
  images: Poll<Image[]>;
  volumes: Poll<Volume[]>;
  stats: Poll<StatsResponse>;
  go: (tab: string, filter?: string) => void;
}

export function OverviewPage({ system, containers, images, volumes, stats, go }: Props) {
  const [logs, setLogs] = useState<Container | null>(null);
  const cs = containers.data ?? [];
  const running = cs.filter((c) => c.state === 'running').length;
  const stopped = cs.filter((c) => c.state === 'exited' || c.state === 'dead' || c.state === 'created').length;
  const attention = cs.filter((c) => attentionReason(c));
  const imgs = images.data ?? [];
  const vols = volumes.data ?? [];
  const unusedImgs = reclaimableImages(imgs);
  const unusedVols = unusedVolumes(vols);
  const reclaim = sumSize(unusedImgs) + sumSize(unusedVols);
  const v = (ready: unknown, node: ReactNode) => (ready ? node : '—');

  const attentionCols: Column<Container>[] = [
    { key: 'name', label: 'Container', sort: (c) => c.name, render: (c) => <span className="key">{c.name}</span> },
    {
      key: 'status',
      label: 'Status',
      render: (c) => {
        const [tone, text] = containerStatus(c);
        return <Badge tone={tone}>{text}</Badge>;
      },
    },
    { key: 'issue', label: 'Issue', render: (c) => attentionReason(c) },
  ];

  return (
    <>
      <div className="kpis">
        <Kpi
          label="Running containers"
          value={v(containers.data, running)}
          sub={containers.data && `of ${cs.length} · ${stopped} stopped`}
          onClick={() => go('containers', 'running')}
        />
        <Kpi
          label="Need attention"
          value={v(containers.data, attention.length)}
          sub="restarting, unhealthy or crashed"
          alert={attention.length > 0}
          onClick={() => go('containers', 'attention')}
        />
        <Kpi
          label="Images on disk"
          value={v(images.data, fmtBytes(sumSize(imgs)))}
          sub={images.data && `${imgs.length} images · ${unusedImgs.length} unused`}
          onClick={() => go('images')}
        />
        <Kpi
          label="Volumes"
          value={v(volumes.data, fmtBytes(sumSize(vols)))}
          sub={volumes.data && `${vols.length} volumes · ${unusedVols.length} unused`}
          onClick={() => go('volumes')}
        />
        <Kpi label="Reclaimable" value={v(images.data && volumes.data, fmtBytes(reclaim))} sub="unused images + volumes" onClick={() => go('images', 'unused')} />
        <Kpi
          label="Host"
          value={system ? `${system.cpus} CPU` : '—'}
          sub={system && `${fmtBytes(system.mem_total)} RAM · Engine ${system.server_version}`}
        />
      </div>

      <div className="grid">
        <Card id="cpu-card" title="CPU, last hour" sub={`Busiest five containers, % of one core, sampled every ${stats.data?.interval_sec ?? 5} s`}>
          {stats.error && <ErrorLine>Couldn’t load stats: {stats.error}</ErrorLine>}
          {!stats.data ? (
            <Skeleton height={240} />
          ) : stats.data.series.length === 0 ? (
            <p className="muted center chart-empty">No running containers yet. The chart fills in as samples arrive.</p>
          ) : (
            <CpuChart series={stats.data.series} />
          )}
        </Card>
        <Card id="mem-card" title="Memory" sub="Running containers against their limit; red at 85% of the limit or more">
          {!containers.data ? <Skeleton height={240} /> : <MemChart containers={cs} hostMem={system?.mem_total ?? 0} />}
        </Card>
      </div>

      <Card id="attention-card" title="Needs attention" sub="Restarting, unhealthy, crashed, out of memory, or near the memory limit">
        {containers.error && <ErrorLine>Couldn’t load containers: {containers.error}</ErrorLine>}
        {!containers.data ? (
          <Skeleton height={120} />
        ) : (
          <DataTable
            rows={attention}
            rowKey={(c) => c.id}
            columns={attentionCols}
            empty="All containers look healthy."
            actions={(c) => <ContainerActions c={c} refresh={containers.refresh} onLogs={() => setLogs(c)} />}
          />
        )}
      </Card>
      {logs && <LogViewer id={logs.id} name={logs.name} onClose={() => setLogs(null)} />}
    </>
  );
}
