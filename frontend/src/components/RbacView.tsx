import { useEffect, useMemo, useState } from "react";
import { call, copyText } from "../lib/bridge";
import { fail, say, ui } from "../lib/app";
import { useStore } from "../lib/store";
import { ensureLoaded, useTable } from "../lib/tables";
import type { RbacAnswer, RbacMatrix, Subject } from "../lib/types";

const sid = (s: Subject) => `${s.kind}:${s.ns ? s.ns + "/" : ""}${s.name}`;

export function RbacView() {
  const nsSel = useStore(ui, (s) => s.nsSel);
  const nsTable = useTable("Namespaces");
  const [subjects, setSubjects] = useState<Subject[]>([]);
  const [filter, setFilter] = useState("");
  const [cur, setCur] = useState<Subject | null>(null);
  const [ns, setNs] = useState(nsSel[0] ?? "default");
  const [m, setM] = useState<RbacMatrix | null>(null);
  const [cell, setCell] = useState<[string, string] | null>(null);
  const [ans, setAns] = useState<RbacAnswer | null>(null);
  const [showSystem, setShowSystem] = useState(false);

  const load = () => call<Subject[]>("RbacSubjects").then((s) => {
    setSubjects(s);
    setCur((c) => c ?? s.find((x) => !x.name.startsWith("system:")) ?? s[0] ?? null);
  }, fail);
  useEffect(() => {
    void ensureLoaded("Namespaces");
    void load();
  }, []);
  useEffect(() => {
    if (!cur) return;
    let live = true;
    setCell(null);
    setAns(null);
    setM(null);
    call<RbacMatrix>("RbacMatrix", cur, ns).then((x) => live && setM(x), fail);
    return () => {
      live = false;
    };
  }, [cur, ns]);
  useEffect(() => {
    if (!cur || !cell) return;
    let live = true;
    setAns(null);
    call<RbacAnswer>("RbacExplain", cur, ns, cell[0], cell[1]).then((x) => live && setAns(x), fail);
    return () => {
      live = false;
    };
  }, [cell]); // eslint-disable-line react-hooks/exhaustive-deps

  const namespaces = useMemo(() => [...nsTable.rows.values()].map((r) => r.m).sort(), [nsTable.version]); // eslint-disable-line react-hooks/exhaustive-deps
  const shown = useMemo(() => {
    const f = filter.toLowerCase();
    return subjects.filter((s) => (showSystem || !(s.name.startsWith("system:") || (s.ns ?? "").startsWith("kube-"))) && (!f || sid(s).toLowerCase().includes(f)));
  }, [subjects, filter, showSystem]);
  const quick = [...new Set([...nsSel, ...(cur?.ns ? [cur.ns] : []), "default", ns])].slice(0, 4);

  return (
    <div className="grow" style={{ display: "flex" }}>
      <div className="subjects">
        <div className="flex" style={{ padding: "0 8px 8px" }}><span className="sec-label grow">Subjects</span><button className="btn xs ghost fa" title="Reload" onClick={() => void load()}>↻</button></div>
        <input className="input" style={{ margin: "0 4px 8px", height: 26 }} placeholder="Filter subjects" value={filter} onChange={(e) => setFilter(e.target.value)} />
        {shown.map((s) => (
          <div key={sid(s)} className={"subj" + (cur && sid(cur) === sid(s) ? " on" : "")} onClick={() => setCur(s)}>
            <span className="ell" style={{ fontWeight: 500 }}>{s.ns ? `${s.ns}/${s.name}` : s.name}</span>
            <span className="small fa">{s.kind} · {s.bindings} binding{s.bindings === 1 ? "" : "s"}</span>
          </div>
        ))}
        <label className="flex small fa" style={{ padding: "10px 8px", cursor: "pointer" }}>
          <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} />Show system subjects
        </label>
      </div>
      <div className="grow stack" style={{ overflow: "auto", padding: "16px 20px", display: "flex", flexDirection: "column", gap: 14 }}>
        <div className="flex" style={{ alignItems: "baseline", gap: 10, flexWrap: "wrap" }}>
          <h1 style={{ fontSize: "calc(16px * var(--zf))", fontWeight: 600 }}>RBAC Explorer</h1>
          {cur && <span className="meta-mono">{cur.kind} · {cur.ns ? cur.ns + "/" : ""}{cur.name}</span>}
          <span style={{ flex: 1 }} />
          <div className="seg mono">{quick.map((n) => <button key={n} className={n === ns ? "on" : ""} onClick={() => setNs(n)}>{n}</button>)}</div>
          <select className="input mono" style={{ height: 26 }} value={ns} onChange={(e) => setNs(e.target.value)}>
            {[...new Set([ns, ...namespaces])].map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </div>
        <p className="mu" style={{ margin: 0, maxWidth: 680, lineHeight: 1.5 }}>
          Effective permissions after st8ks resolves every Role, ClusterRole and binding. Click a cell to see which rule grants or denies it. Group membership of users comes from your identity provider, so user rows show only direct and implicit groups.
        </p>
        {m && (
          <div className="matrix">
            <div className="mr hd"><span>Resource</span>{m.verbs.map((v) => <span key={v} className="mono" style={{ textAlign: "center" }}>{v}</span>)}</div>
            {m.resources.map((r, ri) => (
              <div key={r.name} className="mr bd">
                <span className="flex mono" style={{ fontSize: "calc(12px * var(--zf))" }}>{r.name}{r.cluster && <span className="fa" style={{ fontFamily: "var(--sans)", fontSize: "calc(10px * var(--zf))" }}>cluster</span>}</span>
                {m.cells[ri].map((c, vi) => {
                  const v = m.verbs[vi];
                  const sel = cell && cell[0] === r.name && cell[1] === v;
                  if (c.na) return <div key={v} className="mc fa" style={{ cursor: "default" }}>·</div>;
                  return (
                    <div key={v} className="mc" onClick={() => setCell([r.name, v])}
                      style={{ background: c.a ? "var(--okw)" : c.p ? "var(--waw)" : "transparent", color: c.a ? "var(--ok)" : c.p ? "var(--wa)" : "var(--fa)", boxShadow: `inset 0 0 0 1.5px ${sel ? "var(--ac)" : "transparent"}` }}>
                      {c.a ? "✓" : c.p ? "◐" : "✕"}
                    </div>
                  );
                })}
              </div>
            ))}
          </div>
        )}
        {cell && (
          <div className="card" style={{ maxWidth: 780, gap: 6 }}>
            <div className="flex" style={{ alignItems: "baseline" }}>
              <span style={{ fontWeight: 500 }}>Can {cur?.name} {cell[1]} {cell[0]}{m?.resources.find((r) => r.name === cell[0])?.cluster ? " (cluster-wide)" : ` in ${ns}`}?</span>
              {ans && <span style={{ fontWeight: 600, color: ans.allowed ? "var(--ok)" : ans.partial ? "var(--wa)" : "var(--er)" }}>{ans.allowed ? "Yes" : ans.partial ? "Only named objects" : "No"}</span>}
            </div>
            {!ans && <span className="flex fa"><span className="spinner" />Checking…</span>}
            {ans && (
              <>
                <span className="mu" style={{ lineHeight: 1.5 }}>{ans.why}</span>
                <span className="small fa">{ans.verified}</span>
                <div className="flex">
                  <code className="mono small fa grow ell">{ans.cmd}</code>
                  <button className="btn xs" onClick={() => {
                    void copyText(ans.cmd);
                    say("Command copied");
                  }}>Copy</button>
                </div>
              </>
            )}
          </div>
        )}
        {m && m.bindings.length > 0 && (
          <div className="col" style={{ gap: 6 }}>
            <span className="sec-label">Bindings that apply</span>
            {m.bindings.map((b) => <span key={b} className="mono small mu">{b}</span>)}
          </div>
        )}
      </div>
    </div>
  );
}
