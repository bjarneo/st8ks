import { useCallback, useMemo, useState } from "react";
import { kindByKind, nsVisible, openAI, openDetail, ui } from "../lib/app";
import { compileQuery } from "../lib/filter";
import { shallowEqual, useStore } from "../lib/store";
import { useTable } from "../lib/tables";
import type { Col, Row } from "../lib/types";
import { VTable } from "./VTable";

const COLS: Col[] = [
  { l: "Type", t: "status", w: "90px", i: 0 },
  { l: "Reason", t: "text", w: "150px", i: 1 },
  { l: "Namespace", t: "ns", w: "minmax(90px,.6fr)" },
  { l: "Object", t: "mono", w: "minmax(180px,1.2fr)", i: 2 },
  { l: "Message", t: "text", w: "minmax(300px,3fr)", i: 3 },
  { l: "Count", t: "num", w: "54px", i: 4 },
  { l: "Last", t: "time", w: "64px", i: 5 },
];

export function EventsView() {
  const t = useTable("Events");
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const all = useStore(ui, (s) => s.issues);
  const [warn, setWarn] = useState(false);
  const [q, setQ] = useState("");
  const issues = useMemo(() => all.filter((i) => nsVisible(i.ns, nsSel)), [all, nsSel]);
  const rows = useMemo(() => {
    const set = new Set(nsSel);
    const pred = compileQuery(q, COLS);
    const out: Row[] = [];
    for (const r of t.rows.values()) {
      if (set.size && !set.has(r.n ?? "")) continue;
      if (warn && !r.b) continue;
      if (pred && !pred(r)) continue;
      out.push(r);
    }
    out.sort((a, b) => (b.x?.[0] ?? 0) - (a.x?.[0] ?? 0));
    return out;
  }, [t.version, nsSel, warn, q]); // eslint-disable-line react-hooks/exhaustive-deps
  const onOpen = useCallback((r: Row) => {
    const [k, ...rest] = (r.c[2] ?? "").split("/");
    const kind = kindByKind(k);
    if (kind) openDetail({ kind, ns: kind === "Nodes" || kind === "Namespaces" ? "" : r.n ?? "", name: rest.join("/") });
  }, []);
  return (
    <div className="col grow">
      <div className="view-head">
        <h1>Events &amp; troubleshooting</h1>
        <span className="meta-mono">events · {rows.length}</span>
        {t.state !== "ready" && <span className="spinner" />}
        <span style={{ flex: 1 }} />
        <button className="btn" style={{ background: warn ? "var(--waw)" : undefined }} onClick={() => setWarn(!warn)}>Warnings only{warn ? " ✓" : ""}</button>
        <input className="input" style={{ width: 280 }} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter reason, object, message" />
      </div>
      {issues.length > 0 && (
        <div style={{ padding: "0 18px 14px", display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(240px, 1fr))", gap: 10 }}>
          {issues.map((g) => (
            <div key={g.id} className="card" style={{ padding: "12px 14px", gap: 6 }}>
              <div className="flex"><span className="dot" style={{ background: g.sev === "er" ? "var(--er)" : "var(--wa)" }} /><span style={{ fontWeight: 500 }} className="ell">{g.title}</span></div>
              <span className="meta-mono ell">{g.ns ? g.ns + "/" : ""}{g.obj}</span>
              <span className="mu" style={{ fontSize: 12 }}>{g.reason}{g.meta ? " · " + g.meta : ""}</span>
              <div className="flex" style={{ gap: 6, marginTop: 4 }}>
                <button className="btn xs" onClick={() => openDetail({ kind: g.kind, ns: g.ns, name: g.obj })}>Open</button>
                <button className="btn xs accent" onClick={() => openAI(g.id, true)}>Explain</button>
              </div>
            </div>
          ))}
        </div>
      )}
      <VTable rows={rows} cols={COLS} onOpen={onOpen} minWidth={960}
        header={COLS.map((c) => <span key={c.l}>{c.l}</span>)}
        empty={t.state === "ready" ? <div className="empty">No events match. The API server keeps events for one hour by default.</div> : null} />
    </div>
  );
}
