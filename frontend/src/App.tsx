import { useCallback, useEffect, useState } from 'react';
import { api, UNAUTHORIZED_EVENT } from './api';
import { ActionsProvider } from './actions';
import { Button, ErrorLine } from './components/ui';
import { attentionReason, fmtBytes, reclaimableImages, sumSize, unusedVolumes } from './format';
import { useHash, usePoll } from './hooks';
import { ContainersPage } from './pages/Containers';
import { ImagesPage } from './pages/Images';
import { NetworksPage } from './pages/Networks';
import { OverviewPage } from './pages/Overview';
import { RegistryPage } from './pages/Registry';
import { VolumesPage } from './pages/Volumes';
import type { Session } from './types';

const TABS = ['overview', 'containers', 'images', 'volumes', 'networks', 'registry'] as const;
type Tab = (typeof TABS)[number];
const LABELS: Record<Tab, string> = {
  overview: 'Overview',
  containers: 'Containers',
  images: 'Images',
  volumes: 'Volumes',
  networks: 'Networks',
  registry: 'Registry',
};

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [sessionError, setSessionError] = useState<string>();

  const check = useCallback(() => {
    api.session().then(
      (s) => {
        setSession(s);
        setSessionError(undefined);
      },
      (e: unknown) => setSessionError(e instanceof Error ? e.message : String(e)),
    );
  }, []);

  useEffect(() => {
    check();
    const on = () => setSession((s) => (s ? { ...s, authenticated: false } : s));
    window.addEventListener(UNAUTHORIZED_EVENT, on);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, on);
  }, [check]);

  if (sessionError) {
    return (
      <main className="page narrow">
        <h1 className="title">Docker Dashboard</h1>
        <ErrorLine>Can’t reach the dashboard server: {sessionError}</ErrorLine>
        <Button onClick={check}>Try again</Button>
      </main>
    );
  }
  if (!session) return null;
  if (!session.authenticated) return <Login onDone={check} />;
  return (
    <ActionsProvider readOnly={session.read_only}>
      <Dashboard session={session} onSignOut={() => api.logout().finally(check)} />
    </ActionsProvider>
  );
}

function Login({ onDone }: { onDone: () => void }) {
  const [token, setToken] = useState('');
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await api.login(token);
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="page narrow">
      <div className="login card">
        <h1 className="title">Docker Dashboard</h1>
        <p className="sub">Sign in with the access token set in DASHBOARD_TOKEN on the server.</p>
        <div
          className="form"
          onKeyDown={(e) => {
            if (e.key === 'Enter' && token) void submit();
          }}
        >
          <label>
            <span>Access token</span>
            <input type="password" autoComplete="current-password" autoFocus value={token} onChange={(e) => setToken(e.target.value)} />
          </label>
        </div>
        {error && <ErrorLine>{error}</ErrorLine>}
        <Button primary disabled={!token || busy} onClick={() => void submit()}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </div>
    </main>
  );
}

function Dashboard({ session, onSignOut }: { session: Session; onSignOut: () => void }) {
  const [hash, setHash] = useHash('overview');
  const tab: Tab = (TABS as readonly string[]).includes(hash) ? (hash as Tab) : 'overview';
  const [filters, setFilters] = useState<Record<string, string>>({});
  const setFilter = (t: Tab) => (f: string) => setFilters((x) => ({ ...x, [t]: f }));

  // Containers change fastest; the rest are polled less often and refreshed after actions.
  const system = usePoll(api.system, 60_000);
  const containers = usePoll(api.containers, 5_000);
  const images = usePoll(api.images, 20_000);
  const volumes = usePoll(api.volumes, 30_000);
  const networks = usePoll(api.networks, 20_000);
  const stats = usePoll(() => api.stats(60), 5_000, tab === 'overview');

  const go = (t: string, filter?: string) => {
    if (filter) setFilters((x) => ({ ...x, [t]: filter }));
    setHash(t);
    window.scrollTo({ top: 0 });
  };

  const cs = containers.data ?? [];
  const running = cs.filter((c) => c.state === 'running').length;
  const attention = cs.filter((c) => attentionReason(c)).length;
  const reclaim = images.data && volumes.data ? sumSize(reclaimableImages(images.data)) + sumSize(unusedVolumes(volumes.data)) : null;
  const counts: Partial<Record<Tab, number | undefined>> = {
    containers: containers.data?.length,
    images: images.data?.length,
    volumes: volumes.data?.length,
    networks: networks.data?.length,
  };
  const refreshAll = () => {
    void containers.refresh();
    void images.refresh();
    void volumes.refresh();
    void networks.refresh();
  };

  return (
    <main className="page">
      <header className="head">
        <div className="head-row">
          <h1 className="title">{system.data?.name ? `Docker host · ${system.data.name}` : 'Docker host'}</h1>
          <div className="head-actions">
            {session.read_only && <span className="pill warn">read-only</span>}
            {session.auth_required && (
              <button type="button" className="btn" onClick={onSignOut}>
                Sign out
              </button>
            )}
          </div>
        </div>
        <p className="takeaway">
          {containers.data ? (
            <>
              <b>{running}</b> of <b>{cs.length}</b> containers running ·{' '}
              <button type="button" className={`link${attention ? ' bad-text' : ''}`} onClick={() => go('containers', 'attention')}>
                <b>{attention}</b> need attention
              </button>
              {reclaim != null && (
                <>
                  {' '}
                  · <b>{fmtBytes(reclaim)}</b> reclaimable
                </>
              )}
            </>
          ) : (
            ' '
          )}
        </p>
        {system.data && (
          <p className="note">
            Docker Engine {system.data.server_version} (API {system.data.api_version}) · {system.data.os} · {system.data.arch}
          </p>
        )}
      </header>

      <nav className="tabs" role="tablist" aria-label="Sections">
        {TABS.map((t) => (
          <button key={t} type="button" role="tab" className="tab" aria-selected={tab === t} onClick={() => go(t)}>
            {LABELS[t]}
            {counts[t] != null && <span className="count">{counts[t]}</span>}
          </button>
        ))}
      </nav>

      <div className="tab-page" role="tabpanel" key={tab}>
        {tab === 'overview' && (
          <OverviewPage system={system.data} containers={containers} images={images} volumes={volumes} stats={stats} go={go} />
        )}
        {tab === 'containers' && <ContainersPage poll={containers} filter={filters.containers ?? 'all'} onFilter={setFilter('containers')} />}
        {tab === 'images' && (
          <ImagesPage poll={images} onChanged={refreshAll} filter={filters.images ?? 'all'} onFilter={setFilter('images')} />
        )}
        {tab === 'volumes' && <VolumesPage poll={volumes} />}
        {tab === 'networks' && <NetworksPage poll={networks} containers={cs} onChanged={() => void containers.refresh()} />}
        {tab === 'registry' && <RegistryPage onPulled={() => void images.refresh()} />}
      </div>
    </main>
  );
}
