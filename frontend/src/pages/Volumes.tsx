import { api } from '../api';
import { useActions } from '../actions';
import { DataTable, type Column, type Filter } from '../components/DataTable';
import { Bar, Button, Card, ErrorLine, Pill, Skeleton } from '../components/ui';
import { fmtAgo, fmtBytes, sumSize, unusedVolumes } from '../format';
import type { Poll } from '../hooks';
import type { Volume } from '../types';

const filters: Filter<Volume>[] = [
  { id: 'all', label: 'All', test: () => true },
  { id: 'inuse', label: 'In use', test: (v) => v.used_by.length > 0 },
  { id: 'unused', label: 'Unused', test: (v) => v.used_by.length === 0 },
  { id: 'anonymous', label: 'Anonymous', test: (v) => v.anonymous },
];

export function VolumesPage({ poll }: { poll: Poll<Volume[]> }) {
  const { run, prompt, readOnly } = useActions();
  const rows = poll.data ?? [];
  const maxSize = Math.max(1, ...rows.map((v) => v.size));
  const unused = unusedVolumes(rows);

  const columns: Column<Volume>[] = [
    {
      key: 'name',
      label: 'Volume',
      sort: (v) => v.name,
      render: (v) => (
        <div className="name-cell">
          <span className="mono key" title={v.name}>
            {v.anonymous ? `${v.name.slice(0, 12)}…` : v.name}
          </span>
          {v.anonymous && <Pill>anonymous</Pill>}
          {v.project && <span className="muted small">{v.project}</span>}
        </div>
      ),
    },
    { key: 'driver', label: 'Driver', sort: (v) => v.driver, render: (v) => <Pill>{v.driver}</Pill> },
    {
      key: 'size',
      label: 'Size',
      sort: (v) => v.size,
      render: (v) => (v.size >= 0 ? <Bar frac={v.size / maxSize} label={fmtBytes(v.size)} tone={v.used_by.length ? 'accent' : 'muted'} /> : <span className="muted">unknown</span>),
    },
    {
      key: 'used_by',
      label: 'Used by',
      sort: (v) => v.used_by.join(','),
      render: (v) => (v.used_by.length ? <span>{v.used_by.join(', ')}</span> : <Pill tone="warn">unused</Pill>),
    },
    { key: 'mountpoint', label: 'Mountpoint', secondary: true, sort: (v) => v.mountpoint, render: (v) => <span className="mono muted small">{v.mountpoint}</span> },
    { key: 'created', label: 'Created', secondary: true, sort: (v) => v.created, render: (v) => <span className="muted">{fmtAgo(v.created)}</span> },
  ];

  const create = async () => {
    const v = await prompt({
      title: 'Create a volume',
      fields: [
        { name: 'name', label: 'Name', placeholder: 'app-data', required: true },
        { name: 'driver', label: 'Driver', initial: 'local' },
      ],
      submitLabel: 'Create',
    });
    if (v) await run(() => api.createVolume(v.name!.trim(), v.driver?.trim() || 'local'), { success: `Volume ${v.name} created`, after: () => poll.refresh() });
  };

  return (
    <Card
      id="volumes-card"
      title="Volumes"
      sub={poll.data && `${rows.length} volumes using ${fmtBytes(sumSize(rows))} · ${unused.length} not mounted by any container (${fmtBytes(sumSize(unused))})`}
    >
      {poll.error && <ErrorLine>Couldn’t load volumes: {poll.error}</ErrorLine>}
      {poll.loading && !poll.data ? (
        <Skeleton height={320} />
      ) : (
        <DataTable
          rows={rows}
          rowKey={(v) => v.name}
          columns={columns}
          searchText={(v) => [v.name, v.driver, v.used_by.join(' '), v.project, v.mountpoint].join(' ')}
          searchPlaceholder="Search volumes…"
          filters={filters}
          initialSort={{ key: 'size', dir: 'desc' }}
          actions={(v) =>
            !readOnly && v.used_by.length === 0 ? (
              <Button
                danger
                onClick={() =>
                  run(() => api.removeVolume(v.name), {
                    confirm: {
                      title: `Delete volume ${v.name}?`,
                      body: <p>Its data ({v.size >= 0 ? fmtBytes(v.size) : 'unknown size'}) is permanently deleted. Back it up first if you might need it.</p>,
                      confirmLabel: 'Delete',
                      danger: true,
                    },
                    success: `Volume ${v.name} deleted`,
                    after: () => poll.refresh(),
                  })
                }
              >
                Delete
              </Button>
            ) : null
          }
          toolbar={
            !readOnly && (
              <>
                <Button primary onClick={() => void create()}>
                  Create volume
                </Button>
                <Button
                  danger
                  disabled={unused.length === 0}
                  onClick={() =>
                    run(() => api.pruneVolumes(true), {
                      confirm: {
                        title: 'Delete all unused volumes?',
                        body: (
                          <p>
                            {unused.length} volumes ({fmtBytes(sumSize(unused))}) not mounted by any container — named ones included — are permanently
                            deleted.
                          </p>
                        ),
                        confirmLabel: 'Delete all',
                        danger: true,
                      },
                      success: (r) => `Reclaimed ${fmtBytes((r as { SpaceReclaimed: number }).SpaceReclaimed)}`,
                      after: () => poll.refresh(),
                    })
                  }
                >
                  Prune unused
                </Button>
              </>
            )
          }
        />
      )}
    </Card>
  );
}
