import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { call, copyText } from "../lib/bridge";
import {
  confirmDelete, confirmRestart, kindSingular, openDetail, openDock, saveSettings, say, setNs, ui,
} from "../lib/app";
import { compileQuery, defaultSort, sortRows } from "../lib/filter";
import { shallowEqual, useStore } from "../lib/store";
import { useTable } from "../lib/tables";
import type { Row } from "../lib/types";
import { VTable } from "./VTable";
import { isTyping } from "./App";

let focusFilter: (() => void) | null = null;
window.addEventListener("keydown", (e) => {
  if (e.key === "/" && !isTyping(e) && !e.metaKey && !e.ctrlKey && !ui.get().pal && focusFilter) {
    e.preventDefault();
    focusFilter();
  }
});

const BUILTIN = ["All", "Unhealthy"];

export function ListView() {
  const kind = useStore(ui, (s) => s.kind);
  const info = useStore(ui, (s) => s.kinds[s.kind]);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const filter = useStore(ui, (s) => s.filter);
  const savedView = useStore(ui, (s) => s.savedView);
  const sort = useStore(ui, (s) => s.sort);
  const checked = useStore(ui, (s) => s.checked);
  const detail = useStore(ui, (s) => s.detail);
  const saved = useStore(ui, (s) => s.settings?.savedViews);
  const t = useTable(kind);
  const inputRef = useRef<HTMLInputElement>(null);
  const [cursor, setCursor] = useState(-1);

  useEffect(() => {
    focusFilter = () => inputRef.current?.focus();
    return () => {
      focusFilter = null;
    };
  }, []);
  useEffect(() => setCursor(-1), [kind]);

  const cols = info?.cols ?? [];
  const custom = useMemo(() => (saved ?? []).filter((v) => v.kind === kind).map((v) => v.query), [saved, kind]);
  const views = useMemo(() => [...BUILTIN, ...(kind === "Pods" ? ["Restarts > 0"] : []), ...custom], [kind, custom]);
  const sv = views.includes(savedView) ? savedView : "All";

  const nsRows = useMemo(() => {
    let rows = [...t.rows.values()];
    if (info?.namespaced && nsSel.length) {
      const set = new Set(nsSel);
      rows = rows.filter((r) => set.has(r.n ?? ""));
    }
    return rows;
  }, [t.version, info, nsSel]); // eslint-disable-line react-hooks/exhaustive-deps

  const rows = useMemo(() => {
    let rows = nsRows;
    if (sv === "Unhealthy") rows = rows.filter((r) => r.b);
    else if (sv === "Restarts > 0") rows = rows.filter((r) => (r.x?.[2] ?? 0) > 0);
    const q = (custom.includes(sv) ? sv + " " : "") + filter;
    const pred = compileQuery(q, cols);
    if (pred) rows = rows.filter(pred);
    else if (rows === nsRows) rows = rows.slice();
    return sort ? sortRows(rows, cols, sort.i, sort.dir, t.metrics) : defaultSort(rows);
  }, [nsRows, sv, custom, filter, sort, cols]); // eslint-disable-line react-hooks/exhaustive-deps

  const key = (r: Row) => `${kind}/${r.n ?? ""}/${r.m}`;
  const checkedRows = useMemo(() => rows.filter((r) => checked[key(r)]), [rows, checked]); // eslint-disable-line react-hooks/exhaustive-deps
  const allChk = rows.length > 0 && checkedRows.length === rows.length;

  const onOpen = useCallback((r: Row) => openDetail({ kind, ns: r.n ?? "", name: r.m }), [kind]);
  const onCheck = useCallback((r: Row) => {
    const k = `${kind}/${r.n ?? ""}/${r.m}`;
    ui.set((s) => {
      const c = { ...s.checked };
      if (c[k]) delete c[k];
      else c[k] = true;
      return { checked: c };
    });
  }, [kind]);
  const isChecked = useCallback((r: Row) => !!checked[`${kind}/${r.n ?? ""}/${r.m}`], [checked, kind]);
  const isActive = useCallback((r: Row) => !!detail && detail.kind === kind && detail.name === r.m && (detail.ns ?? "") === (r.n ?? ""), [detail, kind]);

  // Keyboard navigation: j and k move, Enter opens, x selects.
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) return;
      const s = ui.get();
      if (s.pal || s.cf || s.settingsOpen) return;
      const n = rowsRef.current.length;
      if (!n) return;
      if (e.key === "j" || e.key === "ArrowDown") {
        e.preventDefault();
        setCursor((c) => Math.min(n - 1, c + 1));
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault();
        setCursor((c) => Math.max(0, c - 1));
      } else if (e.key === "Enter") {
        setCursor((c) => {
          const r = rowsRef.current[c];
          if (r) onOpen(r);
          return c;
        });
      } else if (e.key === "x") {
        setCursor((c) => {
          const r = rowsRef.current[c];
          if (r) onCheck(r);
          return c;
        });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onOpen, onCheck]);

  const refs = checkedRows.map((r) => ({ kind, ns: r.n ?? "", name: r.m }));
  const isWork = ["Deployments", "StatefulSets", "DaemonSets"].includes(kind);
  const q = filter.trim();

  const header = (
    <>
      <div className={"chk" + (allChk ? " on" : "")} style={{ marginLeft: 6 }} onClick={() => {
        const c = { ...checked };
        for (const r of rows) {
          if (allChk) delete c[key(r)];
          else c[key(r)] = true;
        }
        ui.set({ checked: c });
      }}>{allChk ? "✓" : ""}</div>
      {cols.map((c, i) => (
        <span key={i} onClick={() => ui.set((s) => ({ sort: s.sort?.i === i ? (s.sort.dir > 0 ? { i, dir: -1 } : null) : { i, dir: 1 } }))}>
          {c.l} {sort?.i === i ? (sort.dir > 0 ? "↑" : "↓") : ""}
        </span>
      ))}
    </>
  );

  return (
    <div className="col grow" style={{ position: "relative" }}>
      <div className="view-head" style={{ paddingBottom: 8 }}>
        <div className="flex" style={{ alignItems: "baseline", gap: 10 }}>
          <h1>{info?.label ?? kind}</h1>
          <span className="meta-mono">{info?.api}</span>
          <span className="chip">{rows.length} / {t.rows.size}</span>
          {info && !info.namespaced && <span className="small fa">cluster-scoped</span>}
          {t.state !== "ready" || !t.synced ? <span className="spinner" title="Loading" /> : null}
        </div>
        <div style={{ flex: 1 }} />
        <div className="filterbox">
          <input ref={inputRef} value={filter} placeholder="Filter · ns:prod status:crash app=web" spellCheck={false}
            onChange={(e) => {
              ui.set({ filter: e.target.value });
              setCursor(-1);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                if (filter) ui.set({ filter: "" });
                else (e.target as HTMLInputElement).blur();
                e.stopPropagation();
              }
              if (e.key === "Enter" || e.key === "ArrowDown") {
                (e.target as HTMLInputElement).blur();
                setCursor(0);
              }
            }} />
          <span className="kbd">/</span>
        </div>
        {kind !== "HelmRepositories" && (
          <button className="btn primary" onClick={async () => {
            const ns = nsSel[0] || "default";
            const yaml = await call<string>("NewYAML", kind, ns);
            openDetail({ kind, ns, name: "", create: true }, { dtab: "yaml", ymode: "edit", draft: yaml });
          }}>Create</button>
        )}
      </div>
      <div className="pills">
        {views.map((v) => (
          <button key={v} className={"pill" + (v === sv ? " on" : "")} onClick={() => ui.set({ savedView: v })}>
            {v}
            {custom.includes(v) && (
              <span className="x" title="Remove view" onClick={(e) => {
                e.stopPropagation();
                const all = ui.get().settings?.savedViews ?? [];
                void saveSettings({ savedViews: all.filter((x) => !(x.kind === kind && x.query === v)) });
                if (sv === v) ui.set({ savedView: "All" });
              }}>✕</span>
            )}
          </button>
        ))}
        {q && !custom.includes(q) && (
          <button className="pill add" onClick={() => {
            const all = ui.get().settings?.savedViews ?? [];
            void saveSettings({ savedViews: [...all, { kind, query: q }] });
            ui.set({ savedView: q, filter: "" });
            say(`Saved view “${q}”`);
          }}>+ Save “{q}” as view</button>
        )}
      </div>
      {t.err && <div className="warnbox" style={{ margin: "0 18px 10px" }}>{t.err}</div>}
      <VTable rows={rows} cols={cols} metrics={t.metrics} header={header} cursor={cursor}
        isChecked={isChecked} isActive={isActive} onOpen={onOpen} onCheck={kind === "HelmRepositories" ? undefined : onCheck}
        empty={t.state === "ready" && t.synced ? (
          <div className="flex mu" style={{ padding: "40px 18px", gap: 10 }}>
            No {info?.label ?? kind} match these filters.
            {(filter || sv !== "All" || nsSel.length > 0) && (
              <button className="btn xs" style={{ color: "var(--ac)" }} onClick={() => {
                ui.set({ filter: "", savedView: "All" });
                setNs([]);
              }}>Clear filters</button>
            )}
          </div>
        ) : null} />
      {checkedRows.length > 0 && (
        <div className="bulkbar">
          <span style={{ fontWeight: 500, marginRight: 8 }}>{checkedRows.length} selected</span>
          {kind === "Pods" && (
            <button className="btn" onClick={() => {
              checkedRows.slice(0, 6).forEach((r) => openDock("logs", r.n ?? "", r.m));
              if (checkedRows.length > 6) say("Opened logs for the first 6 pods");
            }}>Logs</button>
          )}
          {isWork && <button className="btn" onClick={() => confirmRestart(kind, refs)}>Restart</button>}
          <button className="btn" onClick={() => {
            void copyText(checkedRows.map((r) => r.m).join("\n"));
            say(`Copied ${checkedRows.length} names`);
          }}>Copy names</button>
          <button className="btn danger-text" onClick={() => confirmDelete(kind, refs)}>Delete…</button>
          <div className="vsep" />
          <button className="btn icon sm fa" title={`Clear selection of ${kindSingular(kind)}`} onClick={() => ui.set({ checked: {} })}>✕</button>
        </div>
      )}
    </div>
  );
}
