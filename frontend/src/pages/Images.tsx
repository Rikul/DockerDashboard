import { useState } from 'react';
import { api, ApiError } from '../api';
import { useActions } from '../actions';
import { DataTable, type Column, type Filter } from '../components/DataTable';
import { InspectViewer } from '../components/LogViewer';
import { Bar, Button, Card, ErrorLine, IconButton, Pill, Skeleton } from '../components/ui';
import { fmtAgo, fmtBytes, reclaimableImages, shortHashes, sumSize } from '../format';
import type { Poll } from '../hooks';
import type { Image } from '../types';

const filters: Filter<Image>[] = [
  { id: 'all', label: 'All', test: () => true },
  { id: 'inuse', label: 'In use', test: (i) => i.containers > 0 },
  { id: 'unused', label: 'Unused', test: (i) => i.containers === 0 },
  { id: 'dangling', label: 'Dangling', test: (i) => i.dangling },
];

export function ImagesPage({ poll, onChanged, filter, onFilter }: { poll: Poll<Image[]>; onChanged: () => void; filter: string; onFilter: (f: string) => void }) {
  const { run, prompt, readOnly, isBusy } = useActions();
  const [inspect, setInspect] = useState<Image | null>(null);
  const rows = poll.data ?? [];
  const maxSize = Math.max(1, ...rows.map((i) => i.size));
  const unused = reclaimableImages(rows);
  const refresh = async () => {
    await poll.refresh();
    onChanged();
  };

  const remove = (i: Image) =>
    run(
      async () => {
        try {
          return await api.removeImage(i.id);
        } catch (e) {
          // Several tags on one image: removing by ID needs force. Only that
          // conflict is retried; "used by stopped container" is surfaced.
          if (e instanceof ApiError && e.status === 409 && /referenced in multiple repositories/i.test(e.message)) {
            return api.removeImage(i.id, true);
          }
          throw e;
        }
      },
      {
        confirm: {
          title: `Remove ${i.ref}?`,
          body: (
            <p>
              Deletes the image{i.repo_tags.length > 1 ? ` and its ${i.repo_tags.length} tags (${i.repo_tags.join(', ')})` : ''}, freeing up to{' '}
              {fmtBytes(i.size)}. It can be pulled again later if it came from a registry.
            </p>
          ),
          confirmLabel: 'Remove',
          danger: true,
        },
        success: `${i.ref} removed`,
        after: refresh,
      },
    );

  const pull = async (initial = '') => {
    const v = await prompt({
      title: 'Pull an image',
      body: <p className="sub">Docker Hub names like nginx:1.27 work, as do full references like ghcr.io/org/app:tag.</p>,
      fields: [{ name: 'ref', label: 'Image reference', placeholder: 'nginx:1.27-alpine', initial, required: true }],
      submitLabel: 'Pull',
    });
    if (v?.ref) await run(() => api.pullImage(v.ref!.trim()), { success: `Pulled ${v.ref}`, after: refresh, key: 'pull' });
  };

  const prune = (all: boolean) => {
    const target = all ? unused : rows.filter((i) => i.dangling);
    return run(() => api.pruneImages(all), {
      confirm: {
        title: all ? 'Remove all unused images?' : 'Remove dangling images?',
        body: (
          <p>
            {target.length} images ({fmtBytes(sumSize(target))}) not used by any container will be deleted.
            {all && ' Images for stopped containers are kept; tagged images you may still want are removed.'}
          </p>
        ),
        confirmLabel: 'Prune',
        danger: true,
      },
      success: (r) => `Reclaimed ${fmtBytes((r as { SpaceReclaimed: number }).SpaceReclaimed)}`,
      after: refresh,
    });
  };

  const columns: Column<Image>[] = [
    {
      key: 'ref',
      label: 'Image',
      sort: (i) => i.ref,
      render: (i) => (
        <div className="name-cell">
          <span className={`mono clip${i.dangling ? ' muted' : ''}`} title={i.ref}>
            {shortHashes(i.ref)}
          </span>
          {i.repo_tags.length > 1 && <span className="muted small">+{i.repo_tags.length - 1} more tags</span>}
          {i.dangling && <Pill tone="warn">dangling</Pill>}
        </div>
      ),
    },
    { key: 'id', label: 'ID', secondary: true, sort: (i) => i.short_id, render: (i) => <span className="mono muted">{i.short_id}</span> },
    { key: 'size', label: 'Size', sort: (i) => i.size, render: (i) => <Bar frac={i.size / maxSize} label={fmtBytes(i.size)} tone={i.containers ? 'accent' : 'muted'} /> },
    { key: 'created', label: 'Created', secondary: true, sort: (i) => i.created, render: (i) => <span className="muted">{fmtAgo(i.created)}</span> },
    {
      key: 'containers',
      label: 'Containers',
      align: 'right',
      sort: (i) => i.containers,
      render: (i) => (i.containers ? i.containers : <span className="muted">unused</span>),
    },
  ];

  return (
    <Card
      id="images-card"
      title="Local images"
      sub={
        poll.data &&
        `${rows.length} images using ${fmtBytes(sumSize(rows))} · ${unused.length} not used by any container (${fmtBytes(sumSize(unused))})`
      }
    >
      {poll.error && <ErrorLine>Couldn’t load images: {poll.error}</ErrorLine>}
      {poll.loading && !poll.data ? (
        <Skeleton height={320} />
      ) : (
        <DataTable
          rows={rows}
          rowKey={(i) => i.id}
          columns={columns}
          searchText={(i) => [i.ref, i.repo_tags.join(' '), i.short_id].join(' ')}
          searchPlaceholder="Search repository, tag or ID…"
          filters={filters}
          filter={filter}
          onFilter={onFilter}
          initialSort={{ key: 'size', dir: 'desc' }}
          actions={(i) => (
            <>
              <IconButton icon="inspect" label="Inspect" onClick={() => setInspect(i)} />
              {!readOnly && !i.dangling && (
                <IconButton icon="download" label="Update (pull the newest version of this tag)" onClick={() => void pull(i.ref)} />
              )}
              {!readOnly && i.containers === 0 && (
                <IconButton icon="trash" label="Remove" danger onClick={() => remove(i)} />
              )}
            </>
          )}
          toolbar={
            !readOnly && (
              <>
                <Button primary onClick={() => void pull()} disabled={isBusy('pull')}>
                  {isBusy('pull') ? 'Pulling…' : 'Pull image'}
                </Button>
                <Button onClick={() => prune(false)} disabled={!rows.some((i) => i.dangling)}>
                  Prune dangling
                </Button>
                <Button danger onClick={() => prune(true)} disabled={unused.length === 0}>
                  Prune unused
                </Button>
              </>
            )
          }
        />
      )}
      {inspect && <InspectViewer title={`Inspect · ${inspect.ref}`} load={() => api.inspectImage(inspect.id)} onClose={() => setInspect(null)} />}
    </Card>
  );
}
