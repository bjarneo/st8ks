import { useEffect, useMemo, useState } from "react";
import { call } from "../lib/bridge";
import { fail, openDetail, ui } from "../lib/app";
import { toneVar } from "../lib/format";
import { useStore } from "../lib/store";
import { ensureLoaded, useTable } from "../lib/tables";
import type { Topology } from "../lib/types";
import { useScale } from "./VTable";

const COLW = 230, X0 = 24, W = 190, H = 40;

export function TopologyView() {
  const nsSel = useStore(ui, (s) => s.nsSel);
  const nsT = useTable("Namespaces");
  const [ns, setNs] = useState(nsSel.length === 1 ? nsSel[0] : "");
  const [topo, setTopo] = useState<Topology | null>(null);
  const [sel, setSel] = useState<string | null>(null);
  const namespaces = useMemo(() => [...nsT.rows.values()].map((r) => r.m).sort(), [nsT.version]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => void ensureLoaded("Namespaces"), []);
  const counts = useStore(ui, (s) => s.counts);
  useEffect(() => {
    if (ns || !namespaces.length) return;
    // Start with the busiest application namespace, preferring one with ingresses.
    const score = (n: string) => (counts.Ingresses?.[n]?.[0] ?? 0) * 1000 + (counts.Pods?.[n]?.[0] ?? 0) - (/^kube-|-system$/.test(n) ? 1e6 : 0);
    setNs([...namespaces].sort((a, b) => score(b) - score(a))[0]);
  }, [namespaces, ns, counts]);
  useEffect(() => {
    if (nsSel.length === 1) setNs(nsSel[0]);
  }, [nsSel]);
  useEffect(() => {
    if (!ns) return;
    let live = true;
    const load = () => call<Topology>("Topology", ns).then((t) => live && setTopo(t), fail);
    void load();
    setSel(null);
    const iv = setInterval(() => void load(), 5000);
    return () => {
      live = false;
      clearInterval(iv);
    };
  }, [ns]);

  const lit = useMemo(() => {
    if (!sel || !topo) return null;
    const col = new Map(topo.nodes.map((n) => [n.id, n.col]));
    const adj = new Map<string, string[]>();
    for (const e of topo.edges) {
      adj.set(e.a, [...(adj.get(e.a) ?? []), e.b]);
      adj.set(e.b, [...(adj.get(e.b) ?? []), e.a]);
    }
    // Walk left and right separately, so a trace follows one chain.
    const out = new Set([sel]);
    for (const dir of [-1, 1]) {
      const q = [sel];
      while (q.length) {
        const id = q.pop()!;
        for (const n of adj.get(id) ?? []) {
          if (out.has(n) || Math.sign((col.get(n) ?? 0) - (col.get(id) ?? 0)) !== dir) continue;
          out.add(n);
          q.push(n);
        }
      }
    }
    return out;
  }, [sel, topo]);

  const pos = useMemo(() => new Map((topo?.nodes ?? []).map((n) => [n.id, n])), [topo]);
  // The layout grows with the text size. It matches the .tnode size in the CSS.
  const k = useScale();
  const cw = COLW * k, x0 = X0 * k, w = W * k, h = H * k;
  const width = x0 + (topo?.cols.length ?? 6) * cw;
  const height = ((topo?.height ?? 400) + 40) * k;

  return (
    <div className="col grow">
      <div className="view-head">
        <h1>Topology</h1>
        <select className="input mono" style={{ height: 26 }} value={ns} onChange={(e) => setNs(e.target.value)}>
          {[...new Set([ns, ...namespaces])].filter(Boolean).map((n) => <option key={n} value={n}>{n}</option>)}
        </select>
        {topo?.truncated && <span className="chip" style={{ color: "var(--wa)" }}>showing the first 300 pods</span>}
        <span style={{ flex: 1 }} />
        <span className="flex small mu" style={{ gap: 14 }}><span>— owns</span><span>- - routes / selects</span><span>··· mounts</span><span className="fa">click to trace · double-click to open</span></span>
      </div>
      <div className="topo-wrap" onClick={() => setSel(null)}>
        {topo && !topo.nodes.length && <div className="empty">No workloads in namespace {ns}.</div>}
        {topo && topo.nodes.length > 0 && (
          <div style={{ position: "relative", width, height, margin: 10 }}>
            <svg width={width} height={height} style={{ position: "absolute", left: 0, top: 0, pointerEvents: "none" }}>
              {topo.edges.map((e, i) => {
                const a = pos.get(e.a), b = pos.get(e.b);
                if (!a || !b) return null;
                const [l, r] = a.col <= b.col ? [a, b] : [b, a];
                const x1 = x0 + l.col * cw + w, y1 = l.y * k + h / 2, x2 = x0 + r.col * cw, y2 = r.y * k + h / 2;
                const hot = lit ? lit.has(e.a) && lit.has(e.b) : false;
                const d = `M${x1} ${y1} C${x1 + 50} ${y1} ${x2 - 50} ${y2} ${x2} ${y2}`;
                return <path key={i} d={d} fill="none" stroke={hot ? "var(--ac)" : "var(--fa)"} strokeOpacity={lit && !hot ? 0.15 : 0.6}
                  strokeWidth={hot ? 1.6 : 1} strokeDasharray={e.t === "r" ? "4 3" : e.t === "u" ? "1 3" : undefined} />;
              })}
            </svg>
            {topo.cols.map((c, i) => <div key={c} className="small fa" style={{ position: "absolute", top: 36 * k, left: x0 + i * cw, fontWeight: 500 }}>{c}</div>)}
            {topo.nodes.map((n) => {
              const on = !lit || lit.has(n.id);
              return (
                <div key={n.id} className="tnode" title={`${n.type} ${n.name}\nClick to trace · double-click to open`}
                  style={{ left: x0 + n.col * cw, top: n.y * k, borderColor: sel === n.id ? "var(--ac)" : undefined, borderLeftColor: toneVar(n.tone, "var(--ln)"), opacity: on ? 1 : 0.35 }}
                  onClick={(e) => {
                    e.stopPropagation();
                    setSel(sel === n.id ? null : n.id);
                  }}
                  onDoubleClick={(e) => {
                    e.stopPropagation();
                    openDetail({ kind: n.kind, ns: n.ns, name: n.name });
                  }}>
                  <span className="ell" style={{ fontSize: "calc(11.5px * var(--zf))", fontWeight: 500 }}>{n.name}</span>
                  <span className="mono fa ell" style={{ fontSize: "calc(10px * var(--zf))" }}>{n.type}{n.sub ? " · " + n.sub : ""}</span>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
