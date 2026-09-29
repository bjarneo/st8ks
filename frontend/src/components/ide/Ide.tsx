import "./ide.css";
import { useEffect, useMemo, useState } from "react";
import { call } from "../../lib/bridge";
import { connect, fail, saveSettings, toggleDensity, toggleTheme, ui } from "../../lib/app";
import { shallowEqual, useStore } from "../../lib/store";
import {
  askApply, closeFolder, dryRun, fixAtCursor, forgetRecent, ide, isLive, openFolder, refreshWs, reloadClean, save, startIde, textOf,
} from "../../lib/ide";
import type { Inspect } from "../../lib/types";
import { Logo, ModeSwitch, statusDot } from "../TopBar";
import { useRowHeight } from "../VTable";
import { Sidebar } from "./Sidebar";
import { EditorArea } from "./EditorArea";
import { Panel } from "./Panel";
import { Inspector } from "./Inspector";
import { IdePalette } from "./IdePalette";
import { ApplyDialog } from "./ApplyDialog";

/** LIVE_KINDS are the kinds that the live objects view lists. */
export const LIVE_KINDS = ["Deployments", "StatefulSets", "Pods", "Services", "ConfigMaps"];

function useMac() {
  return useStore(ui, (s) => s.platform === "darwin");
}

/** keyLabel prints a shortcut for the platform. */
export function keyLabel(mac: boolean, k: string): string {
  return mac ? "⌘" + k : "Ctrl " + k;
}

function useIdeKeys(visible: boolean) {
  useEffect(() => {
    if (!visible) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      const u = ui.get();
      if (u.cf || u.settingsOpen) return;
      const t = e.target as HTMLElement | null;
      // Shell keys such as Ctrl K and Ctrl P belong to the terminal.
      if (e.ctrlKey && !e.metaKey && t?.closest?.(".xterm")) return;
      const mod = e.metaKey || e.ctrlKey;
      const k = e.key.toLowerCase();
      const s = ide.get();
      if (mod && (k === "p" || k === "k")) {
        e.preventDefault();
        ide.set({ pal: !s.pal });
        return;
      }
      if (s.pal) return;
      if (e.key === "Escape") {
        if (s.plan) ide.set({ plan: null });
        return;
      }
      if (!mod) return;
      if (k === "s") {
        e.preventDefault();
        void save();
      } else if (k === "j") {
        e.preventDefault();
        ide.set({ panelOpen: !s.panelOpen });
      } else if (e.key === ".") {
        e.preventDefault();
        fixAtCursor();
      } else if (e.key === "Enter") {
        e.preventDefault();
        void (e.shiftKey ? askApply() : dryRun());
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [visible]);
}

/** InspectLoop keeps the inspector data of the cursor position current. It
 * renders nothing, so a keystroke does not render the whole IDE. */
function InspectLoop({ visible }: { visible: boolean }) {
  const active = useStore(ide, (s) => s.active);
  const line = useStore(ide, (s) => s.cur.line);
  const edit = useStore(ide, (s) => s.edit);
  const ctx = useStore(ui, (s) => (s.cluster?.status === "Connected" ? s.cluster.context : ""));
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!visible) return;
    const h = setInterval(() => setTick((t) => t + 1), 3000);
    return () => clearInterval(h);
  }, [visible]);
  useEffect(() => {
    if (!visible || !active || isLive(active)) {
      ide.set({ inspect: null });
      return;
    }
    let dead = false;
    const h = setTimeout(() => {
      call<Inspect>("IdeInspect", active, textOf(active), line).then((r) => {
        if (!dead && ide.get().active === active) ide.set({ inspect: r });
      }, () => {});
    }, 90);
    return () => {
      dead = true;
      clearTimeout(h);
    };
  }, [visible, active, line, edit, ctx, tick]);
  return null;
}

export function Ide({ visible }: { visible: boolean }) {
  useEffect(() => {
    void startIde();
  }, []);
  useEffect(() => {
    const set = () => void call("IdeSetActive", visible && !document.hidden);
    set();
    if (visible) void reloadClean();
    document.addEventListener("visibilitychange", set);
    return () => document.removeEventListener("visibilitychange", set);
  }, [visible]);
  useIdeKeys(visible);
  const ws = useStore(ide, (s) => !!s.ws);
  const view = useStore(ide, (s) => s.view);
  const pal = useStore(ide, (s) => s.pal);
  const plan = useStore(ide, (s) => !!s.plan);
  const inspOn = useStore(ui, (s) => !s.settings?.ideHideInspector);
  const [wide, setWide] = useState(window.innerWidth >= 1100);
  useEffect(() => {
    const f = () => setWide(window.innerWidth >= 1100);
    window.addEventListener("resize", f);
    return () => window.removeEventListener("resize", f);
  }, []);
  useEffect(() => {
    ui.set({ ideWatch: visible && ws && view === "cluster" ? LIVE_KINDS.concat("Namespaces") : [] });
  }, [visible, ws, view]);
  const irh = useRowHeight() - 4;
  return (
    <>
      <InspectLoop visible={visible} />
      <IdeTopBar />
      <div className="body" style={{ "--irh": irh + "px" } as React.CSSProperties}>
        {ws ? (
          <>
            <ActivityBar />
            <Sidebar />
            <div className="ide-main">
              <EditorArea />
              <Panel visible={visible} />
            </div>
            {inspOn && wide && <Inspector />}
          </>
        ) : (
          <Welcome />
        )}
      </div>
      <IdeStatus />
      {pal && <IdePalette />}
      {plan && <ApplyDialog />}
    </>
  );
}

// ---- Top bar ----

function WorkspaceMenu({ close }: { close: () => void }) {
  const recent = useStore(ide, (s) => s.recent);
  const root = useStore(ide, (s) => s.ws?.root);
  const pick = (fn: () => unknown) => () => {
    close();
    fn();
  };
  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="menu" style={{ width: 340 }}>
        <div className="menu-item" onClick={pick(() => void openFolder())}>Open folder…</div>
        <div className="menu-item" onClick={pick(() => void refreshWs().then(reloadClean))}>Reload files and Git status</div>
        {root && <div className="menu-item" onClick={pick(() => void closeFolder())}>Close folder</div>}
        {recent.filter((r) => r !== root).length > 0 && <div className="menu-sep" />}
        {recent.filter((r) => r !== root).map((r) => (
          <div key={r} className="menu-item mono" style={{ fontSize: "calc(11.5px * var(--zf))" }} title={r} onClick={pick(() => void openFolder(r))}>
            <span className="ell grow">{r}</span>
          </div>
        ))}
      </div>
    </>
  );
}

function ContextMenu({ close }: { close: () => void }) {
  const contexts = useStore(ui, (s) => s.contexts);
  const cur = useStore(ui, (s) => s.cluster?.context);
  const curStatus = useStore(ui, (s) => s.cluster?.status);
  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="menu" style={{ width: 300 }}>
        <div className="small fa" style={{ padding: "4px 8px 6px" }}>Target context for checks, diff, apply and terminal</div>
        {contexts.map((c) => (
          <div key={c.name} className="menu-item" onClick={() => {
            close();
            if (c.name !== cur) void connect(c.name);
          }}>
            <span className="mark">{c.name === cur ? "✓" : ""}</span>
            <span className="dot sm" style={{ background: statusDot(c.name === cur ? curStatus : c.status) }} />
            <span className="ell grow">{c.name}</span>
            <span className="mono fa" style={{ fontSize: "calc(10.5px * var(--zf))" }}>{[c.dist, c.version].filter(Boolean).join(" ")}</span>
          </div>
        ))}
        {!contexts.length && <div className="menu-item fa">No contexts. Add a kubeconfig in the Cluster view.</div>}
      </div>
    </>
  );
}

function IdeTopBar() {
  const ws = useStore(ide, (s) => s.ws ? { name: s.ws.name, root: s.ws.root, git: s.ws.git, branch: s.ws.branch, ahead: s.ws.ahead, behind: s.ws.behind } : null, shallowEqual);
  const busy = useStore(ide, (s) => s.busy);
  const cl = useStore(ui, (s) => s.cluster);
  const theme = useStore(ui, (s) => s.settings?.theme);
  const density = useStore(ui, (s) => s.settings?.density);
  const mac = useMac();
  const [menu, setMenu] = useState<"" | "ws" | "ctx">("");
  return (
    <div className="top" onDoubleClick={(e) => {
      if (e.target === e.currentTarget) window.runtime?.WindowToggleMaximise?.();
    }}>
      <div className="brand" style={{ flex: "none" }}><Logo /><b>st8ks</b></div>
      <div className="vsep" />
      <div style={{ position: "relative", minWidth: 0, display: "flex", flex: "0 1 auto" }}>
        {ws ? (
          <button className="ide-repo" title={ws.root} onClick={() => setMenu(menu === "ws" ? "" : "ws")}>
            <span className="name">{ws.name}</span>
            {ws.git && <span className="br">⎇ {ws.branch || "detached"}</span>}
            {ws.ahead > 0 && <span className="br ac">↑{ws.ahead}</span>}
            {ws.behind > 0 && <span className="br wa">↓{ws.behind}</span>}
          </button>
        ) : (
          <button className="btn" onClick={() => setMenu(menu === "ws" ? "" : "ws")}>Open folder<span className="fa">⌄</span></button>
        )}
        {menu === "ws" && <WorkspaceMenu close={() => setMenu("")} />}
      </div>
      <div style={{ position: "relative", flex: "none" }}>
        <button className="btn ctx-btn" title="Target context" onClick={() => setMenu(menu === "ctx" ? "" : "ctx")}>
          <span className="dot" style={{ background: statusDot(cl?.status) }} />
          <span style={{ fontWeight: 500 }} className="ell">{cl?.context ?? "No context"}</span>
          <span className="fa">⌄</span>
        </button>
        {menu === "ctx" && <ContextMenu close={() => setMenu("")} />}
      </div>
      <div style={{ flex: 1, display: "flex", justifyContent: "center", minWidth: 44 }}>
        <button className="btn search-btn" style={{ maxWidth: 400 }} onClick={() => ide.set({ pal: true })}>
          <span className="ell" style={{ flex: 1, textAlign: "left" }}>Go to file or run a command…</span>
          <span className="kbd" style={{ border: "1px solid var(--ln)", padding: "1px 5px" }}>{keyLabel(mac, "P")}</span>
        </button>
      </div>
      <button className="btn" title="Server dry run of the file" disabled={!!busy} onClick={() => void dryRun()}>
        {busy === "dry" && <span className="spinner" />}Dry-run<span className="kbd">{mac ? "⌘↵" : "Ctrl ↵"}</span>
      </button>
      <button className="btn primary" title="Server-side apply of the file" disabled={!!busy} onClick={() => void askApply()}>
        {(busy === "plan" || busy === "apply") && <span className="spinner" />}Apply…
      </button>
      <div className="vsep" />
      <ModeSwitch mode="ide" />
      <button className="btn" style={{ color: "var(--mu)" }} title="Toggle density" onClick={toggleDensity}>{density === "compact" ? "Compact" : "Comfortable"}</button>
      <button className="btn icon" style={{ color: "var(--mu)" }} title="Toggle theme" onClick={toggleTheme}>{theme === "dark" ? "☾" : "☀"}</button>
      <button className="btn icon" style={{ color: "var(--mu)" }} title="Settings" onClick={() => ui.set({ settingsOpen: true })}>⚙</button>
    </div>
  );
}

// ---- Activity bar ----

const icons = {
  explorer: <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M4 2.5h6l3.5 3.5v9.5H4z" /><path d="M10 2.5V6h3.5" /></svg>,
  cluster: <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M9 2l6 3.3v7.4L9 16l-6-3.3V5.3z" /><path d="M3 5.3L9 8.6l6-3.3M9 8.6V16" /></svg>,
  git: <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="5" cy="4" r="1.6" /><circle cx="5" cy="14" r="1.6" /><circle cx="13" cy="7" r="1.6" /><path d="M5 5.6v6.8M13 8.6c0 2.4-3 2.6-6.6 4.2" /></svg>,
};

function ActivityBar() {
  const view = useStore(ide, (s) => s.view);
  const changes = useStore(ide, (s) => s.ws?.changes.length ?? 0);
  const items: [typeof view, string][] = [["explorer", "Explorer"], ["cluster", "Live objects"], ["git", "Source control"]];
  return (
    <div className="ide-act">
      {items.map(([id, title]) => (
        <button key={id} title={title} className={view === id ? "on" : ""} onClick={() => ide.set({ view: id })}>
          {icons[id]}
          {id === "git" && changes > 0 && <span className="badge">{changes}</span>}
        </button>
      ))}
    </div>
  );
}

// ---- Welcome ----

function Welcome() {
  const recent = useStore(ide, (s) => s.recent);
  const started = useStore(ide, (s) => s.started);
  const busy = useStore(ide, (s) => s.busy === "open");
  if (!started) return <div className="center"><span className="spinner" /></div>;
  return (
    <div className="ide-welcome">
      <div className="col card" style={{ gap: 14 }}>
        <div style={{ fontSize: "calc(16px * var(--zf))", fontWeight: 600 }}>Open a folder of Kubernetes manifests</div>
        <div className="mu" style={{ lineHeight: 1.55 }}>
          The IDE checks your YAML against the schema and the live objects of the connected cluster. It shows the diff to the cluster,
          runs server dry runs, applies with server-side apply, and commits and pushes with Git.
        </div>
        <div className="flex">
          <button className="btn primary" disabled={busy} onClick={() => void openFolder()}>{busy && <span className="spinner" />}Open folder…</button>
        </div>
        {recent.length > 0 && (
          <div className="col" style={{ gap: 6 }}>
            <span className="sec-label">Recent folders</span>
            <div className="ide-recent">
              {recent.map((r) => (
                <div key={r} className="menu-item" title={r} onClick={() => void openFolder(r)}>
                  <span className="ell grow">{r}</span>
                  <span className="fa" title="Remove from the list" onClick={(e) => {
                    e.stopPropagation();
                    void forgetRecent(r).catch(fail);
                  }}>✕</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// ---- Status bar ----

function IdeStatus() {
  const ws = useStore(ide, (s) => s.ws ? { git: s.ws.git, branch: s.ws.branch, ahead: s.ws.ahead } : null, shallowEqual);
  const cl = useStore(ui, (s) => s.cluster);
  const counts = useStore(ide, (s) => {
    let e = 0, w = 0;
    for (const p in s.diags) for (const d of s.diags[p]) {
      if (d.sev === "error") e++;
      else if (d.sev === "warning") w++;
    }
    return [e, w] as const;
  }, shallowEqual);
  const lensOff = useStore(ui, (s) => !!s.settings?.ideLensOff);
  const active = useStore(ide, (s) => s.active);
  const dirty = useStore(ide, (s) => (s.active ? !!s.dirty[s.active] : false));
  const cur = useStore(ide, (s) => s.cur, shallowEqual);
  const schema = useStore(ide, (s) => s.inspect?.schema ?? "");
  const lang = useMemo(() => {
    if (!active) return "";
    if (isLive(active)) return "YAML · live";
    if (/\.md$/i.test(active)) return "Markdown";
    if (/\/templates\//.test(active)) return "Helm template";
    return /\.ya?ml$/i.test(active) || /Kustomization$/.test(active) ? "YAML" : "Plain text";
  }, [active]);
  const version = cl?.status === "Connected" && cl.version ? cl.version.replace(/^(v\d+\.\d+).*$/, "$1") : "";
  return (
    <div className="status">
      {ws?.git && <span>⎇ {ws.branch || "detached"}{ws.ahead ? ` ↑${ws.ahead}` : ""}</span>}
      <span className="flex" style={{ gap: 6 }}><span className="dot sm" style={{ background: statusDot(cl?.status) }} />{cl?.context ?? "No context"}</span>
      {ws && (
        <span className="link flex" style={{ gap: 10 }} onClick={() => ide.set({ panel: "problems", panelOpen: true })}>
          <span style={{ color: counts[0] ? "var(--er)" : undefined }}>✕ {counts[0]}</span>
          <span style={{ color: counts[1] ? "var(--wa)" : undefined }}>▲ {counts[1]}</span>
        </span>
      )}
      {lensOff && <span className="link" onClick={() => void saveSettings({ ideLensOff: false })}>error lens off</span>}
      <span style={{ flex: 1 }} />
      {active && (
        <>
          <span>{isLive(active) ? "read-only" : dirty ? "● unsaved" : "saved"}</span>
          <span>Ln {cur.line + 1}, Col {cur.col}</span>
          <span>Spaces: 2</span>
          <span>{lang}</span>
        </>
      )}
      {version && <span title={schema}>k8s {version} schema</span>}
    </div>
  );
}
