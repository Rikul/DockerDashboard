import { useState } from 'react';
import { api } from '../api';
import { useActions } from '../actions';
import { DataTable, type Column } from '../components/DataTable';
import { InspectViewer } from '../components/LogViewer';
import { Button, Card, ErrorLine, IconButton, Pill, Skeleton } from '../components/ui';
import type { Poll } from '../hooks';
import type { Container, Network } from '../types';

export function NetworksPage({ poll, containers, onChanged }: { poll: Poll<Network[]>; containers: Container[]; onChanged: () => void }) {
  const { run, prompt, readOnly } = useActions();
  const [inspect, setInspect] = useState<Network | null>(null);
  const rows = poll.data ?? [];
  const refresh = async () => {
    await poll.refresh();
    onChanged();
  };
  // Containers can't join host/none, and "host"-driver networks are special.
  const joinable = (n: Network) => !readOnly && n.name !== 'host' && n.name !== 'none' && n.driver !== 'host' && n.driver !== 'null';

  const disconnect = (n: Network, name: string) =>
    run(() => api.disconnectNetwork(n.id, name), {
      confirm: {
        title: `Disconnect ${name} from ${n.name}?`,
        body: <p>The container loses its address on this network straight away.</p>,
        confirmLabel: 'Disconnect',
        danger: true,
      },
      success: `${name} disconnected from ${n.name}`,
      after: refresh,
    });

  const connect = async (n: Network) => {
    const options = containers.filter((c) => !n.containers.includes(c.name)).map((c) => c.name);
    if (!options.length) return;
    const v = await prompt({
      title: `Connect a container to ${n.name}`,
      fields: [{ name: 'container', label: 'Container', type: 'select', options }],
      submitLabel: 'Connect',
    });
    if (v?.container) await run(() => api.connectNetwork(n.id, v.container!), { success: `${v.container} connected to ${n.name}`, after: refresh });
  };

  const create = async () => {
    const v = await prompt({
      title: 'Create a network',
      fields: [
        { name: 'name', label: 'Name', placeholder: 'app-net', required: true },
        { name: 'driver', label: 'Driver', type: 'select', options: ['bridge', 'overlay', 'macvlan', 'ipvlan'] },
        { name: 'internal', label: 'Internal (no outside access)', type: 'checkbox' },
      ],
      submitLabel: 'Create',
    });
    if (v)
      await run(() => api.createNetwork(v.name!.trim(), v.driver ?? 'bridge', v.internal === 'true'), {
        success: `Network ${v.name} created`,
        after: refresh,
      });
  };

  const columns: Column<Network>[] = [
    {
      key: 'name',
      label: 'Network',
      sort: (n) => n.name,
      render: (n) => (
        <div className="name-cell">
          <span className="key">{n.name}</span>
          {n.builtin && <Pill>built-in</Pill>}
          {n.project && <span className="muted small">{n.project}</span>}
        </div>
      ),
    },
    { key: 'driver', label: 'Driver', sort: (n) => n.driver, render: (n) => <Pill>{n.driver}</Pill> },
    { key: 'subnet', label: 'Subnet', secondary: true, sort: (n) => n.subnet, render: (n) => <span className="mono">{n.subnet ?? '—'}</span> },
    { key: 'gateway', label: 'Gateway', secondary: true, sort: (n) => n.gateway, render: (n) => <span className="mono muted">{n.gateway ?? '—'}</span> },
    { key: 'internal', label: 'Internal', sort: (n) => n.internal, render: (n) => (n.internal ? 'Yes' : <span className="muted">No</span>) },
    {
      key: 'containers',
      label: 'Attached containers',
      sort: (n) => n.containers.length,
      render: (n) =>
        n.containers.length ? (
          <div className="members">
            {n.containers.map((c) => (
              <span key={c} className="pill member">
                {c}
                {joinable(n) && (
                  <button type="button" className="member-x" aria-label={`Disconnect ${c}`} onClick={() => disconnect(n, c)}>
                    ×
                  </button>
                )}
              </span>
            ))}
          </div>
        ) : (
          <span className="muted">none</span>
        ),
    },
  ];

  return (
    <Card id="networks-card" title="Networks" sub="Built-in networks (bridge, host, none) can’t be removed">
      {poll.error && <ErrorLine>Couldn’t load networks: {poll.error}</ErrorLine>}
      {poll.loading && !poll.data ? (
        <Skeleton height={260} />
      ) : (
        <DataTable
          rows={rows}
          rowKey={(n) => n.id}
          columns={columns}
          searchText={(n) => [n.name, n.driver, n.subnet, n.containers.join(' '), n.project].join(' ')}
          searchPlaceholder="Search networks or containers…"
          initialSort={{ key: 'containers', dir: 'desc' }}
          actions={(n) => (
            <>
              <IconButton icon="inspect" label="Inspect" onClick={() => setInspect(n)} />
              {joinable(n) && <IconButton icon="link" label="Connect a container" onClick={() => void connect(n)} />}
              {!readOnly && !n.builtin && n.containers.length === 0 && (
                <IconButton
                  icon="trash"
                  label="Remove"
                  danger
                  onClick={() =>
                    run(() => api.removeNetwork(n.id), {
                      confirm: { title: `Remove network ${n.name}?`, body: <p>No containers are attached to it.</p>, confirmLabel: 'Remove', danger: true },
                      success: `Network ${n.name} removed`,
                      after: refresh,
                    })
                  }
                />
              )}
            </>
          )}
          toolbar={
            !readOnly && (
              <>
                <Button primary onClick={() => void create()}>
                  Create network
                </Button>
                <Button
                  danger
                  disabled={!rows.some((n) => !n.builtin && n.containers.length === 0)}
                  onClick={() =>
                    run(api.pruneNetworks, {
                      confirm: {
                        title: 'Remove unused networks?',
                        body: <p>Every user-defined network with no containers attached is removed.</p>,
                        confirmLabel: 'Prune',
                        danger: true,
                      },
                      success: (r) => `Removed ${(r as { NetworksDeleted?: string[] }).NetworksDeleted?.length ?? 0} networks`,
                      after: refresh,
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
      {inspect && <InspectViewer title={`Inspect · ${inspect.name}`} load={() => api.inspectNetwork(inspect.id)} onClose={() => setInspect(null)} />}
    </Card>
  );
}
