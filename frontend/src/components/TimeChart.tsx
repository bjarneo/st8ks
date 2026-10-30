import { memo, useEffect, useMemo, useRef, useState } from "react";
import { scale, ui } from "../lib/app";
import { useStore } from "../lib/store";
import type { MetricLine } from "../lib/metrics";

// A small line chart in plain SVG for the Metrics tab. Series colors come
// from the --c1 to --c7 tokens in metrics.css. Text uses the text tokens.

export interface ChartSeries { name: string; v: (number | null)[]; slot: number }

interface Props {
  start: number;
  step: number;
  n: number;
  series: ChartSeries[];
  lines: MetricLine[];
  /** fmt formats a value. With step, it formats an axis tick. */
  fmt: (v: number, step?: number) => string;
  /** binary picks the ticks and their labels in units of 1024, for bytes. */
  binary?: boolean;
  /** hover is the index under the pointer, in this chart or the other one. */
  hover: number | null;
  active: boolean;
  onHover: (i: number | null) => void;
  label: string;
}

const PLOT_H = 150;
const AXIS_H = 20;
const PAD_T = 14;

function niceStep(raw: number): number {
  const p = 10 ** Math.floor(Math.log10(raw));
  const f = raw / p;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * p;
}

/** decimals returns the decimals that a tick step needs, such as 3 for 0.025. */
export function decimals(step: number): number {
  return (String(+step.toPrecision(3)).split(".")[1] ?? "").length;
}

/** trimZeros removes trailing zeros after a decimal point. */
export const trimZeros = (s: string) => (s.includes(".") ? s.replace(/\.?0+$/, "") : s);

const BINARY = ["", "Ki", "Mi", "Gi", "Ti"];

/** yTicks returns about 4 clean ticks from 0 up to the first tick above max. */
function yTicks(max: number, binary: boolean): { ticks: number[]; unit: number; step: number } {
  let unit = 1;
  if (binary) while (unit * 1024 <= max && unit < 1024 ** 4) unit *= 1024;
  const step = niceStep(max / unit / 4) * unit;
  const ticks: number[] = [];
  for (let v = 0; v < max + step * 0.999; v += step) ticks.push(v);
  return { ticks, unit, step };
}

const X_STEPS = [60, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800];

function xTicks(t0: number, t1: number, maxTicks: number): { at: number[]; iv: number } {
  const span = t1 - t0;
  const iv = X_STEPS.find((s) => span / s <= maxTicks) ?? X_STEPS[X_STEPS.length - 1];
  const at: number[] = [];
  if (iv >= 86400) {
    const d = new Date(t0 * 1000);
    d.setHours(0, 0, 0, 0);
    for (let t = d.getTime() / 1000; t <= t1; ) {
      if (t >= t0) at.push(t);
      d.setDate(d.getDate() + iv / 86400);
      t = d.getTime() / 1000;
    }
    return { at, iv };
  }
  // Align the ticks to the local clock.
  const off = -new Date(t0 * 1000).getTimezoneOffset() * 60;
  for (let t = Math.ceil((t0 + off) / iv) * iv - off; t <= t1; t += iv) at.push(t);
  return { at, iv };
}

const two = (n: number) => String(n).padStart(2, "0");

function clock(t: number, seconds = false): string {
  const d = new Date(t * 1000);
  return `${two(d.getHours())}:${two(d.getMinutes())}${seconds ? ":" + two(d.getSeconds()) : ""}`;
}

function dayLabel(t: number): string {
  return new Date(t * 1000).toLocaleDateString(undefined, { weekday: "short", day: "numeric" });
}

/** pathOf draws a series and breaks the line where a value is missing. */
function pathOf(v: (number | null)[], x: (i: number) => number, y: (v: number) => number, base?: number): string {
  let d = "";
  let run: number[] = [];
  const flush = () => {
    if (!run.length) return;
    if (base !== undefined) {
      d += `M${x(run[0]).toFixed(1)} ${base}`;
      for (const i of run) d += `L${x(i).toFixed(1)} ${y(v[i]!).toFixed(1)}`;
      d += `L${x(run[run.length - 1]).toFixed(1)} ${base}Z`;
    } else {
      d += `M${x(run[0]).toFixed(1)} ${y(v[run[0]]!).toFixed(1)}`;
      // A lone point shows as a dot through the round line cap.
      if (run.length === 1) d += "l0.01 0";
      for (let k = 1; k < run.length; k++) d += `L${x(run[k]).toFixed(1)} ${y(v[run[k]]!).toFixed(1)}`;
    }
    run = [];
  };
  for (let i = 0; i < v.length; i++) {
    if (v[i] === null || v[i] === undefined) flush();
    else run.push(i);
  }
  flush();
  return d;
}

export const TimeChart = memo(function TimeChart(p: Props) {
  const wrap = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(0);
  const zf = useStore(ui, (s) => scale(s.settings));
  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const t1 = p.start + (p.n - 1) * p.step;
  const geo = useMemo(() => {
    let max = 0;
    for (const s of p.series) for (const v of s.v) if (v !== null && v > max) max = v;
    // A line far above the data would flatten it, so it shows only as text.
    const shown = p.lines.filter((l) => max === 0 || l.v <= max * 4);
    for (const l of shown) max = Math.max(max, l.v);
    if (max <= 0) max = p.binary ? 1 << 20 : 0.01;
    const { ticks, unit, step } = yTicks(max * 1.04, !!p.binary);
    const top = ticks[ticks.length - 1];
    // Binary ticks share one unit, so 512Ki shows as 0.5Mi next to 1Mi.
    const suffix = BINARY[Math.round(Math.log2(unit) / 10)];
    const labels = ticks.map((v) => (v === 0 ? "0" : p.binary ? trimZeros((v / unit).toFixed(decimals(step / unit))) + suffix : p.fmt(v, step)));
    // One left margin for every chart, so the CPU and memory charts share the time axis.
    const padL = Math.ceil(Math.max(6, ...labels.map((l) => l.length)) * 6.4 * zf) + 10;
    const plotW = Math.max(40, w - padL - 6);
    const x = (i: number) => padL + (p.n > 1 ? (i / (p.n - 1)) * plotW : 0);
    const y = (v: number) => PAD_T + PLOT_H - (v / top) * PLOT_H;
    return { ticks, labels, top, padL, plotW, x, y, shown, hidden: p.lines.filter((l) => !shown.includes(l)) };
  }, [p.series, p.lines, p.binary, p.fmt, p.n, w, zf]);

  const paths = useMemo(() => {
    const base = PAD_T + PLOT_H;
    return p.series.map((s) => ({
      slot: s.slot,
      line: pathOf(s.v, geo.x, geo.y),
      area: p.series.length === 1 ? pathOf(s.v, geo.x, geo.y, base) : "",
    }));
  }, [p.series, geo]);

  const xt = useMemo(() => xTicks(p.start, t1, Math.max(2, Math.floor(geo.plotW / (80 * zf)))), [p.start, t1, geo.plotW, zf]);
  const xLabel = (t: number) => (xt.iv >= 86400 ? dayLabel(t) : clock(t));

  const pick = (clientX: number) => {
    const r = wrap.current!.getBoundingClientRect();
    const i = Math.round(((clientX - r.left - geo.padL) / geo.plotW) * (p.n - 1));
    return Math.max(0, Math.min(p.n - 1, i));
  };
  const onKey = (e: React.KeyboardEvent) => {
    const cur = p.hover ?? p.n - 1;
    let next: number | null = cur;
    if (e.key === "ArrowLeft") next = Math.max(0, cur - 1);
    else if (e.key === "ArrowRight") next = Math.min(p.n - 1, cur + 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = p.n - 1;
    else return;
    e.preventDefault();
    e.stopPropagation();
    p.onHover(next);
  };

  const h = p.hover;
  const hx = h !== null ? geo.x(h) : 0;
  const rows = h === null ? [] : p.series
    .filter((s) => s.v[h] !== null && s.v[h] !== undefined)
    .map((s) => ({ s, v: s.v[h] as number }))
    .sort((a, b) => b.v - a.v);
  const span = t1 - p.start;
  const tipTime = h === null ? "" : (span > 20 * 3600 ? dayLabel(p.start + h * p.step) + " " : "") + clock(p.start + h * p.step, p.step < 60);

  return (
    <div ref={wrap} className="mx-chart" tabIndex={0} role="img" aria-label={p.label}
      onKeyDown={onKey} onFocus={() => p.hover === null && p.onHover(p.n - 1)} onBlur={() => p.onHover(null)}>
      {w > 0 && (
        <svg width={w} height={PAD_T + PLOT_H + AXIS_H}
          onPointerMove={(e) => p.onHover(pick(e.clientX))} onPointerLeave={() => p.onHover(null)}>
          {geo.ticks.map((v, i) => (
            <g key={i}>
              <line className="mx-grid" x1={geo.padL} x2={geo.padL + geo.plotW} y1={Math.round(geo.y(v)) + 0.5} y2={Math.round(geo.y(v)) + 0.5} />
              <text className="mx-ax" x={geo.padL - 6} y={geo.y(v) + 3.5} textAnchor="end">{geo.labels[i]}</text>
            </g>
          ))}
          {xt.at.map((t) => {
            const xx = geo.x((t - p.start) / p.step);
            return <text key={t} className="mx-ax" x={xx} y={PAD_T + PLOT_H + 14} textAnchor="middle">{xLabel(t)}</text>;
          })}
          {paths.map((s, i) => s.area && <path key={"a" + i} className={"mx-area c" + s.slot} d={s.area} />)}
          {paths.map((s, i) => <path key={i} className={"mx-line c" + s.slot} d={s.line} />)}
          {geo.shown.map((l) => {
            const yy = Math.round(geo.y(l.v)) + 0.5;
            const below = l.kind === "request" && yy < PAD_T + 12;
            return (
              <g key={l.kind + l.label}>
                <line className={"mx-ref " + l.kind} x1={geo.padL} x2={geo.padL + geo.plotW} y1={yy} y2={yy} />
                <text className="mx-reft" x={geo.padL + geo.plotW - 2} y={l.kind === "request" && !below ? yy + 11 : yy - 4} textAnchor="end">{l.label} {p.fmt(l.v)}</text>
              </g>
            );
          })}
          {h !== null && (
            <g>
              <line className="mx-cross" x1={Math.round(hx) + 0.5} x2={Math.round(hx) + 0.5} y1={PAD_T} y2={PAD_T + PLOT_H} />
              {rows.map(({ s, v }) => <circle key={s.name} className={"mx-dot c" + s.slot} cx={hx} cy={geo.y(v)} r={4} />)}
            </g>
          )}
        </svg>
      )}
      {geo.hidden.length > 0 && (
        <div className="mx-above small fa">{geo.hidden.map((l) => `${l.label} ${p.fmt(l.v)}`).join(" · ")} · above the chart</div>
      )}
      {p.active && h !== null && (
        <div className="mx-tip" style={hx > w / 2 ? { right: w - hx + 12 } : { left: hx + 12 }}>
          <div className="fa small">{tipTime}</div>
          {rows.length === 0 && <div className="mu small">No data</div>}
          {rows.map(({ s, v }) => (
            <div key={s.name} className="mx-tiprow">
              <span className={"mx-key c" + s.slot} />
              <span className="mx-tipv">{p.fmt(v)}</span>
              <span className="mu ell">{s.name}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
});
