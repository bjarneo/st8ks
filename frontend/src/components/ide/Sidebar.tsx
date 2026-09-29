import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { kindSingular, ui } from "../../lib/app";
import { shallowEqual, useStore } from "../../lib/store";
import { ensureLoaded, useTables, table } from "../../lib/tables";
import { toneVar } from "../../lib/format";
import { baseName, commit, dirName, ide, isLive, liveParts, openFile, openLive, push, saveAll } from "../../lib/ide";
import type { Diag, IdeChange, Row } from "../../lib/types";
import { useRowHeight } from "../VTable";
import { keyLabel, LIVE_KINDS } from "./Ide";

export function Sidebar() {
  const view = useStore(ide, (s) => s.view);
  const name = useStore(ide, (s) => s.ws?.name ?? "");
  const ctx = useStore(ui, (s) => s.cluster?.context ?? "no context");
  const title = view === "explorer" ? `EXPLORER · ${name}` : view === "cluster" ? `LIVE · ${ctx}` : "SOURCE CONTROL";
  return (
    <div className="ide-side">
      <div className="ide-side-h"><span className="t" title={title}>{title}</span></div>
      {view === "explorer" && <Explorer />}
      {view === "cluster" && <LiveObjects />}
      {view === "git" && <SourceControl />}
    </div>
  );
}

// ---- Explorer ----

interface TNode { name: string; path: string; dir: boolean; kids: TNode[] }

function buildTree(files: string[]): TNode {
  const root: TNode = { name: "", path: "", dir: true, kids: [] };
  const dirs = new Map<string, TNode>([["", root]]);
  const dirOf = (p: string): TNode => {
    const have = dirs.get(p);
    if (have) return have;
    const parent = dirOf(dirName(p));
    const n: TNode = { name: baseName(p), path: p, dir: true, kids: [] };
    parent.kids.push(n);
    dirs.set(p, n);
    return n;
  };
  for (const f of files) dirOf(dirName(f)).kids.push({ name: baseName(f), path: f, dir: false, kids: [] });
  const sort = (n: TNode) => {
    n.kids.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
    n.kids.forEach((k) => k.dir && sort(k));
  };
  sort(root);
  return root;
}

interface VRow { node: TNode; depth: number; open: boolean }

const NO_FILES: string[] = [];
const NO_CHANGES: IdeChange[] = [];

const stColor: Record<string, string> = { M: "var(--wa)", A: "var(--ok)", U: "var(--ok)", D: "var(--er)", R: "var(--ac)" };

function diagCounts(ds: Diag[] | undefined): [number, number] {
  let e = 0, w = 0;
  for (const d of ds ?? []) {
    if (d.sev === "error") e++;
    else if (d.sev === "warning") w++;
  }
  return [e, w];
}

function Explorer() {
  const files = useStore(ide, (s) => s.ws?.files) ?? NO_FILES;
  const changes = useStore(ide, (s) => s.ws?.changes) ?? NO_CHANGES;
  const diags = useStore(ide, (s) => s.diags);
  const openDirs = useStore(ide, (s) => s.openDirs);
  const active = useStore(ide, (s) => s.active);
  const rh = useRowHeight() - 4;
  const tree = useMemo(() => buildTree(files), [files]);
  const small = files.length <= 300;
  const byPath = useMemo(() => new Map(changes.map((c) => [c.path, c.st])), [changes]);
  // Folder flags: 1 warning, 2 error, 4 changed.
  const flags = useMemo(() => {
    const f = new Map<string, number>();
    const up = (p: string, bit: number) => {
      for (let d = dirName(p); ; d = dirName(d)) {
        f.set(d, (f.get(d) ?? 0) | bit);
        if (!d) break;
      }
    };
    for (const p in diags) {
      const [e, w] = diagCounts(diags[p]);
      if (e) up(p, 2);
      else if (w) up(p, 1);
    }
    for (const c of changes) up(c.path, 4);
    return f;
  }, [diags, changes]);
  const rows = useMemo(() => {
    const out: VRow[] = [];
    const walk = (n: TNode, depth: number) => {
      for (const k of n.kids) {
        const open = k.dir && (openDirs[k.path] ?? small);
        out.push({ node: k, depth, open });
        if (open) walk(k, depth + 1);
      }
    };
    walk(tree, 0);
    return out;
  }, [tree, openDirs, small]);

  const box = useRef<HTMLDivElement>(null);
  const [view, setView] = useState({ top: 0, h: 600 });
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setView({ top: el.scrollTop, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // Keep the active file in view.
  useEffect(() => {
    const el = box.current;
    const i = rows.findIndex((r) => r.node.path === active);
    if (!el || i < 0) return;
    const y = i * rh;
    if (y < el.scrollTop || y + rh > el.scrollTop + el.clientHeight) el.scrollTop = Math.max(0, y - el.clientHeight / 3);
  }, [active]); // eslint-disable-line react-hooks/exhaustive-deps
  const start = Math.max(0, Math.floor(view.top / rh) - 20);
  const end = Math.min(rows.length, Math.ceil((view.top + view.h) / rh) + 20);
  const toggle = (p: string, open: boolean) => ide.set((s) => ({ openDirs: { ...s.openDirs, [p]: !open } }));
  return (
    <div className="ide-side-b" ref={box} onScroll={(e) => setView({ top: e.currentTarget.scrollTop, h: e.currentTarget.clientHeight })}>
      {!rows.length && <div className="ide-note">The folder has no files.</div>}
      <div style={{ height: rows.length * rh, position: "relative" }}>
        {rows.slice(start, end).map((r, i) => {
          const n = r.node;
          const top = (start + i) * rh;
          const pad = 10 + r.depth * 12;
          if (n.dir) {
            const fl = flags.get(n.path) ?? 0;
            return (
              <div key={n.path} className="ide-row" title={n.path} style={{ position: "absolute", left: 0, right: 0, top, paddingLeft: pad, color: fl & 2 ? "var(--er)" : fl & 1 ? "var(--wa)" : undefined }}
                onClick={() => toggle(n.path, r.open)}>
                <span className="chev">{r.open ? "⌄" : "›"}</span>
                <span className="lbl">{n.name}</span>
                {fl & 4 ? <span className="st" style={{ color: "var(--wa)", opacity: 0.6 }}>•</span> : null}
              </div>
            );
          }
          const [e, w] = diagCounts(diags[n.path]);
          const st = byPath.get(n.path);
          const on = n.path === active;
          return (
            <div key={n.path} className={"ide-row" + (on ? " on" : "")} title={n.path}
              style={{ position: "absolute", left: 0, right: 0, top, paddingLeft: pad, color: e ? "var(--er)" : w ? "var(--wa)" : undefined }}
              onClick={() => void openFile(n.path)}>
              <span className="chev" />
              <span className="lbl">{n.name}</span>
              {e + w > 0 && <span className="n" style={{ color: e ? "var(--er)" : "var(--wa)" }}>{e + w}</span>}
              {st && <span className="st" style={{ color: stColor[st] }}>{st}</span>}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---- Live objects ----

function colIndex(kind: string, want: (t: string, l: string) => boolean): number {
  const k = ui.get().kinds[kind];
  return k ? k.cols.findIndex((c) => want(c.t, c.l)) : -1;
}

function liveMeta(kind: string, r: Row): { meta: string; tone: string } {
  const at = (i: number) => (i >= 0 ? { v: r.c[i] ?? "", t: r.k[i] ?? "-" } : { v: "", t: "-" });
  switch (kind) {
    case "Pods": {
      const ready = at(colIndex(kind, (t) => t === "ready"));
      const st = at(colIndex(kind, (t) => t === "status"));
      return { meta: ready.v, tone: st.t !== "-" ? st.t : ready.t };
    }
    case "Services": {
      const t = at(colIndex(kind, (_, l) => l === "Type"));
      return { meta: t.v, tone: "o" };
    }
    case "ConfigMaps": {
      const d = at(colIndex(kind, (_, l) => l === "Data"));
      return { meta: d.v ? d.v + " keys" : "", tone: "o" };
    }
  }
  const ready = at(colIndex(kind, (t) => t === "ready"));
  return { meta: ready.v, tone: ready.t === "-" ? "o" : ready.t };
}

function LiveObjects() {
  const connected = useStore(ui, (s) => s.cluster?.status === "Connected");
  const kinds = useStore(ui, (s) => LIVE_KINDS.filter((k) => s.kinds[k]), shallowEqual);
  const defNs = useStore(ui, (s) => s.nsSel[0] || s.contexts.find((c) => c.name === s.cluster?.context)?.namespace || "default");
  // The namespace follows the object at the cursor, or the live tab that shows.
  const docNs = useStore(ide, (s) => (isLive(s.active) ? liveParts(s.active!)[1] : s.inspect?.ref?.ns ?? ""));
  const pick = useStore(ide, (s) => s.liveNs);
  const last = useRef("");
  if (docNs) last.current = docNs;
  const ns = pick || last.current || defNs;
  const ver = useTables(kinds.concat("Namespaces"));
  useEffect(() => {
    void ensureLoaded("Namespaces");
  }, []);
  const namespaces = useMemo(() => [...table("Namespaces").rows.values()].map((r) => r.m).sort(), [ver]); // eslint-disable-line react-hooks/exhaustive-deps
  const groups = useMemo(() => kinds.map((k) => {
    const rows = [...table(k).rows.values()].filter((r) => r.n === ns).sort((a, b) => (a.m < b.m ? -1 : 1));
    return { kind: k, rows };
  }).filter((g) => g.rows.length), [kinds, ns, ver]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!connected) return <div className="ide-side-b"><div className="ide-note">Connect to a cluster to see its live objects.</div></div>;
  return (
    <div className="ide-side-b">
      <div className="ide-note">
        Live objects in namespace{" "}
        <select className="mono" value={ns} onChange={(e) => ide.set({ liveNs: e.target.value })}
          style={{ height: 20, border: "1px solid var(--ln)", borderRadius: 4, background: "var(--bg)", fontSize: "calc(11px * var(--zf))", maxWidth: 140 }}>
          {!namespaces.includes(ns) && <option value={ns}>{ns}</option>}
          {namespaces.map((n) => <option key={n} value={n}>{n}</option>)}
        </select>
        . They open read-only.
      </div>
      {!groups.length && <div className="ide-note">No deployments, pods, services or config maps in {ns}.</div>}
      {groups.map((g) => (
        <div key={g.kind}>
          <div className="ide-grp">{ui.get().kinds[g.kind]?.label ?? g.kind}</div>
          {g.rows.map((r) => {
            const m = liveMeta(g.kind, r);
            return (
              <div key={r.u} className="ide-row" title={`${kindSingular(g.kind)} ${r.m}`} style={{ paddingLeft: 22, gap: 8 }} onClick={() => void openLive(g.kind, ns, r.m)}>
                <span className="dot sm" style={{ background: toneVar(m.tone, "var(--ok)") }} />
                <span className="lbl">{r.m}</span>
                <span className="meta">{m.meta}</span>
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

// ---- Source control ----

function SourceControl() {
  const ws = useStore(ide, (s) => s.ws);
  const msg = useStore(ide, (s) => s.msg);
  const busy = useStore(ide, (s) => s.busy);
  const unsaved = useStore(ide, (s) => Object.keys(s.dirty).length);
  const mac = useStore(ui, (s) => s.platform === "darwin");
  if (!ws) return null;
  if (!ws.git) {
    return <div className="ide-side-b"><div className="ide-note">This folder is not a Git repository, or Git is not installed. Open a Git repository to commit and push.</div></div>;
  }
  const n = ws.changes.length;
  const canCommit = n > 0 && msg.trim() !== "";
  return (
    <div className="ide-side-b">
      <div className="ide-git">
        <textarea rows={3} placeholder={`Commit message (${keyLabel(mac, "Enter")} to commit)`} value={msg}
          onChange={(e) => ide.set({ msg: e.target.value })}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              e.preventDefault();
              void commit();
            }
          }} />
        <button className="btn primary" style={{ opacity: canCommit ? 1 : 0.45 }} disabled={busy === "commit"} onClick={() => void commit()}>
          {busy === "commit" && <span className="spinner" />}Commit {n} {n === 1 ? "file" : "files"}
        </button>
        {ws.ahead > 0 && (
          <button className="btn" disabled={busy === "push"} onClick={() => void push()}>
            {busy === "push" && <span className="spinner" />}Push ↑{ws.ahead} to {ws.upstream || "origin"}
          </button>
        )}
        {!ws.upstream && ws.branch && ws.ahead === 0 && (
          <button className="btn" disabled={busy === "push"} onClick={() => void push()}>
            {busy === "push" && <span className="spinner" />}Publish {ws.branch} to origin
          </button>
        )}
        {ws.behind > 0 && <div className="small wa">{ws.behind} {ws.behind === 1 ? "commit" : "commits"} on {ws.upstream} are not pulled. Pull in the terminal.</div>}
        {unsaved > 0 && (
          <div className="small wa">
            {unsaved} unsaved {unsaved === 1 ? "file is" : "files are"} not in the commit. <a href="#" onClick={(e) => {
              e.preventDefault();
              void saveAll();
            }}>Save all</a>
          </div>
        )}
      </div>
      <div className="ide-grp" style={{ paddingTop: 4 }}>Changes · {n}</div>
      {n === 0 && <div className="ide-note mu" style={{ fontSize: "inherit" }}>The working tree is clean.</div>}
      {ws.changes.map((c) => <ChangeRow key={c.path} c={c} />)}
    </div>
  );
}

function ChangeRow({ c }: { c: IdeChange }) {
  const label: Record<string, string> = { M: "Modified", A: "Added", U: "Untracked", D: "Deleted", R: "Renamed" };
  return (
    <div className="ide-row" title={`${c.path} · ${label[c.st] ?? c.st}`} style={{ paddingLeft: 14, gap: 8, cursor: c.st === "D" ? "default" : "pointer" }}
      onClick={() => {
        if (c.st !== "D") void openFile(c.path, { mode: c.st === "M" || c.st === "R" ? "git" : "src" });
      }}>
      <span className="lbl" style={{ textDecoration: c.st === "D" ? "line-through" : undefined }}>{baseName(c.path)}</span>
      <span className="meta ell" style={{ maxWidth: 110 }}>{dirName(c.path)}</span>
      <span className="st" style={{ color: stColor[c.st] }}>{c.st}</span>
    </div>
  );
}
