// JSON shapes returned by the Go API (backend/internal/api/handlers.go).

export interface Session {
  authenticated: boolean;
  auth_required: boolean;
  read_only: boolean;
}

export interface SystemInfo {
  name: string;
  server_version: string;
  api_version: string;
  os: string;
  arch: string;
  kernel: string;
  cpus: number;
  mem_total: number;
  storage_driver: string;
  read_only: boolean;
  registry: string | null;
  stats_interval_sec: number;
}

export interface Port {
  ip?: string;
  private_port: number;
  public_port?: number;
  type: string;
}

export interface Mount {
  type: string;
  name?: string;
  source: string;
  destination: string;
  rw: boolean;
}

export type ContainerState = 'created' | 'running' | 'paused' | 'restarting' | 'removing' | 'exited' | 'dead';

export interface Container {
  id: string;
  name: string;
  image: string;
  image_id: string;
  command: string;
  created: string;
  state: ContainerState;
  status: string;
  health?: 'healthy' | 'unhealthy' | 'starting' | 'none';
  exit_code?: number;
  oom_killed?: boolean;
  restart_count: number;
  restart_policy?: string;
  started_at?: string;
  ports: Port[];
  networks: string[];
  mounts: Mount[];
  project?: string;
  cpu_pct?: number;
  mem_bytes?: number;
  mem_limit?: number;
  mem_limit_set: boolean;
}

export interface Image {
  id: string;
  short_id: string;
  ref: string;
  repo_tags: string[];
  repository: string;
  tag: string;
  created: string;
  size: number;
  containers: number;
  dangling: boolean;
}

export interface Volume {
  name: string;
  driver: string;
  mountpoint: string;
  created?: string;
  size: number; // -1 when unknown
  used_by: string[];
  anonymous: boolean;
  project?: string;
}

export interface Network {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
  subnet?: string;
  gateway?: string;
  created?: string;
  containers: string[];
  builtin: boolean;
  project?: string;
}

export interface StatsSample {
  ts: string;
  cpu_pct: number;
  mem_bytes: number;
  mem_limit: number;
}

export interface StatsSeries {
  id: string;
  name: string;
  samples: StatsSample[];
}

export interface StatsResponse {
  interval_sec: number;
  series: StatsSeries[];
}

export interface RegistryTag {
  ref: string;
  repository: string;
  tag: string;
  digest: string;
  media_type: string;
  size: number;
  created?: string;
  platforms?: string[];
  error?: string;
}

export interface RegistryResponse {
  configured: boolean;
  host?: string;
  tags: RegistryTag[];
}

export interface PruneReport {
  ContainersDeleted?: string[];
  ImagesDeleted?: unknown[];
  VolumesDeleted?: string[];
  NetworksDeleted?: string[];
  SpaceReclaimed: number;
}

export interface LogLine {
  stream: 'stdout' | 'stderr';
  text: string;
}
