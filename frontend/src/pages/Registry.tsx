import { api } from '../api';
import { useActions } from '../actions';
import { DataTable, type Column } from '../components/DataTable';
import { Button, Card, ErrorLine, Pill, Skeleton } from '../components/ui';
import { fmtAgo, fmtBytes } from '../format';
import { usePoll } from '../hooks';
import type { RegistryTag } from '../types';

export function RegistryPage({ onPulled }: { onPulled: () => void }) {
  const { run, toast, readOnly, isBusy } = useActions();
  const poll = usePoll(() => api.registry(), 60_000);
  const data = poll.data;

  if (data && !data.configured) {
    return (
      <Card id="registry-card" title="Registry">
        <div className="empty-state">
          <p>No registry is connected. Set these environment variables on the dashboard server and restart it:</p>
          <pre className="code">{`REGISTRY_URL=https://registry.example.com
REGISTRY_USERNAME=robot-account     # optional
REGISTRY_PASSWORD=…                 # optional, or a token`}</pre>
          <p className="sub">
            Works with any registry that speaks the Docker Registry HTTP API v2 (distribution, Harbor, GitLab, GHCR, Artifactory…). Deleting tags
            needs deletes enabled on the registry.
          </p>
        </div>
      </Card>
    );
  }

  const tags = data?.tags ?? [];
  const repos = new Set(tags.map((t) => t.repository)).size;

  const columns: Column<RegistryTag>[] = [
    {
      key: 'ref',
      label: 'Image',
      sort: (t) => `${t.repository}:${t.tag}`,
      render: (t) => (
        <div className="name-cell">
          <span className="mono">
            {t.repository}:<b>{t.tag}</b>
          </span>
          {t.error && <Pill tone="bad" title={t.error}>unreadable</Pill>}
        </div>
      ),
    },
    { key: 'created', label: 'Built', sort: (t) => t.created ?? '', render: (t) => <span className="muted">{fmtAgo(t.created)}</span> },
    { key: 'size', label: 'Size', align: 'right', sort: (t) => t.size, render: (t) => (t.size ? fmtBytes(t.size) : '—') },
    {
      key: 'platforms',
      label: 'Platforms',
      secondary: true,
      sort: (t) => t.platforms?.length ?? 1,
      render: (t) => (t.platforms?.length ? <span className="small">{t.platforms.join(', ')}</span> : <span className="muted small">single</span>),
    },
    { key: 'digest', label: 'Digest', secondary: true, sort: (t) => t.digest, render: (t) => <span className="mono muted small">{t.digest.replace('sha256:', '').slice(0, 12)}</span> },
  ];

  return (
    <Card
      id="registry-card"
      title={data?.host ? `Registry · ${data.host}` : 'Registry'}
      sub={data && `${tags.length} tags across ${repos} repositories · newest tags first, up to 25 per repository`}
    >
      {poll.error && <ErrorLine>Couldn’t read the registry: {poll.error}</ErrorLine>}
      {data?.problems?.map((p) => (
        <ErrorLine key={p}>{p}</ErrorLine>
      ))}
      {poll.loading && !data ? (
        <Skeleton height={260} />
      ) : (
        <DataTable
          rows={tags}
          rowKey={(t) => t.ref}
          columns={columns}
          searchText={(t) => `${t.repository}:${t.tag} ${t.digest}`}
          searchPlaceholder="Search repository or tag…"
          initialSort={{ key: 'created', dir: 'desc' }}
          actions={(t) => (
            <>
              <Button
                onClick={() => {
                  void navigator.clipboard?.writeText(t.ref).then(
                    () => toast(`Copied ${t.ref}`),
                    () => toast(t.ref),
                  );
                }}
              >
                Copy
              </Button>
              {!readOnly && (
                <>
                  <Button disabled={isBusy(`pull:${t.ref}`)} onClick={() => run(() => api.pullImage(t.ref), { success: `Pulled ${t.ref}`, after: onPulled, key: `pull:${t.ref}` })}>
                    {isBusy(`pull:${t.ref}`) ? 'Pulling…' : 'Pull to host'}
                  </Button>
                  <Button
                    danger
                    onClick={() =>
                      run(() => api.deleteRegistryTag(t.repository, t.tag), {
                        confirm: {
                          title: `Delete ${t.repository}:${t.tag} from the registry?`,
                          body: (
                            <p>
                              This deletes the manifest {t.digest.slice(0, 19)}…, so <b>every tag pointing at the same digest</b> goes too. Run the
                              registry’s garbage collection afterwards to free the space.
                            </p>
                          ),
                          confirmLabel: 'Delete',
                          danger: true,
                        },
                        success: `Deleted ${t.repository}:${t.tag}`,
                        after: () => api.registry(true).then(() => poll.refresh()),
                      })
                    }
                  >
                    Delete
                  </Button>
                </>
              )}
            </>
          )}
          toolbar={
            <Button onClick={() => run(() => api.registry(true), { after: () => poll.refresh() })} title="Re-read the registry (it is cached for a minute)">
              Refresh
            </Button>
          }
        />
      )}
    </Card>
  );
}
