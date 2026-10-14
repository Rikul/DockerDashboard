import { useMemo, useState } from 'react';
import { scaleLinear, scaleTime, scaleBand } from 'd3-scale';
import { line } from 'd3-shape';
import { leastIndex, max, extent } from 'd3-array';
import { useWidth } from '../hooks';
import { fmtBytes, fmtPct } from '../format';
import type { Container, StatsSeries } from '../types';

export const SERIES_COLORS = ['var(--c1)', 'var(--c2)', 'var(--c3)', 'var(--c4)', 'var(--c5)'];

let canvas: HTMLCanvasElement | undefined;
/** Text width at the chart label font, for laying out margins. */
function textWidth(s: string): number {
  canvas ??= document.createElement('canvas');
  const ctx = canvas.getContext('2d');
  if (!ctx) return s.length * 7;
  ctx.font = '12px system-ui, -apple-system, "Segoe UI", sans-serif';
  return ctx.measureText(s).width;
}

/** CPU of the busiest containers over time, labelled at the line ends. */
export function CpuChart({ series, top = 5, height = 240 }: { series: StatsSeries[]; top?: number; height?: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);

  const data = useMemo(
    () =>
      series.slice(0, top).map((s, i) => ({
        id: s.id,
        name: s.name,
        color: SERIES_COLORS[i % SERIES_COLORS.length]!,
        pts: s.samples.map((p) => ({ t: new Date(p.ts), v: p.cpu_pct })),
      })),
    [series, top],
  );

  const W = Math.max(width, 280);
  const H = height;
  const all = data.flatMap((d) => d.pts);
  const yMax = Math.max(100, max(all, (p) => p.v) ?? 0);
  const endLabel = (d: (typeof data)[number]) => `${d.name} ${fmtPct(d.pts.at(-1)?.v)}`;
  const m = {
    t: 8,
    b: 22,
    l: textWidth(`${Math.round(yMax)}%`) + 10,
    r: Math.min(W * 0.4, (max(data, (d) => textWidth(endLabel(d))) ?? 0) + 14),
  };
  const [t0, t1] = extent(all, (p) => p.t) as [Date | undefined, Date | undefined];
  const x = scaleTime([t0 ?? new Date(Date.now() - 3600e3), t1 ?? new Date()], [m.l, W - m.r]);
  const y = scaleLinear([0, yMax], [H - m.b, m.t]).nice();
  const path = line<{ t: Date; v: number }>()
    .x((p) => x(p.t))
    .y((p) => y(p.v));

  // End labels: start at each line's last value, then push apart.
  const labels = data
    .map((d) => ({ d, y: y(d.pts.at(-1)?.v ?? 0) }))
    .sort((a, b) => a.y - b.y);
  // Push down to clear the label above, then back up from the bottom edge,
  // so crowding at one end never moves labels at the other end off-canvas.
  for (let i = 1; i < labels.length; i++) labels[i]!.y = Math.max(labels[i]!.y, labels[i - 1]!.y + 14);
  for (let i = labels.length - 1; i >= 0; i--) {
    const limit = i === labels.length - 1 ? H - m.b : labels[i + 1]!.y - 14;
    labels[i]!.y = Math.max(m.t + 6, Math.min(labels[i]!.y, limit));
  }

  const times = data[0]?.pts.map((p) => p.t) ?? [];
  const hoverT = hover != null ? times[hover] : undefined;
  const nTicks = Math.max(2, Math.floor((W - m.l - m.r) / 80));
  const xTicks = x.ticks(nTicks);
  // d3's multi-scale format: seconds when the window is short, else HH:MM.
  const fmtTick = x.tickFormat(nTicks);

  return (
    <div className="chart" ref={ref} style={{ minHeight: H }}>
      {width > 0 && (
        <svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`} role="img" aria-label="CPU usage over time">
          {y.ticks(4).map((v) => (
            <g key={v}>
              <line x1={m.l} x2={W - m.r} y1={y(v)} y2={y(v)} className="grid" />
              <text x={m.l - 6} y={y(v)} dy="0.32em" textAnchor="end">
                {v}%
              </text>
            </g>
          ))}
          {xTicks.map((t) => (
            <text key={+t} x={x(t)} y={H - 6} textAnchor="middle">
              {fmtTick(t)}
            </text>
          ))}
          <line x1={m.l} x2={W - m.r} y1={y(0)} y2={y(0)} className="axis" />
          {data.map((d) => (
            <path
              key={d.id}
              d={path(d.pts) ?? ''}
              fill="none"
              stroke={d.color}
              strokeWidth={2}
              strokeLinecap="round"
              strokeLinejoin="round"
              opacity={hoverT ? 0.85 : 1}
            />
          ))}
          {labels.map(({ d, y: ly }) => (
            <text key={d.id} x={W - m.r + 6} y={ly} dy="0.32em">
              <tspan className="strong">{d.name}</tspan> {fmtPct(d.pts.at(-1)?.v)}
            </text>
          ))}
          {hoverT && (
            <g>
              <line x1={x(hoverT)} x2={x(hoverT)} y1={m.t} y2={H - m.b} className="reference" />
              {data.map((d) => {
                const p = d.pts.find((q) => +q.t === +hoverT);
                return p ? <circle key={d.id} cx={x(p.t)} cy={y(p.v)} r={3.5} fill={d.color} /> : null;
              })}
            </g>
          )}
          <rect
            x={m.l}
            y={m.t}
            width={Math.max(0, W - m.l - m.r)}
            height={H - m.t - m.b}
            fill="transparent"
            onPointerMove={(e) => {
              const box = e.currentTarget.ownerSVGElement!.getBoundingClientRect();
              const px = ((e.clientX - box.left) / box.width) * W;
              const ti = x.invert(px);
              const i = leastIndex(times, (t) => Math.abs(+t - +ti));
              setHover(i ?? null);
            }}
            onPointerLeave={() => setHover(null)}
          />
        </svg>
      )}
      {hoverT && (
        <div className="tip" style={{ left: `clamp(0px, calc(${(x(hoverT) / W) * 100}% + 12px), calc(100% - 180px))`, top: 8 }}>
          <div className="tip-h">{hoverT.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</div>
          {data
            .map((d) => ({ d, p: d.pts.find((q) => +q.t === +hoverT) }))
            .filter((o) => o.p)
            .sort((a, b) => b.p!.v - a.p!.v)
            .map(({ d, p }) => (
              <div key={d.id}>
                <span className="sw" style={{ background: d.color }} />
                {d.name} <b>{fmtPct(p!.v)}</b>
              </div>
            ))}
        </div>
      )}
    </div>
  );
}

/** Memory per running container against its limit (or host memory). */
export function MemChart({ containers, hostMem, rows = 10 }: { containers: Container[]; hostMem: number; rows?: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<string | null>(null);
  const data = containers
    .filter((c) => c.state === 'running' && c.mem_bytes != null)
    .map((c) => ({
      name: c.name,
      mem: c.mem_bytes!,
      lim: c.mem_limit_set && c.mem_limit ? c.mem_limit : 0,
    }))
    .sort((a, b) => b.mem - a.mem)
    .slice(0, rows);

  const W = Math.max(width, 280);
  const rowH = 24;
  const H = Math.max(120, data.length * rowH + 26);
  const valLabel = (d: (typeof data)[number]) => (d.lim ? `${fmtBytes(d.mem)} / ${fmtBytes(d.lim)}` : `${fmtBytes(d.mem)} · no limit`);
  const m = {
    l: Math.min(W * 0.35, (max(data, (d) => textWidth(d.name)) ?? 0) + 10),
    r: (max(data, (d) => textWidth(valLabel(d))) ?? 0) + 12,
    t: 4,
    b: 22,
  };
  const xMax = max(data, (d) => Math.max(d.mem, d.lim)) || hostMem || 1;
  const x = scaleLinear([0, xMax], [m.l, Math.max(m.l + 10, W - m.r)]);
  const y = scaleBand(
    data.map((d) => d.name),
    [m.t, H - m.b],
  ).padding(0.35);
  const ticks = x.ticks(Math.max(2, Math.floor((W - m.l - m.r) / 90)));

  return (
    <div className="chart" ref={ref} style={{ minHeight: H }}>
      {width > 0 && data.length > 0 && (
        <svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Memory by container">
          {ticks.map((t) => (
            <g key={t}>
              <line x1={x(t)} x2={x(t)} y1={m.t} y2={H - m.b} className="grid" />
              <text x={x(t)} y={H - 6} textAnchor="middle">
                {fmtBytes(t)}
              </text>
            </g>
          ))}
          {data.map((d) => {
            const yy = y(d.name) ?? 0;
            const bw = y.bandwidth();
            const hot = d.lim > 0 && d.mem / d.lim >= 0.85;
            return (
              <g key={d.name} onPointerEnter={() => setHover(d.name)} onPointerLeave={() => setHover(null)}>
                <rect x={0} y={yy - (y.step() - bw) / 2} width={W} height={y.step()} fill="transparent" />
                <text x={m.l - 8} y={yy + bw / 2} dy="0.32em" textAnchor="end" className="strong">
                  {d.name}
                </text>
                {d.lim > 0 && <rect x={m.l} y={yy} width={x(d.lim) - m.l} height={bw} rx={4} className="track" />}
                <rect
                  x={m.l}
                  y={yy}
                  width={Math.max(2, x(d.mem) - m.l)}
                  height={bw}
                  rx={4}
                  fill={hot ? 'var(--bad)' : 'var(--c2)'}
                  opacity={hover && hover !== d.name ? 0.45 : 1}
                />
                <text x={x(Math.max(d.mem, d.lim)) + 6} y={yy + bw / 2} dy="0.32em">
                  {valLabel(d)}
                </text>
              </g>
            );
          })}
        </svg>
      )}
      {width > 0 && data.length === 0 && <p className="muted center">No running containers</p>}
    </div>
  );
}
