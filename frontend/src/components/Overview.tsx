import { memo, useMemo, useRef } from "react";
import { goList, nsVisible, openAI, openDetail, saveSettings, ui } from "../lib/app";
import { cores, gib, pctColor, toneVar } from "../lib/format";
import { shallowEqual, useStore } from "../lib/store";
import { useTable } from "../lib/tables";
import type { Row, Usage } from "../lib/types";
import { sumCounts } from "./Tree";

interface CardProps { title: string; val: string; sub: string; use?: number; req?: number; lim?: number; trend?: number[] }

function Card({ title, val, sub, use, req, lim, trend }: CardProps) {
  const over = (lim ?? 0) > 100;
  return (
    <div className="card">
      <div className="flex" style={{ justifyContent: "space-between", alignItems: "baseline" }}>
        <span className="mu">{title}</span><span className="fa small">{sub}</span>
      </div>
      <div className="val">{val}</div>
      {trend && trend.length > 1 && (
        <div className="trend">
          {trend.map((v, i) => <div key={i} style={{ height: `${Math.max(4, Math.min(100, (v * 100) / Math.max(5, Math.max(...trend) * 1.15)))}%` }} />)}
        </div>
      )}
      {req !== undefined && req > 0 && (
        <>
          <div className="usebar">
            {use !== undefined && use >= 0 && <div className="use" style={{ width: `${Math.min(use, 100)}%`, background: use > 85 ? "var(--er)" : use > 70 ? "var(--wa)" : "var(--ac)" }} />}
            <div className="mark" title="requests" style={{ left: `${Math.min(req, 100)}%`, background: "var(--tx)" }} />
            <div className="mark" title="limits" style={{ left: `${Math.min(lim ?? 0, 100)}%`, background: over ? "var(--er)" : "var(--fa)" }} />
          </div>
          <div className="mono small" style={{ color: over ? "var(--er)" : "var(--fa)" }}>
            requests {req}% · limits {lim}%{over ? " (overcommitted)" : ""}
          </div>
        </>
      )}
    </div>
  );
}

const pct = (a: number, b: number) => (b > 0 ? Math.round((a * 100) / b) : 0);

function usageCard(title: string, u: Usage, fmt: (n: number) => string, unit: string): CardProps {
  const used = u.used >= 0 ? pct(u.used, u.alloc) : -1;
  return {
    title,
    val: used >= 0 ? `${used}%` : "—",
    sub: used >= 0 ? `${fmt(u.used)} of ${fmt(u.alloc)} ${unit}` : `${fmt(u.alloc)} ${unit} allocatable`,
    use: used,
    req: Math.max(1, pct(u.req, u.alloc)),
    lim: pct(u.lim, u.alloc),
    trend: u.trend,
  };
}

function Bar({ v }: { v: number }) {
  return (
    <div className="flex">
      <div className="minibar"><div style={{ width: `${Math.max(0, Math.min(v, 100))}%`, background: pctColor(v) }} /></div>
      <span className="mono small mu" style={{ width: 34 }}>{v >= 0 ? v + "%" : "—"}</span>
    </div>
  );
}

function pctOf(r: Row, m: [number, number] | undefined, i: 0 | 1): number {
  return m && r.x?.[i] ? pct(m[i], r.x[i]) : -1;
}

function NodeTable() {
  const t = useTable("Nodes");
  const rows = useMemo(() => [...t.rows.values()].sort((a, b) => a.m.localeCompare(b.m)), [t.version]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!rows.length) return <div className="dashed">{t.state === "ready" ? "No nodes are visible with your credentials." : "Loading nodes…"}</div>;
  return (
    <div className="nodes-t">
      <div className="hd"><span>Name</span><span>Status</span><span>Zone</span><span>CPU</span><span>Memory</span><span>Pods</span></div>
      {rows.map((r) => (
        <div key={r.u} className="rw" onClick={() => openDetail({ kind: "Nodes", ns: "", name: r.m })}>
          <span className="ell" style={{ fontWeight: 500 }}>{r.m}</span>
          <span className="ell" style={{ color: toneVar(r.k[1]) }}>{r.c[1]}</span>
          <span className="mono small mu ell">{r.c[4]}</span>
          <Bar v={pctOf(r, t.metrics.get(r.u), 0)} />
          <Bar v={pctOf(r, t.metrics.get(r.u), 1)} />
          <span className="mono small mu">{r.c[7]}</span>
        </div>
      ))}
    </div>
  );
}

const NodeCard = memo(function NodeCard({ r, pods, m }: { r: Row; pods: Row[]; m?: [number, number] }) {
  return (
    <div className="card" style={{ padding: "12px 14px" }}>
      <div className="flex" style={{ alignItems: "baseline", cursor: "pointer" }} onClick={() => openDetail({ kind: "Nodes", ns: "", name: r.m })}>
        <span style={{ fontWeight: 500 }} className="ell">{r.m}</span><span className="mono small fa">{r.c[4]}</span>
        <span style={{ flex: 1 }} /><span className="small" style={{ color: toneVar(r.k[1]) }}>{r.c[1]}</span>
      </div>
      <div style={{ display: "flex", flexWrap: "wrap", gap: 3 }}>
        {pods.map((p) => (
          <div key={p.u} className="sq" data-ns={p.n} data-name={p.m} title={`${p.n}/${p.m} · ${p.c[2]}`} style={{ background: toneVar(p.k[2], "var(--ok)") }} />
        ))}
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "36px 1fr", gap: "4px 8px", alignItems: "center" }} className="mono small mu">
        <span>cpu</span><Bar v={pctOf(r, m, 0)} />
        <span>mem</span><Bar v={pctOf(r, m, 1)} />
      </div>
    </div>
  );
});

function NodeMap() {
  const nodes = useTable("Nodes");
  const pods = useTable("Pods");
  const prev = useRef(new Map<string, Row[]>());
  const byNode = useMemo(() => {
    const m = new Map<string, Row[]>();
    for (const p of pods.rows.values()) {
      const n = p.c[7];
      if (!n || n === "—" || p.k[2] === "m") continue;
      let l = m.get(n);
      if (!l) m.set(n, (l = []));
      l.push(p);
    }
    // Keep the same array for a node whose pods did not change, so its
    // card does not render again.
    for (const [n, l] of m) {
      l.sort((a, b) => (a.m < b.m ? -1 : 1));
      const old = prev.current.get(n);
      if (old && old.length === l.length && old.every((x, i) => x === l[i])) m.set(n, old);
    }
    prev.current = m;
    return m;
  }, [pods.version]); // eslint-disable-line react-hooks/exhaustive-deps
  const rows = useMemo(() => [...nodes.rows.values()].sort((a, b) => a.m.localeCompare(b.m)), [nodes.version]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="nodemap" onClick={(e) => {
      const el = (e.target as HTMLElement).closest<HTMLElement>(".sq");
      if (el?.dataset.name) openDetail({ kind: "Pods", ns: el.dataset.ns ?? "", name: el.dataset.name });
    }}>
      {rows.map((r) => <NodeCard key={r.u} r={r} pods={byNode.get(r.m) ?? NO_PODS} m={nodes.metrics.get(r.u)} />)}
    </div>
  );
}

const NO_PODS: Row[] = [];

export function Overview() {
  const cl = useStore(ui, (s) => s.cluster);
  const ov = useStore(ui, (s) => s.overview);
  const style = useStore(ui, (s) => s.settings?.overviewStyle);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const counts = useStore(ui, (s) => s.counts);
  const kinds = useStore(ui, (s) => s.kinds);
  const allIssues = useStore(ui, (s) => s.issues);
  const issues = useMemo(() => allIssues.filter((i) => nsVisible(i.ns, nsSel)), [allIssues, nsSel]);

  const cards: CardProps[] = [];
  if (ov) {
    cards.push(usageCard("CPU", ov.cpu, cores, "cores"));
    cards.push(usageCard("Memory", ov.mem, gib, "GiB"));
    cards.push({ title: "Pods", val: String(ov.pods), sub: `of ${ov.podsCap} allocatable` });
    cards.push({ title: "Nodes", val: `${ov.ready} / ${ov.nodes}`, sub: "Ready" + (ov.cordoned ? ` · ${ov.cordoned} cordoned` : "") });
  }
  const health = [
    ["Pods", "Pods"], ["Deployments", "Deployments"], ["StatefulSets", "StatefulSets"], ["DaemonSets", "DaemonSets"],
    ["Jobs", "Jobs"], ["Certificates", "certificates.cert-manager.io"],
  ].filter(([, k]) => kinds[k]).map(([label, kind]) => {
    const [t, b] = sumCounts(counts, kind, nsSel);
    return { label, kind, t, b };
  });

  return (
    <div className="page">
      <div className="page-head">
        <h1>Overview</h1>
        <span className="meta-mono">{cl?.context} · {cl?.dist} {cl?.version} · live · 15s</span>
      </div>
      <div className="cards">{cards.map((c) => <Card key={c.title} {...c} />)}</div>
      <div className="health">
        {health.map((h) => (
          <div key={h.kind} onClick={() => goList(h.kind, h.b ? "Unhealthy" : "All")}>
            <span className="mu">{h.label}</span>
            <span className="v" style={{ color: h.b ? "var(--wa)" : "var(--tx)" }}>{h.t - h.b}/{h.t}</span>
            <span className="small" style={{ color: h.b ? "var(--wa)" : "var(--fa)" }}>{h.b ? `${h.b} need attention` : "all healthy"}</span>
          </div>
        ))}
      </div>
      <div className="col" style={{ gap: 10 }}>
        <div className="flex" style={{ gap: 10 }}>
          <h2 style={{ fontSize: 13, fontWeight: 600 }}>Needs attention</h2>
          <span className="meta-mono">{issues.length}</span>
          <span style={{ flex: 1 }} />
          {issues.length > 0 && <button className="btn sm" style={{ color: "var(--ac)" }} onClick={() => openAI(null)}>Explain with Assistant</button>}
        </div>
        {!issues.length && <div className="dashed">Nothing needs attention. All workloads are healthy.</div>}
        {issues.length > 0 && (
          <div className="box">
            {issues.map((i) => (
              <div key={i.id} className="issue-row">
                <span className="dot lg" style={{ background: i.sev === "er" ? "var(--er)" : "var(--wa)" }} />
                <div className="col grow" style={{ gap: 2 }}>
                  <span style={{ fontWeight: 500 }} className="ell">{i.title}</span>
                  <span className="meta-mono ell">{i.ns ? i.ns + " · " : ""}{i.reason}{i.meta ? " · " + i.meta : ""}</span>
                </div>
                <button className="btn sm" onClick={() => openDetail({ kind: i.kind, ns: i.ns, name: i.obj })}>Open</button>
                <button className="btn sm accent" onClick={() => openAI(i.id, true)}>Explain</button>
              </div>
            ))}
          </div>
        )}
      </div>
      <div className="col" style={{ gap: 10 }}>
        <div className="flex">
          <h2 style={{ fontSize: 13, fontWeight: 600 }}>Nodes</h2>
          <span style={{ flex: 1 }} />
          <div className="seg">
            <button className={style !== "nodemap" ? "on" : ""} onClick={() => void saveSettings({ overviewStyle: "metrics" })}>Table</button>
            <button className={style === "nodemap" ? "on" : ""} onClick={() => void saveSettings({ overviewStyle: "nodemap" })}>Node map</button>
          </div>
        </div>
        {style === "nodemap" ? <NodeMap /> : <NodeTable />}
      </div>
    </div>
  );
}

