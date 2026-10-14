import type {
  Container,
  Image,
  Network,
  PruneReport,
  RegistryResponse,
  Session,
  StatsResponse,
  SystemInfo,
  Volume,
} from './types';

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// Fired when any call comes back 401, so the app can show the sign-in screen.
export const UNAUTHORIZED_EVENT = 'dd:unauthorized';

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Requested-With': 'dockerdashboard' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const res = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let data: unknown = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = text;
  }
  if (!res.ok) {
    if (res.status === 401 && path !== '/api/login') window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    const msg =
      data && typeof data === 'object' && 'error' in data ? String((data as { error: unknown }).error) : `HTTP ${res.status}`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

const enc = encodeURIComponent;

export const api = {
  session: () => request<Session>('GET', '/api/session'),
  login: (token: string) => request<{ authenticated: boolean }>('POST', '/api/login', { token }),
  logout: () => request<{ authenticated: boolean }>('POST', '/api/logout'),

  system: () => request<SystemInfo>('GET', '/api/system'),
  stats: (minutes = 60) => request<StatsResponse>('GET', `/api/stats?minutes=${minutes}`),

  containers: () => request<Container[]>('GET', '/api/containers'),
  inspectContainer: (id: string) => request<unknown>('GET', `/api/containers/${enc(id)}`),
  containerAction: (id: string, action: 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'kill') =>
    request<{ status: string }>('POST', `/api/containers/${enc(id)}/${action}`),
  removeContainer: (id: string, force = false, volumes = false) =>
    request<{ status: string }>('DELETE', `/api/containers/${enc(id)}?force=${force}&volumes=${volumes}`),
  pruneContainers: () => request<PruneReport>('POST', '/api/containers/prune'),
  logsUrl: (id: string, tail: number, follow: boolean, timestamps: boolean) =>
    `/api/containers/${enc(id)}/logs?tail=${tail}&follow=${follow ? 1 : 0}&timestamps=${timestamps ? 1 : 0}`,

  images: () => request<Image[]>('GET', '/api/images'),
  inspectImage: (id: string) => request<unknown>('GET', `/api/images/${enc(id)}`),
  removeImage: (id: string, force = false) => request<unknown>('DELETE', `/api/images/${enc(id)}?force=${force}`),
  pullImage: (ref: string) => request<{ status: string }>('POST', '/api/images/pull', { ref }),
  pruneImages: (all: boolean) => request<PruneReport>('POST', `/api/images/prune?all=${all}`),

  volumes: (refresh = false) => request<Volume[]>('GET', `/api/volumes${refresh ? '?refresh=1' : ''}`),
  createVolume: (name: string, driver = 'local') => request<unknown>('POST', '/api/volumes', { name, driver }),
  removeVolume: (name: string, force = false) => request<unknown>('DELETE', `/api/volumes/${enc(name)}?force=${force}`),
  pruneVolumes: (all: boolean) => request<PruneReport>('POST', `/api/volumes/prune?all=${all}`),

  networks: () => request<Network[]>('GET', '/api/networks'),
  createNetwork: (name: string, driver: string, internal: boolean) =>
    request<{ id: string }>('POST', '/api/networks', { name, driver, internal }),
  removeNetwork: (id: string) => request<unknown>('DELETE', `/api/networks/${enc(id)}`),
  connectNetwork: (id: string, container: string) =>
    request<unknown>('POST', `/api/networks/${enc(id)}/connect`, { container }),
  disconnectNetwork: (id: string, container: string, force = false) =>
    request<unknown>('POST', `/api/networks/${enc(id)}/disconnect`, { container, force }),
  pruneNetworks: () => request<PruneReport>('POST', '/api/networks/prune'),

  registry: (refresh = false) => request<RegistryResponse>('GET', `/api/registry${refresh ? '?refresh=1' : ''}`),
  deleteRegistryTag: (repository: string, tag: string) =>
    request<unknown>('DELETE', `/api/registry/${repository.split('/').map(enc).join('/')}?tag=${enc(tag)}`),
};
