import type { Container, Image, Port, Volume } from './types';

export function fmtBytes(n: number | undefined | null): string {
  if (n == null || n < 0 || !Number.isFinite(n)) return '—';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export const fmtPct = (n: number | undefined) => (n == null ? '—' : `${n < 10 ? n.toFixed(1) : n.toFixed(0)}%`);

export const fmtCount = (n: number) => n.toLocaleString('en-US');

export function fmtAgo(iso: string | undefined, now = Date.now()): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '—';
  const s = Math.max(0, (now - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  const d = Math.round(s / 86400);
  if (d < 60) return d === 1 ? '1 day ago' : `${d} days ago`;
  return new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
}

export function fmtDuration(fromIso: string | undefined, now = Date.now()): string {
  if (!fromIso) return '—';
  const s = (now - Date.parse(fromIso)) / 1000;
  if (!Number.isFinite(s) || s < 0) return '—';
  if (s < 60) return `${Math.round(s)} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 172800) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} d`;
}

export function fmtPort(p: Port): string {
  return p.public_port ? `${p.public_port}→${p.private_port}/${p.type}` : `${p.private_port}/${p.type}`;
}

// Exit codes from `docker stop` (SIGTERM, SIGINT) are a normal shutdown.
const cleanExit = new Set([0, 130, 143]);

/** Why a container needs attention, or null if it doesn't. */
export function attentionReason(c: Container): string | null {
  const reasons: string[] = [];
  if (c.state === 'restarting') reasons.push(`Restart loop (${c.restart_count} restarts)`);
  if (c.state === 'dead') reasons.push('Dead: the daemon could not stop or remove it');
  if (c.health === 'unhealthy') reasons.push('Health check failing');
  if (c.oom_killed) reasons.push('Killed: out of memory');
  else if ((c.state === 'exited') && c.exit_code != null && !cleanExit.has(c.exit_code))
    reasons.push(`Exited with code ${c.exit_code}${c.exit_code === 137 ? ' (killed)' : ''}`);
  if (c.state === 'running' && c.mem_limit_set && c.mem_bytes != null && c.mem_limit)
    if (c.mem_bytes / c.mem_limit >= 0.85) reasons.push(`Memory at ${Math.round((c.mem_bytes / c.mem_limit) * 100)}% of limit`);
  return reasons.length ? reasons.join(' · ') : null;
}

export type Tone = 'ok' | 'warn' | 'bad' | 'muted';

export function containerStatus(c: Container): [Tone, string] {
  if (c.state === 'running') {
    if (c.health === 'unhealthy') return ['bad', 'Unhealthy'];
    if (c.health === 'starting') return ['warn', 'Starting'];
    return ['ok', c.health === 'healthy' ? 'Running · healthy' : 'Running'];
  }
  if (c.state === 'restarting') return ['bad', 'Restarting'];
  if (c.state === 'paused') return ['warn', 'Paused'];
  if (c.state === 'dead') return ['bad', 'Dead'];
  if (c.state === 'created') return ['muted', 'Created'];
  if (c.state === 'exited') {
    const code = c.exit_code ?? 0;
    return [cleanExit.has(code) && !c.oom_killed ? 'muted' : 'bad', `Exited (${code})`];
  }
  return ['muted', c.state];
}

export const reclaimableImages = (images: Image[]) => images.filter((i) => i.containers === 0);
export const unusedVolumes = (vols: Volume[]) => vols.filter((v) => v.used_by.length === 0);
export const sumSize = (xs: { size: number }[]) => xs.reduce((s, x) => s + Math.max(0, x.size), 0);
