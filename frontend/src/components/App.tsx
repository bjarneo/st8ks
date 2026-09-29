import { lazy, Suspense, useEffect, useState } from "react";
import { changeTextSize, closeDetail, fail, init, openDock, ui } from "../lib/app";
import { useStore } from "../lib/store";
import { setWatched } from "../lib/tables";
import { TopBar } from "./TopBar";
import { Rail } from "./Rail";
import { Tree } from "./Tree";
import { StatusBar } from "./StatusBar";
import { Overview } from "./Overview";
import { ListView } from "./ListView";
import { EventsView } from "./EventsView";
import { RbacView } from "./RbacView";
import { HelmView } from "./HelmView";
import { ClustersView } from "./ClustersView";
import { TopologyView } from "./TopologyView";
import { Detail } from "./Detail";
import { Assistant } from "./Assistant";
import { Palette } from "./Palette";
import { Confirm } from "./Confirm";
import { SettingsModal } from "./SettingsModal";
import { DownView, NoContexts, Toast } from "./Misc";
import { ErrorBoundary } from "./ErrorBoundary";

// The dock holds xterm.js, so it loads when the first log or shell opens.
const Dock = lazy(() => import("./Dock").then((m) => ({ default: m.Dock })));
// The IDE holds CodeMirror and xterm.js, so it loads when it opens first.
const Ide = lazy(() => import("./ide/Ide").then((m) => ({ default: m.Ide })));

function useWatchManager() {
  const key = useStore(ui, (s) => {
    if (s.cluster?.status !== "Connected" || !s.tree.length) return "";
    const k = new Set<string>();
    if (s.settings?.mode === "ide") {
      for (const x of s.ideWatch) if (s.kinds[x]) k.add(x);
      return s.cluster.context + "|" + [...k].sort().join(",");
    }
    if (s.view === "list") k.add(s.kind);
    if (s.view === "overview") {
      k.add("Nodes");
      if (s.settings?.overviewStyle === "nodemap") k.add("Pods");
    }
    if (s.view === "events") k.add("Events");
    if (s.detail && !s.detail.create) k.add(s.detail.kind);
    return s.cluster.context + "|" + [...k].filter((x) => s.kinds[x]).sort().join(",");
  });
  useEffect(() => {
    const kinds = key.split("|")[1];
    void setWatched(kinds ? kinds.split(",") : []);
  }, [key]);
}

export function isTyping(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  return !!t?.closest?.("input, textarea, select, [contenteditable=true], .cm-editor, .xterm");
}

function useKeyboard() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const s = ui.get();
      // Ctrl or Cmd with +, - or 0 changes the text size, as in a browser.
      if ((e.metaKey || e.ctrlKey) && !e.altKey && ["=", "+", "-", "_", "0"].includes(e.key)) {
        e.preventDefault();
        return changeTextSize(e.key === "0" ? 0 : e.key === "-" || e.key === "_" ? -1 : 1);
      }
      // The IDE has its own keys.
      if (s.settings?.mode === "ide") {
        if (e.key === "Escape" && (s.cf || s.settingsOpen)) ui.set({ cf: null, settingsOpen: false });
        return;
      }
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        ui.set({ pal: !s.pal });
        return;
      }
      if (s.pal) return;
      if (e.key === "Escape") {
        if (s.cf) return ui.set({ cf: null });
        if (s.settingsOpen) return ui.set({ settingsOpen: false });
        if (isTyping(e)) {
          (e.target as HTMLElement).blur?.();
          return;
        }
        if (s.detail) return closeDetail();
        if (s.ai) return ui.set({ ai: false });
        return;
      }
      if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey || s.cf || s.settingsOpen) return;
      const d = s.detail;
      if (d && !d.create) {
        if (d.kind === "Pods" && e.key === "l") return openDock("logs", d.ns, d.name);
        if (d.kind === "Pods" && e.key === "s") return openDock("term", d.ns, d.name);
        const tabs: Record<string, "overview" | "yaml" | "events" | "related"> = { o: "overview", y: "yaml", e: "events", r: "related" };
        if (tabs[e.key]) return ui.set({ dtab: tabs[e.key] });
      }
      if (e.key === "a") ui.set({ ai: !s.ai });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}

function Stage() {
  const v = useStore(ui, (s) => s.view);
  const down = useStore(ui, (s) => s.cluster?.status === "Unreachable");
  const connecting = useStore(ui, (s) => s.cluster?.status === "Connecting");
  const hasDetail = useStore(ui, (s) => !!s.detail);
  const page = useStore(ui, (s) => s.settings?.detailLayout === "page" && !!s.detail);
  const noCtx = useStore(ui, (s) => s.ready && !s.contexts.length);
  if (noCtx && v !== "clusters") return <NoContexts />;
  if (down && v !== "clusters") return <DownView />;
  if (connecting && v !== "clusters") {
    return (
      <div className="center">
        <div className="flex mu"><span className="spinner" />Connecting…</div>
      </div>
    );
  }
  return (
    <>
      {!page && v === "overview" && <Overview />}
      {!page && v === "list" && <ListView />}
      {!page && v === "events" && <EventsView />}
      {!page && v === "rbac" && <RbacView />}
      {!page && v === "helm" && <HelmView />}
      {!page && v === "clusters" && <ClustersView />}
      {!page && v === "topology" && <TopologyView />}
      {hasDetail && <Detail />}
    </>
  );
}

export function App() {
  useEffect(() => {
    init().catch(fail);
  }, []);
  useWatchManager();
  useKeyboard();
  const ready = useStore(ui, (s) => s.ready);
  const dock = useStore(ui, (s) => s.dock.length > 0);
  const ai = useStore(ui, (s) => s.ai);
  const pal = useStore(ui, (s) => s.pal);
  const cf = useStore(ui, (s) => !!s.cf);
  const settings = useStore(ui, (s) => s.settingsOpen);
  const mode = useStore(ui, (s) => s.settings?.mode ?? "cluster");
  // The IDE stays mounted after it opens once, so its editors and terminal
  // keep their state while the cluster views show.
  const [ideLoaded, setIdeLoaded] = useState(false);
  useEffect(() => {
    if (mode === "ide") setIdeLoaded(true);
  }, [mode]);
  if (!ready) return <div className="app" />;
  return (
    <div className="app">
      <div className="mode-box" hidden={mode === "ide"}>
        <TopBar />
        <div className="body">
          <Rail />
          <Tree />
          <div className="main">
            <div className="stage">
              <ErrorBoundary label="view"><Stage /></ErrorBoundary>
            </div>
            {dock && <ErrorBoundary label="dock"><Suspense fallback={null}><Dock /></Suspense></ErrorBoundary>}
          </div>
          {ai && <ErrorBoundary label="assistant"><Assistant /></ErrorBoundary>}
        </div>
        <StatusBar />
      </div>
      {ideLoaded && (
        <div className="mode-box" hidden={mode !== "ide"}>
          <ErrorBoundary label="IDE">
            <Suspense fallback={<div className="center"><div className="flex mu"><span className="spinner" />Loading the IDE…</div></div>}>
              <Ide visible={mode === "ide"} />
            </Suspense>
          </ErrorBoundary>
        </div>
      )}
      {pal && mode !== "ide" && <Palette />}
      {cf && <Confirm />}
      {settings && <SettingsModal />}
      <Toast />
    </div>
  );
}
