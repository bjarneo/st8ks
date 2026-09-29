// Global UI state and the actions that change it.

import { call, errText, on } from "./bridge";
import { createStore } from "./store";
import { reloadTable, resetTables } from "./tables";
import { singular } from "./format";
import type {
  ClusterState, ContextInfo, Counts, Forward, InitState, Issue, KindInfo, Overview, Ref, Settings,
  TreeItem, TreeSection, TreeState, FixProposal, Source,
} from "./types";

export type View = "overview" | "list" | "events" | "rbac" | "helm" | "clusters" | "topology";
export type DTab = "overview" | "yaml" | "events" | "related";
export type YMode = "live" | "edit" | "diff";

export interface DetailRef { kind: string; ns: string; name: string; create?: boolean }
export interface DockTab { id: string; type: "logs" | "term"; ns: string; pod: string; container?: string; previous?: boolean }
export interface ConfirmSpec {
  title: string;
  items: string[];
  note: string;
  cta: string;
  danger?: boolean;
  word?: string | null;
  input?: { label: string; value: string; type?: "number" | "text" };
  run: (dry: boolean, input?: string) => Promise<string>;
  noDry?: boolean;
  after?: () => void;
}

export interface UI {
  ready: boolean;
  platform: string;
  version: string;
  settings: Settings | null;
  loadErr: string;
  contexts: ContextInfo[];
  sources: Source[];
  explicit: boolean;
  cluster: ClusterState | null;
  prevContext: string;
  tree: TreeSection[];
  kinds: Record<string, KindInfo>;
  counts: Counts;
  overview: Overview | null;
  issues: Issue[];
  forwards: Forward[];
  view: View;
  kind: string;
  nsSel: string[];
  filter: string;
  savedView: string;
  sort: { i: number; dir: 1 | -1 } | null;
  checked: Record<string, true>;
  detail: DetailRef | null;
  dtab: DTab;
  ymode: YMode;
  draft: string | null;
  detailNonce: number;
  dock: DockTab[];
  dockActive: string | null;
  dockMin: boolean;
  pal: boolean;
  ai: boolean;
  aiIssue: string | null;
  aiAuto: number;
  cf: ConfirmSpec | null;
  toast: string | null;
  applied: Record<string, number>;
  settingsOpen: boolean;
  helmSel: string | null;
  /** ideWatch lists the kinds that the IDE shows, such as its live objects view. */
  ideWatch: string[];
}

export const ui = createStore<UI>({
  ready: false, platform: "linux", version: "dev", settings: null, loadErr: "", contexts: [], sources: [], explicit: false, cluster: null, prevContext: "",
  tree: [], kinds: {}, counts: {}, overview: null, issues: [], forwards: [],
  view: "overview", kind: "Pods", nsSel: [], filter: "", savedView: "All", sort: null, checked: {},
  detail: null, dtab: "overview", ymode: "live", draft: null, detailNonce: 0,
  dock: [], dockActive: null, dockMin: false, pal: false, ai: false, aiIssue: null, aiAuto: 0,
  cf: null, toast: null, applied: {}, settingsOpen: false, helmSel: null, ideWatch: [],
});

let toastTimer = 0;
export function say(msg: string) {
  clearTimeout(toastTimer);
  ui.set({ toast: msg });
  toastTimer = window.setTimeout(() => ui.set({ toast: null }), 3800);
}

export function fail(e: unknown) {
  say(errText(e));
}

/** TEXT_SIZES are the steps of the text size setting, in percent. */
export const TEXT_SIZES = [80, 90, 100, 110, 120, 135, 150];

/** scale returns the text size factor, 1 for 100 %. */
export function scale(s: Settings | null | undefined): number {
  return (s?.textSize || 100) / 100;
}

function applyRoot(s: Settings) {
  const r = document.documentElement;
  const z = scale(s);
  r.dataset.theme = s.theme;
  r.dataset.density = s.density;
  // Rows, headers and log lines have fixed heights, so the code and the CSS use the same rounded values.
  r.style.setProperty("--zf", String(z));
  r.style.setProperty("--rh", Math.round((s.density === "comfortable" ? 36 : 28) * z) + "px");
  r.style.setProperty("--hh", Math.round(30 * z) + "px");
  r.style.setProperty("--lh", Math.round(20 * z) + "px");
}

/** changeTextSize moves the text size one step up or down. 0 resets it. */
export function changeTextSize(step: -1 | 0 | 1) {
  const cur = ui.get().settings?.textSize || 100;
  let next = 100;
  if (step > 0) next = TEXT_SIZES.find((v) => v > cur) ?? TEXT_SIZES[TEXT_SIZES.length - 1];
  if (step < 0) next = [...TEXT_SIZES].reverse().find((v) => v < cur) ?? TEXT_SIZES[0];
  if (next === cur) return;
  void saveSettings({ textSize: next });
  say(`Text size ${next} %`);
}

export async function saveSettings(patch: Partial<Settings>) {
  const cur = ui.get().settings;
  if (!cur) return;
  const next = { ...cur, ...patch };
  ui.set({ settings: next });
  applyRoot(next);
  try {
    await call("SaveSettings", next);
  } catch (e) {
    fail(e);
  }
}

export function kindInfo(name: string): KindInfo | undefined {
  return ui.get().kinds[name];
}

/** kindByKind maps a singular kind such as "Pod" to its table name. */
export function kindByKind(kind: string): string | undefined {
  const ks = ui.get().kinds;
  for (const k in ks) if (ks[k].kind === kind && !ks[k].custom) return k;
  for (const k in ks) if (ks[k].kind === kind) return k;
  return undefined;
}

export function kindLabel(name: string): string {
  return kindInfo(name)?.label ?? name;
}

export function kindSingular(name: string): string {
  return kindInfo(name)?.kind ?? singular(name);
}

// ---- Startup and contexts ----

let started = false;

export async function init() {
  if (started) return;
  started = true;
  subscribeEvents();
  const st = await call<InitState>("Init");
  applyRoot(st.settings);
  ui.set({
    settings: st.settings, contexts: st.contexts ?? [], sources: st.sources ?? [], explicit: st.explicit,
    platform: st.platform, version: st.version || "dev", loadErr: st.loadErr, ready: true,
  });
  document.documentElement.dataset.platform = st.platform;
  if (st.initial) void connect(st.initial);
  void call("ProbeContexts");
}

function subscribeEvents() {
  on<ClusterState>("cluster", (s) => {
    if (s.context === ui.get().cluster?.context) ui.set({ cluster: s });
  });
  on<ContextInfo[]>("contexts", (c) => ui.set({ contexts: c }));
  on<Source[]>("sources", (src) => ui.set({ sources: src ?? [] }));
  on<Counts>("counts", (c) => ui.set((s) => ({ counts: { ...s.counts, ...c } })));
  on<Overview>("overview", (o) => ui.set({ overview: o }));
  on<Issue[]>("issues", (i) => ui.set({ issues: i ?? [] }));
  on<Forward[]>("forwards", (f) => ui.set({ forwards: f }));
  on<TreeState>("tree", (t) => {
    const old = ui.get().kinds;
    const kinds = Object.fromEntries(t.kinds.map((k) => [k.name, k]));
    // The backend replaces the table of a CRD whose printer columns or
    // version changed, and its version counter starts again.
    for (const k of t.kinds) {
      const o = old[k.name];
      if (o && (o.version !== k.version || JSON.stringify(o.cols) !== JSON.stringify(k.cols))) void reloadTable(k.name);
    }
    ui.set({ tree: t.tree, kinds });
  });
  on<string>("toast", (m) => say(m));
}

export async function connect(name: string) {
  const s = ui.get();
  const prev = s.cluster?.status === "Connected" ? s.cluster.context : s.prevContext;
  resetTables();
  const saved = s.settings?.nsByContext?.[name] ?? [];
  ui.set({
    cluster: { context: name, status: "Connecting", server: "", dist: "", version: "", region: "", metrics: false },
    prevContext: prev && prev !== name ? prev : s.prevContext,
    tree: [], kinds: {}, counts: {}, overview: null, issues: [], detail: null, checked: {}, dock: [], dockActive: null,
    nsSel: saved, filter: "", aiIssue: null,
    view: s.view === "clusters" ? "overview" : s.view,
  });
  try {
    const st = await call<ClusterState>("Connect", name);
    if (ui.get().cluster?.context !== name) return;
    ui.set({ cluster: st });
    if (st.status !== "Connected") return;
    const [tree, counts, ov, issues, fw] = await Promise.all([
      call<TreeState>("Tree"), call<Counts>("Counts"), call<Overview>("Overview"), call<Issue[]>("Issues"), call<Forward[]>("Forwards"),
    ]);
    if (ui.get().cluster?.context !== name) return;
    ui.set({
      tree: tree.tree, kinds: Object.fromEntries(tree.kinds.map((k) => [k.name, k])),
      counts, overview: ov, issues, forwards: fw,
    });
  } catch (e) {
    if (ui.get().cluster?.context === name) {
      ui.set({ cluster: { context: name, status: "Unreachable", error: errText(e), server: "", dist: "", version: "", region: "", metrics: false } });
    }
  }
}

export async function retry() {
  const name = ui.get().cluster?.context;
  if (!name) return;
  if (!ui.get().tree.length) return connect(name);
  try {
    const st = await call<ClusterState>("Retry");
    // The user can switch context while the probe waits.
    if (ui.get().cluster?.context !== name) return;
    ui.set({ cluster: st });
    if (st.status !== "Connected") say("Still unreachable: " + (st.error ?? ""));
  } catch (e) {
    fail(e);
  }
}

export function setNs(nsSel: string[]) {
  ui.set({ nsSel });
  const s = ui.get();
  const ctx = s.cluster?.context;
  if (ctx && s.settings) void saveSettings({ nsByContext: { ...s.settings.nsByContext, [ctx]: nsSel } });
}

// ---- Navigation ----

export function nav(item: TreeItem) {
  if (item.view) {
    ui.set({ view: item.view as View, detail: null, checked: {}, filter: "", savedView: "All", sort: null });
  } else if (item.kind) {
    ui.set({ view: "list", kind: item.kind, detail: null, checked: {}, filter: "", savedView: "All", sort: null });
  }
}

export function goList(kind: string, savedView = "All", filter = "") {
  ui.set({ view: "list", kind, detail: null, checked: {}, filter, savedView, sort: null });
}

export function openDetail(ref: DetailRef, extra: Partial<UI> = {}) {
  const s = ui.get();
  const patch: Partial<UI> = { detail: ref, dtab: "overview", ymode: "live", draft: null, detailNonce: s.detailNonce + 1, ...extra };
  // Detail pages need a list or view behind them.
  if (s.view === "clusters") patch.view = "overview";
  ui.set(patch);
}

export function closeDetail() {
  ui.set({ detail: null, draft: null });
}

let dockSeq = 0;
export function openDock(type: "logs" | "term", ns: string, pod: string, container?: string, previous?: boolean) {
  ui.set((s) => {
    const same = s.dock.find((t) => t.type === type && t.ns === ns && t.pod === pod && (container === undefined || t.container === container) && (previous === undefined || !!t.previous === previous));
    if (same) return { dockActive: same.id, dockMin: false };
    const tab: DockTab = { id: `${type}-${++dockSeq}`, type, ns, pod, container, previous };
    return { dock: [...s.dock, tab], dockActive: tab.id, dockMin: false };
  });
}

export function closeDock(id: string) {
  ui.set((s) => {
    const dock = s.dock.filter((t) => t.id !== id);
    return { dock, dockActive: s.dockActive === id ? dock[dock.length - 1]?.id ?? null : s.dockActive };
  });
}

export function openAI(issue: string | null, auto = false) {
  ui.set((s) => ({ ai: true, aiIssue: issue, aiAuto: auto ? s.aiAuto + 1 : s.aiAuto }));
}

// ---- Confirmation and writes ----

export function ask(cf: ConfirmSpec) {
  ui.set({ cf });
}

/** protectedWord returns the word a user must type before a change to a
 * production namespace or context, or null. */
export function protectedWord(namespaces: string[], word: string): string | null {
  const re = protectedRe();
  const ctx = ui.get().cluster?.context ?? "";
  if (re.test(ctx) || namespaces.some((n) => n === "" || re.test(n))) return word;
  return null;
}

/** protectedRe is the pattern from the settings for protected names. */
export function protectedRe(): RegExp {
  try {
    return new RegExp(ui.get().settings?.protectedPattern || "prod", "i");
  } catch {
    return /prod/i;
  }
}

export function confirmDelete(kind: string, items: Ref[], after?: () => void) {
  const one = items.length === 1;
  const ownedPods = kind === "Pods";
  ask({
    title: `Delete ${items.length} ${one ? kindSingular(kind) : kindLabel(kind)}`,
    danger: true,
    cta: "Delete",
    items: items.map((i) => (i.ns ? i.ns + "/" : "") + i.name),
    note: ownedPods
      ? "Pods that a controller manages are created again. To remove them for good, scale down or delete the owning workload."
      : "This cannot be undone.",
    word: protectedWord(items.map((i) => i.ns), one ? items[0].name : `delete ${items.length}`),
    run: (dry) => call<string>("Delete", items, dry),
    after: () => {
      const s = ui.get();
      const next = { ...s.checked };
      for (const i of items) delete next[`${kind}/${i.ns}/${i.name}`];
      const gone = s.detail && items.some((i) => i.name === s.detail!.name && i.ns === s.detail!.ns && kind === s.detail!.kind);
      ui.set({ checked: next, detail: gone ? null : s.detail });
      after?.();
    },
  });
}

export function confirmRestart(kind: string, items: Ref[]) {
  ask({
    title: `Restart ${items.length === 1 ? kindSingular(kind).toLowerCase() + "/" + items[0].name : items.length + " " + kindLabel(kind).toLowerCase()}`,
    cta: "Restart",
    items: items.map((i) => i.ns + "/" + i.name),
    note: "A rolling restart patches spec.template.metadata.annotations. Pods are replaced within maxUnavailable and the PodDisruptionBudgets.",
    word: protectedWord(items.map((i) => i.ns), items.length === 1 ? items[0].name : `restart ${items.length}`),
    run: (dry) => call<string>("Restart", items, dry),
    after: () => ui.set({ checked: {} }),
  });
}

export function confirmCordon(node: string, on: boolean) {
  ask({
    title: `${on ? "Cordon" : "Uncordon"} node ${node}`,
    cta: on ? "Cordon" : "Uncordon",
    items: ["node/" + node],
    note: on
      ? "The scheduler stops placing new pods on this node. Running pods stay."
      : "The node accepts new pods again. Pending pods can schedule here.",
    word: protectedWord([""], node),
    run: (dry) => call<string>("Cordon", node, on, dry),
  });
}

export async function runIssueAction(issue: Issue) {
  const a = issue.action;
  if (!a) return;
  const mark = () => ui.set((s) => ({ applied: { ...s.applied, [issue.id]: Date.now() } }));
  try {
    switch (a.type) {
      case "diff": {
        const p = await call<FixProposal>("ProposeFix", a);
        const s = ui.get();
        ui.set({
          view: s.view === "overview" || s.view === "events" || s.view === "clusters" ? "list" : s.view,
          kind: s.view === "overview" || s.view === "events" || s.view === "clusters" ? p.kind : s.kind,
          detail: { kind: p.kind, ns: p.ns, name: p.name }, dtab: "yaml", ymode: "diff", draft: p.draft,
          detailNonce: s.detailNonce + 1,
        });
        pendingFix = { issue: issue.id, kind: p.kind, ns: p.ns, name: p.name };
        break;
      }
      case "uncordon":
        ask({
          title: `Uncordon node ${a.node}`,
          cta: "Uncordon",
          items: ["node/" + a.node],
          note: `The node accepts new pods again. Pending pod ${issue.ns}/${issue.obj} can schedule there.`,
          word: protectedWord([""], a.node ?? ""),
          run: (dry) => call<string>("Cordon", a.node, false, dry),
          after: mark,
        });
        break;
      case "helm-rollback": {
        const [cur, target] = await call<number[]>("HelmRollbackTarget", a.ns, a.name);
        ask({
          title: `Roll back ${a.name} to revision ${target}`,
          cta: "Roll back",
          items: [`helm release ${a.ns}/${a.name}`],
          note: `Creates revision ${cur + 1} from revision ${target}. Chart hooks run.`,
          word: protectedWord([a.ns ?? ""], a.name ?? ""),
          run: (dry) => call<string>("HelmRollback", a.ns, a.name, target, dry),
          after: mark,
        });
        break;
      }
      case "logs":
        openDock("logs", a.ns ?? "", a.name ?? "", a.container, true);
        break;
    }
  } catch (e) {
    fail(e);
  }
}

/** pendingFix links an applied diff to the issue that proposed it. */
export let pendingFix: { issue: string; kind: string; ns: string; name: string } | null = null;

export function notePendingFixApplied(kind: string, ns: string, name: string) {
  if (pendingFix && pendingFix.kind === kind && pendingFix.ns === ns && pendingFix.name === name) {
    const id = pendingFix.issue;
    ui.set((s) => ({ applied: { ...s.applied, [id]: Date.now() } }));
    pendingFix = null;
  }
}

export function toggleTheme() {
  const s = ui.get().settings;
  if (s) void saveSettings({ theme: s.theme === "dark" ? "light" : "dark" });
}

/** setMode switches between the cluster views and the IDE. */
export function setMode(mode: "cluster" | "ide") {
  if (ui.get().settings?.mode === mode) return;
  ui.set({ pal: false });
  void saveSettings({ mode });
}

export function toggleDensity() {
  const s = ui.get().settings;
  if (s) void saveSettings({ density: s.density === "compact" ? "comfortable" : "compact" });
}

export function nsVisible(ns: string | undefined, sel: string[]): boolean {
  return !sel.length || !ns || sel.includes(ns);
}
