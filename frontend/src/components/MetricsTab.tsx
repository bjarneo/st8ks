import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import "../metrics.css";
import { call, errText } from "../lib/bridge";
import { ui } from "../lib/app";
import { METRIC_RANGES, type MetricLine, type MetricPanel, type MetricsData, type MetricsRange, type MetricSeries } from "../lib/metrics";
import { decimals, TimeChart, trimZeros as trim, type ChartSeries } from "./TimeChart";

// The Metrics tab of the detail panel. It asks the backend when it shows,
// and again every 30 seconds while it shows. Nothing polls in the background.

const REFRESH_MS = 30_000;

// The last range stays for the next object.
let lastRange: MetricsRange = "1h";

/** fmtCores formats cores like kubectl, in millicores below one core. step is the axis tick step. */
export function fmtCores(v: number, step?: number): string {
  if (v === 0) return "0";
  const big = step !== undefined ? step >= 0.5 : v >= 1;
  if (big) return trim(v.toFixed(step !== undefined ? decimals(step) : v < 10 ? 2 : 1));
  const m = v * 1000;
  if (step !== undefined) return `${trim(m.toFixed(decimals(step * 1000)))}m`;
  return `${trim(m.toFixed(m < 1 ? 2 : m < 10 ? 1 : 0))}m`;
}

/** fmtMem formats bytes with binary units. */
export function fmtMem(b: number): string {
  if (b === 0) return "0";
  const units = ["", "Ki", "Mi", "Gi", "Ti"];
  let i = 0;
  while (b >= 1024 && i < units.length - 1) {
    b /= 1024;
    i++;
  }
  return trim(b.toFixed(i === 0 || b >= 100 ? 0 : 1)) + units[i];
}

/** span formats seconds as 15s, 90s, 5m or 1h. */
function span(sec: number): string {
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  if (sec % 60 === 0 && sec >= 300) return `${sec / 60}m`;
  return `${sec}s`;
}

/** openMetricsSettings opens Settings at the Metrics section. */
export function openMetricsSettings() {
  ui.set({ settingsOpen: true });
  window.setTimeout(() => document.getElementById("settings-metrics")?.scrollIntoView({ block: "start" }), 40);
}

function stats(v: (number | null)[]): { now: number | null; avg: number | null; peak: number | null } {
  let now: number | null = null;
  let sum = 0;
  let n = 0;
  let peak: number | null = null;
  for (const x of v) {
    if (x === null) continue;
    now = x;
    sum += x;
    n++;
    if (peak === null || x > peak) peak = x;
  }
  return { now, avg: n ? sum / n : null, peak };
}

/** sharedLines returns the lines that every part has, so the chart can draw them once. */
function sharedLines(parts: MetricSeries[]): MetricLine[] {
  if (!parts.length) return [];
  const key = (s: MetricSeries) => JSON.stringify(s.lines ?? []);
  const k = key(parts[0]);
  return parts.every((p) => key(p) === k) ? parts[0].lines ?? [] : [];
}

interface PanelProps {
  id: "cpu" | "mem";
  title: string;
  unit: string;
  panel: MetricPanel;
  data: MetricsData;
  byParts: boolean;
  totalName: string;
  fmt: (v: number, step?: number) => string;
  binary?: boolean;
  hover: { i: number; c: "cpu" | "mem" } | null;
  onHover: (h: { i: number; c: "cpu" | "mem" } | null) => void;
}

const Panel = memo(function Panel(p: PanelProps) {
  const { panel, data } = p;
  const parts = p.byParts && panel.parts.length > 0;
  const series = useMemo<ChartSeries[]>(() => {
    if (parts) return panel.parts.map((s, i) => ({ name: s.name, v: s.v, slot: i + 1 }));
    if (!panel.total) return [];
    return [{ name: panel.parts.length === 1 ? panel.parts[0].name : p.totalName, v: panel.total.v, slot: 1 }];
  }, [panel, parts, p.totalName]);
  const lines = useMemo(() => (parts ? sharedLines(panel.parts) : panel.total?.lines ?? []), [panel, parts]);
  const own = parts && lines.length === 0;
  const one = series.length === 1 ? stats(series[0].v) : null;
  const onHover = useCallback((i: number | null) => p.onHover(i === null ? null : { i, c: p.id }), [p.onHover, p.id]);
  const what = data.parts === "pod" ? "pods" : "containers";

  return (
    <div className="mx-panel">
      <div className="mx-head">
        <span className="mx-title">{p.title}</span>
        <span className="fa small">{p.unit}</span>
        <span className="grow" />
        {one && (
          <span className="mx-stats">
            <span><span className="fa">now</span> {one.now === null ? "—" : p.fmt(one.now)}</span>
            <span><span className="fa">avg</span> {one.avg === null ? "—" : p.fmt(one.avg)}</span>
            <span><span className="fa">peak</span> {one.peak === null ? "—" : p.fmt(one.peak)}</span>
          </span>
        )}
      </div>
      {series.length === 0 ? (
        <div className="dashed small">No data in this range.</div>
      ) : (
        <TimeChart start={data.start} step={data.step} n={data.n} series={series} lines={lines} fmt={p.fmt} binary={p.binary}
          hover={p.hover?.i ?? null} active={p.hover?.c === p.id} onHover={onHover}
          label={`${p.title} of ${series.map((s) => s.name).join(", ")} over the last ${data.range}`} />
      )}
      {series.length > 1 && (
        <table className="mx-legend">
          <thead><tr><th /><th>{data.parts === "pod" ? "Pod" : "Container"}</th><th>Now</th><th>Avg</th><th>Peak</th></tr></thead>
          <tbody>
            {series.map((s, i) => {
              const st = stats(s.v);
              const ls = own ? panel.parts[i].lines ?? [] : [];
              return (
                <tr key={s.name}>
                  <td><span className={"mx-key c" + s.slot} /></td>
                  <td className="ell" title={s.name}>
                    {s.name}
                    {ls.length > 0 && <span className="fa"> · {ls.map((l) => `${l.label} ${p.fmt(l.v)}`).join(" · ")}</span>}
                  </td>
                  <td>{st.now === null ? "—" : p.fmt(st.now)}</td>
                  <td>{st.avg === null ? "—" : p.fmt(st.avg)}</td>
                  <td>{st.peak === null ? "—" : p.fmt(st.peak)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {parts && panel.more > 0 && (
        <div className="small fa">{panel.more} more {what} are not shown. These are the {series.length} with the highest peak. Total includes all {what}.</div>
      )}
    </div>
  );
});

function SourceLine({ data }: { data: MetricsData }) {
  const s = data.source;
  if (!s.target) return null;
  return (
    <span className="mono small fa ell" title={s.target}>
      {s.flavor} {s.target}{s.via === "proxy" ? " · API server proxy" : ""}{s.mode !== "auto" ? " · from Settings" : ""}
    </span>
  );
}

function State({ data }: { data: MetricsData }) {
  const s = data.source;
  switch (data.status) {
    case "none":
      return (
        <div className="mx-state">
          <div className="mx-title">No Prometheus found</div>
          <span className="mu">{data.message}</span>
          {s.searched && (
            <>
              <div className="sec-label">st8ks looked for</div>
              <ul>{s.searched.map((l) => <li key={l}>{l}</li>)}</ul>
            </>
          )}
          <span className="mu">If your Prometheus has another name or runs outside the cluster, set it in Settings.</span>
          <div><button className="btn sm" onClick={openMetricsSettings}>Set the metrics source…</button></div>
        </div>
      );
    case "forbidden":
      return (
        <div className="mx-state">
          <div className="mx-title">No permission to query Prometheus</div>
          <span className="mu">{data.message}</span>
          {s.missing && <div className="kv"><span>Missing permission</span><span>{s.missing}</span></div>}
          {s.missing && <code className="mx-cmd">kubectl auth can-i get services --subresource=proxy -n {s.missing.split(" ").pop()}</code>}
          <span className="mu">Ask your cluster admin for the permission, or set a URL that you can reach in Settings.</span>
          <div><button className="btn sm" onClick={openMetricsSettings}>Set the metrics source…</button></div>
        </div>
      );
    case "nodata":
      return (
        <div className="mx-state">
          <div className="mx-title">No data for this object</div>
          <span className="mu">{data.message}</span>
        </div>
      );
    default:
      return (
        <div className="mx-state">
          <div className="mx-title">The query failed</div>
          <div className="errbox">{data.message}</div>
          <div><button className="btn sm" onClick={openMetricsSettings}>Check the metrics source…</button></div>
        </div>
      );
  }
}

export default function MetricsTab({ kind, ns, name }: { kind: string; ns: string; name: string }) {
  const [range, setRangeState] = useState<MetricsRange>(lastRange);
  const [data, setData] = useState<MetricsData | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [split, setSplit] = useState<boolean | null>(null);
  const [hover, setHover] = useState<{ i: number; c: "cpu" | "mem" } | null>(null);
  const seq = useRef(0);
  const setRange = (r: MetricsRange) => {
    lastRange = r;
    setRangeState(r);
  };

  const load = useCallback(async () => {
    const my = ++seq.current;
    setBusy(true);
    try {
      const d = await call<MetricsData>("Metrics", { kind, ns, name }, range);
      if (my !== seq.current) return;
      setData(d);
      setErr("");
    } catch (e) {
      if (my !== seq.current) return;
      setErr(errText(e));
    } finally {
      if (my === seq.current) setBusy(false);
    }
  }, [kind, ns, name, range]);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, REFRESH_MS);
    return () => {
      clearInterval(t);
      seq.current++;
    };
  }, [load]);

  // Coalesce pointer moves to one render per frame.
  const pending = useRef<{ i: number; c: "cpu" | "mem" } | null | undefined>(undefined);
  const onHover = useCallback((h: { i: number; c: "cpu" | "mem" } | null) => {
    if (pending.current === undefined) {
      requestAnimationFrame(() => {
        const next = pending.current;
        pending.current = undefined;
        setHover((cur) => (cur?.i === next?.i && cur?.c === next?.c ? cur : next ?? null));
      });
    }
    pending.current = h;
  }, []);

  const ok = data?.status === "ok";
  const canSplit = ok && (data.cpu.parts.length > 1 || data.mem.parts.length > 1);
  const byParts = split ?? data?.parts === "container";
  const totalName = kind === "Pods" ? "all containers" : kind === "Nodes" ? name : "all pods";
  const stale = busy && !!data && data.range !== range;

  return (
    <div className="mx">
      <div className="mx-bar">
        <div className="seg">
          {METRIC_RANGES.map((r) => <button key={r} className={range === r ? "on" : ""} onClick={() => setRange(r)}>{r}</button>)}
        </div>
        {canSplit && (
          <div className="seg">
            <button className={!byParts ? "on" : ""} onClick={() => setSplit(false)}>Total</button>
            <button className={byParts ? "on" : ""} onClick={() => setSplit(true)}>{data.parts === "pod" ? "Per pod" : "Per container"}</button>
          </div>
        )}
        <span className="grow" />
        {busy && data && <span className="spinner" />}
        {data && <SourceLine data={data} />}
      </div>
      {err && <div className="errbox">{err}</div>}
      {!data && !err && <div className="flex mu"><span className="spinner" />Loading metrics…</div>}
      {data && !ok && <State data={data} />}
      {ok && (
        <div className={"col mx-charts" + (stale ? " stale" : "")}>
          <Panel id="cpu" title="CPU" unit="cores" panel={data.cpu} data={data} byParts={byParts} totalName={totalName} fmt={fmtCores} hover={hover} onHover={onHover} />
          <Panel id="mem" title="Memory" unit="working set" panel={data.mem} data={data} byParts={byParts} totalName={totalName} fmt={fmtMem} binary hover={hover} onHover={onHover} />
          <div className="small fa">
            {data.n} points, one every {span(data.step)}. CPU is the average rate over {span(Math.max(data.step, 300))}.
            {data.step > 60 ? ` Each memory point is the peak of its ${span(data.step)} step.` : ""} st8ks asks again every 30 seconds while this tab shows.
          </div>
        </div>
      )}
    </div>
  );
}
