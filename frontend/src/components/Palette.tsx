import { useEffect, useMemo, useRef, useState } from "react";
import { call } from "../lib/bridge";
import {
  changeTextSize, connect, fail, kindLabel, nav, openAI, openDetail, openDock, saveSettings, say, setMode, toggleDensity, toggleTheme, ui,
} from "../lib/app";
import { useStore } from "../lib/store";
import { ensureLoaded, table, useTables } from "../lib/tables";

interface PItem { label: string; sub: string; grp: string; run: () => void; rank?: number }

const RES_KINDS = ["Pods", "Deployments", "StatefulSets", "DaemonSets", "Services", "Ingresses", "ConfigMaps", "Secrets", "Jobs", "CronJobs", "Nodes", "Namespaces", "PersistentVolumeClaims"];

function score(label: string, q: string): number {
  const l = label.toLowerCase();
  const i = l.indexOf(q);
  if (i === 0) return 0;
  if (i > 0) return 1 + (l[i - 1] === "-" || l[i - 1] === " " || l[i - 1] === "/" ? 0 : 5) + i / 100;
  let j = 0, gaps = 0, last = -1;
  for (let k = 0; k < l.length && j < q.length; k++) {
    if (l[k] === q[j]) {
      if (last >= 0 && k - last > 1) gaps++;
      last = k;
      j++;
    }
  }
  return j === q.length ? 20 + gaps : -1;
}

export function Palette() {
  const [q, setQ] = useState("");
  const [idx, setIdx] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const tree = useStore(ui, (s) => s.tree);
  const contexts = useStore(ui, (s) => s.contexts);
  const cur = useStore(ui, (s) => s.cluster?.context);
  const issues = useStore(ui, (s) => s.issues);
  const kinds = useStore(ui, (s) => s.kinds);
  const settings = useStore(ui, (s) => s.settings);
  const fwCount = useStore(ui, (s) => s.forwards.length);
  const resKinds = useMemo(() => RES_KINDS.filter((k) => kinds[k]), [kinds]);
  const liveVer = useTables(resKinds);
  // Rebuild the item list at most once per second while tables change.
  const [ver, setVer] = useState(liveVer);
  const lastBuild = useRef(0);
  useEffect(() => {
    if (liveVer === ver) return;
    const wait = Math.max(0, 1000 - (Date.now() - lastBuild.current));
    const h = setTimeout(() => {
      lastBuild.current = Date.now();
      setVer(liveVer);
    }, wait);
    return () => clearTimeout(h);
  }, [liveVer, ver]);

  useEffect(() => {
    input.current?.focus();
    resKinds.forEach((k) => void ensureLoaded(k, 0));
  }, [resKinds]);

  const items = useMemo(() => {
    const out: PItem[] = [];
    const act = (label: string, sub: string, run: () => void) => out.push({ label, sub, grp: "Action", run });
    act("Open the IDE", "Edit manifests in a Git repository", () => setMode("ide"));
    act("Explain cluster issues", "Assistant", () => openAI(null));
    for (const i of issues) act(`Explain: ${i.title}`, i.reason, () => openAI(i.id, true));
    act("Toggle light / dark theme", "Appearance", toggleTheme);
    act("Toggle compact density", "Appearance", toggleDensity);
    act("Text size: larger", `Appearance · now ${settings?.textSize ?? 100} % · Ctrl +`, () => changeTextSize(1));
    act("Text size: smaller", `Appearance · now ${settings?.textSize ?? 100} % · Ctrl −`, () => changeTextSize(-1));
    act("Text size: default", "Appearance · 100 % · Ctrl 0", () => changeTextSize(0));
    for (const l of ["drawer", "split", "page"] as const) act(`Detail layout: ${l}`, settings?.detailLayout === l ? "current" : "Layout", () => void saveSettings({ detailLayout: l }));
    act("Overview: node map", "Layout", () => void saveSettings({ overviewStyle: "nodemap" }));
    act("Overview: metrics table", "Layout", () => void saveSettings({ overviewStyle: "metrics" }));
    act("Settings", "Assistant key, safety, logs", () => ui.set({ settingsOpen: true }));
    act("Add kubeconfig file…", "Clusters", () => call<string>("ImportKubeconfig").then((m) => m && say(m), fail));
    act("Add kubeconfig folder…", "Clusters", () => call<string>("ImportKubeconfigFolder").then((m) => m && say(m), fail));
    act("Reload kubeconfig files", "Clusters", () => call("ReloadKubeconfigs").then(() => say("Kubeconfig files reloaded"), fail));
    if (fwCount) act("Stop all port-forwards", `${fwCount} running`, () => call("StopAllForwards").catch(fail));
    for (const c of contexts) {
      if (c.name !== cur) out.push({ label: `Switch context: ${c.name}`, sub: [c.dist, c.version].filter(Boolean).join(" ") || c.server, grp: "Cluster", run: () => void connect(c.name) });
    }
    for (const sec of tree) {
      for (const it of sec.items) out.push({ label: it.label, sub: sec.label, grp: "Go to", run: () => nav(it) });
    }
    for (const k of resKinds) {
      const t = table(k);
      const sing = kindLabel(k).replace(/s$/, "");
      for (const r of t.rows.values()) {
        const ns = r.n ?? "";
        if (k === "Pods") {
          out.push({ label: r.m, sub: `Pod · ${ns} · ${r.c[2] ?? ""}`, grp: "Pod", run: () => openDetail({ kind: k, ns, name: r.m }), rank: r.b ? -1 : 0 });
          out.push({ label: `Logs: ${r.m}`, sub: ns, grp: "Logs", run: () => openDock("logs", ns, r.m) });
          out.push({ label: `Shell: ${r.m}`, sub: ns, grp: "Exec", run: () => openDock("term", ns, r.m) });
        } else {
          out.push({ label: r.m, sub: `${sing}${ns ? " · " + ns : ""}`, grp: sing.length > 10 ? sing.slice(0, 10) : sing, run: () => {
            ui.set({ view: "list", kind: k });
            openDetail({ kind: k, ns, name: r.m });
          } });
        }
      }
    }
    return out;
  }, [ver, tree, contexts, cur, issues, settings, fwCount, resKinds]); // eslint-disable-line react-hooks/exhaustive-deps

  const res = useMemo(() => {
    const lq = q.trim().toLowerCase();
    if (!lq) {
      const acts = items.filter((i) => i.grp === "Action").slice(0, 7);
      const bad = items.filter((i) => i.grp === "Pod" && i.rank === -1).slice(0, 5);
      return [...acts, ...bad];
    }
    const scored: [number, PItem][] = [];
    for (const it of items) {
      const s = score(it.label, lq);
      if (s >= 0) scored.push([s + (it.grp === "Logs" || it.grp === "Exec" ? 0.5 : 0), it]);
    }
    scored.sort((a, b) => a[0] - b[0] || a[1].label.length - b[1].label.length);
    return scored.slice(0, 60).map((x) => x[1]);
  }, [items, q]);

  const sel = Math.min(idx, Math.max(0, res.length - 1));
  useEffect(() => {
    list.current?.querySelector(".pal-item.on")?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const run = (it: PItem | undefined) => {
    if (!it) return;
    ui.set({ pal: false });
    it.run();
  };
  return (
    <>
      <div className="overlay" onClick={() => ui.set({ pal: false })} />
      <div className="palette">
        <input ref={input} value={q} placeholder="Go to a resource, open logs, switch context…" spellCheck={false}
          onChange={(e) => {
            setQ(e.target.value);
            setIdx(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") ui.set({ pal: false });
            else if (e.key === "ArrowDown") {
              e.preventDefault();
              setIdx(Math.min(sel + 1, res.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setIdx(Math.max(0, sel - 1));
            } else if (e.key === "Enter") {
              e.preventDefault();
              run(res[sel]);
            }
          }} />
        <div style={{ padding: "6px 16px 2px" }} className="small fa">{q ? `${res.length}${res.length === 60 ? "+" : ""} results` : "Suggested"}</div>
        <div className="pal-list" ref={list}>
          {!res.length && <div style={{ padding: "14px 10px" }} className="mu">No matches.</div>}
          {res.map((it, i) => (
            <div key={i} className={"pal-item" + (i === sel ? " on" : "")} onMouseMove={() => i !== sel && setIdx(i)} onClick={() => run(it)}>
              <span className="grp">{it.grp}</span>
              <span className="ell grow">{it.label}</span>
              <span className="small fa ell" style={{ maxWidth: 220 }}>{it.sub}</span>
              <span className="fa" style={{ width: 14 }}>{i === sel ? "↵" : ""}</span>
            </div>
          ))}
        </div>
        <div className="pal-foot"><span>↑↓ navigate</span><span>↵ open</span><span>esc close</span></div>
      </div>
    </>
  );
}
