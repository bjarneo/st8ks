// State and actions of the IDE mode. Buffers live outside React state, so a
// keystroke does not render the whole IDE.

import { call, errText, on } from "./bridge";
import { createStore } from "./store";
import { ask, fail, say, ui } from "./app";
import type { Diag, FileData, IdeInit, IdeRun, IdeState, Inspect, ObjectDoc, OutLine, Plan } from "./types";

export type SideView = "explorer" | "cluster" | "git";
export type PanelTab = "problems" | "terminal" | "output";
export type EdMode = "src" | "live" | "git";

export interface Out { ts: string; t: string; c?: string }

export interface IdeS {
  started: boolean;
  ws: IdeState | null;
  recent: string[];
  view: SideView;
  tabs: string[];
  active: string | null;
  modes: Record<string, EdMode>;
  dirty: Record<string, true>;
  /** loaded bumps when a buffer gets text from outside the editor. */
  loaded: Record<string, number>;
  /** edit bumps on every change of any buffer. */
  edit: number;
  openDirs: Record<string, boolean>;
  cur: { line: number; col: number };
  diags: Record<string, Diag[]>;
  panel: PanelTab;
  panelOpen: boolean;
  out: Out[];
  pal: boolean;
  plan: Plan | null;
  planFor: string;
  busy: "" | "dry" | "plan" | "apply" | "commit" | "push" | "open";
  msg: string;
  inspect: Inspect | null;
  liveNs: string;
  goto: { id: string; line: number; n: number } | null;
}

export const ide = createStore<IdeS>({
  started: false, ws: null, recent: [], view: "explorer", tabs: [], active: null, modes: {}, dirty: {}, loaded: {}, edit: 0,
  openDirs: {}, cur: { line: 0, col: 1 }, diags: {}, panel: "problems", panelOpen: true, out: [], pal: false,
  plan: null, planFor: "", busy: "", msg: "", inspect: null, liveNs: "", goto: null,
});

export interface Buf {
  id: string;
  text: string;
  saved: string;
  head: string | null;
  ro: boolean;
  ready: boolean;
  err: string;
}

/** bufs holds the text of every open tab. */
export const bufs = new Map<string, Buf>();

/** EditorHooks lets the actions change the text of an open editor. */
export interface EditorHooks {
  /** replace changes the text of a tab that the editor shows, and reports if it did. */
  replace(id: string, text: string): boolean;
}
let editor: EditorHooks | null = null;
export function registerEditor(h: EditorHooks | null) {
  editor = h;
}

export const isLive = (id: string | null | undefined) => !!id && id.startsWith("live:");
export const isYaml = (p: string) => /\.ya?ml$/i.test(p) || /(^|\/)Kustomization$/.test(p);
export const isKust = (p: string) => /(^|\/)(kustomization\.ya?ml|Kustomization)$/.test(p);
export const baseName = (p: string) => p.slice(p.lastIndexOf("/") + 1);
export const dirName = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");

/** liveParts splits a live tab ID into the table kind, namespace and name. */
export function liveParts(id: string): [string, string, string] {
  const [k, ns, n] = id.slice(5).split("|");
  return [k, ns, n];
}

export function textOf(id: string | null | undefined): string {
  return (id && bufs.get(id)?.text) || "";
}

const now = () => new Date().toTimeString().slice(0, 8);

/** log adds lines to the output panel. */
export function log(lines: (string | OutLine)[], show = true) {
  const add = lines.map((l) => (typeof l === "string" ? { ts: now(), t: l } : { ts: now(), t: l.t, c: l.c }));
  ide.set((s) => ({ out: s.out.concat(add).slice(-500), ...(show ? { panel: "output" as const, panelOpen: true } : {}) }));
}

// ---- Persistence per workspace ----

interface Saved { tabs: string[]; active: string | null; openDirs: Record<string, boolean>; view: SideView; modes: Record<string, EdMode> }

function key(kind: string) {
  return `st8ks.ide.${kind}:${ide.get().ws?.root ?? ""}`;
}

function load<T>(kind: string): T | null {
  try {
    const v = localStorage.getItem(key(kind));
    return v ? (JSON.parse(v) as T) : null;
  } catch {
    return null;
  }
}

function store(kind: string, v: unknown) {
  try {
    localStorage.setItem(key(kind), JSON.stringify(v));
  } catch {
    // Storage is not available. The IDE works without it.
  }
}

let persistTimer = 0;
ide.subscribe(() => {
  const s = ide.get();
  if (!s.ws) return;
  clearTimeout(persistTimer);
  persistTimer = window.setTimeout(() => {
    const t = ide.get();
    if (!t.ws) return;
    store("ui", { tabs: t.tabs.filter((x) => !isLive(x)), active: isLive(t.active) ? null : t.active, openDirs: t.openDirs, view: t.view, modes: t.modes } satisfies Saved);
  }, 400);
});

let unsavedTimer = 0;
function persistUnsaved() {
  clearTimeout(unsavedTimer);
  unsavedTimer = window.setTimeout(() => {
    const out: Record<string, string> = {};
    for (const id of Object.keys(ide.get().dirty)) {
      const b = bufs.get(id);
      if (b && !b.ro) out[id] = b.text;
    }
    store("unsaved", out);
  }, 800);
}

// ---- Start ----

let subscribed = false;

function subscribe() {
  if (subscribed) return;
  subscribed = true;
  on<IdeState>("ide:ws", (st) => {
    if (st.root !== ide.get().ws?.root) return;
    ide.set({ ws: st });
    void reloadClean();
  });
  on<Record<string, Diag[] | null>>("ide:diags", (upd) => {
    ide.set((s) => {
      const d = { ...s.diags };
      for (const p in upd) {
        const v = upd[p];
        if (v) d[p] = v;
        else delete d[p];
      }
      return { diags: d };
    });
  });
  on<OutLine>("ide:log", (l) => log([l], false));
  let ctx = ui.get().cluster?.context;
  let status = ui.get().cluster?.status;
  ui.subscribe(() => {
    const c = ui.get().cluster;
    if (!ide.get().ws) return;
    if (c?.context !== ctx) {
      ctx = c?.context;
      status = c?.status;
      if (ctx) log([`Target context: ${ctx}`], false);
      return;
    }
    if (c?.status !== status) {
      status = c?.status;
      if (status === "Connected") log([`Connected to ${c!.context}${c!.version ? ` (${[c!.dist, c!.version].filter(Boolean).join(" ")})` : ""}`], false);
      if (status === "Unreachable") log([{ t: `${c!.context} is unreachable. ${c!.error ?? ""}`, c: "er" }], false);
    }
  });
  window.addEventListener("focus", () => {
    if (ui.get().settings?.mode === "ide") void reloadClean();
  });
}

function applyWorkspace(st: IdeState, diags: Record<string, Diag[]>) {
  bufs.clear();
  ide.set({ ws: st, diags, tabs: [], active: null, modes: {}, dirty: {}, loaded: {}, openDirs: {}, inspect: null, plan: null });
  const saved = load<Saved>("ui");
  const files = new Set(st.files);
  if (saved) {
    const tabs = saved.tabs.filter((t) => files.has(t));
    ide.set({ tabs, openDirs: saved.openDirs ?? {}, view: saved.view ?? "explorer", modes: saved.modes ?? {} });
    const active = saved.active && tabs.includes(saved.active) ? saved.active : tabs[0] ?? null;
    if (active) void activate(active);
  }
  const unsaved = load<Record<string, string>>("unsaved") ?? {};
  for (const p of Object.keys(unsaved)) {
    if (!files.has(p)) continue;
    void ensureBuf(p).then((b) => {
      if (!b || b.ro || unsaved[p] === b.saved) return;
      replaceText(p, unsaved[p]);
      ide.set((s) => ({ tabs: s.tabs.includes(p) ? s.tabs : [...s.tabs, p] }));
    });
  }
  log([`Opened ${st.root}${st.git ? ` on branch ${st.branch || "(detached)"}` : ""}`, ...(st.truncated ? [{ t: "The workspace has more than 50,000 files. The explorer shows the first 50,000.", c: "wa" }] : [])], false);
}

export async function startIde() {
  subscribe();
  if (ide.get().started) return;
  ide.set({ started: true });
  try {
    const init = await call<IdeInit>("IdeStart");
    ide.set({ recent: init.recent ?? [] });
    if (init.state) applyWorkspace(init.state, init.diags ?? {});
    if (init.err) log([{ t: `The last folder did not open: ${init.err}`, c: "er" }]);
  } catch (e) {
    fail(e);
  }
}

export async function openFolder(path = "") {
  if (Object.keys(ide.get().dirty).length && !(await confirmDiscard("Open another folder"))) return;
  ide.set({ busy: "open" });
  try {
    const st = await call<IdeState | null>("IdeOpen", path);
    if (!st) return;
    const s = ui.get().settings;
    ide.set({ recent: [st.root, ...(ide.get().recent.filter((r) => r !== st.root))].slice(0, 8) });
    if (s) ui.set({ settings: { ...s, ideWorkspace: st.root } });
    applyWorkspace(st, await call<Record<string, Diag[]>>("IdeDiags"));
  } catch (e) {
    fail(e);
  } finally {
    ide.set({ busy: "" });
  }
}

export async function closeFolder() {
  if (Object.keys(ide.get().dirty).length && !(await confirmDiscard("Close the folder"))) return;
  await call("IdeClose");
  bufs.clear();
  ide.set({ ws: null, tabs: [], active: null, dirty: {}, diags: {}, inspect: null, plan: null });
}

export async function forgetRecent(path: string) {
  const r = await call<string[]>("IdeForget", path);
  ide.set({ recent: r ?? [] });
}

/** confirmDiscard asks before unsaved changes are lost. */
function confirmDiscard(title: string): Promise<boolean> {
  const n = Object.keys(ide.get().dirty).length;
  return new Promise((resolve) => {
    let done = false;
    ask({
      title, cta: "Discard changes", danger: true, noDry: true,
      items: Object.keys(ide.get().dirty),
      note: `${n} ${n === 1 ? "file has" : "files have"} unsaved changes. They are lost.`,
      run: async () => {
        done = true;
        store("unsaved", {});
        resolve(true);
        return "Changes discarded";
      },
    });
    const off = ui.subscribe(() => {
      if (!ui.get().cf) {
        off();
        if (!done) resolve(false);
      }
    });
  });
}

// ---- Buffers and tabs ----

const loading = new Map<string, Promise<Buf | null>>();

/** ensureBuf loads the text of a tab once, also when two callers ask at the same time. */
function ensureBuf(id: string): Promise<Buf | null> {
  const have = bufs.get(id);
  if (have?.ready) return Promise.resolve(have);
  let p = loading.get(id);
  if (!p) {
    p = loadBuf(id).finally(() => loading.delete(id));
    loading.set(id, p);
  }
  return p;
}

async function loadBuf(id: string): Promise<Buf | null> {
  const have = bufs.get(id);
  const b: Buf = have ?? { id, text: "", saved: "", head: null, ro: isLive(id), ready: false, err: "" };
  bufs.set(id, b);
  try {
    if (isLive(id)) {
      const [kind, ns, name] = liveParts(id);
      const doc = await call<ObjectDoc>("GetObject", { kind, ns, name });
      b.text = b.saved = doc.yaml;
    } else {
      const f = await call<FileData>("IdeRead", id);
      b.text = b.saved = f.text;
      b.head = f.hasHead ? f.head : null;
    }
    b.ready = true;
    b.err = "";
  } catch (e) {
    b.err = errText(e);
    b.ready = true;
  }
  ide.set((s) => ({ loaded: { ...s.loaded, [id]: (s.loaded[id] ?? 0) + 1 } }));
  return b;
}

/** showTab makes a tab active and loads its text. */
export function showTab(id: string) {
  void activate(id);
}

async function activate(id: string) {
  ide.set((s) => ({ active: id, tabs: s.tabs.includes(id) ? s.tabs : [...s.tabs, id] }));
  await ensureBuf(id);
}

/** revealDirs opens the folders above a file in the explorer. */
function revealDirs(p: string) {
  const parts = p.split("/");
  const open: Record<string, boolean> = {};
  for (let i = 1; i < parts.length; i++) open[parts.slice(0, i).join("/")] = true;
  ide.set((s) => ({ openDirs: { ...s.openDirs, ...open } }));
}

let gotoSeq = 0;

export async function openFile(p: string, opts: { line?: number; mode?: EdMode } = {}) {
  revealDirs(p);
  if (opts.mode) ide.set((s) => ({ modes: { ...s.modes, [p]: opts.mode! } }));
  await activate(p);
  if (opts.line !== undefined) ide.set({ goto: { id: p, line: opts.line, n: ++gotoSeq } });
}

export function gotoLine(id: string, line: number) {
  ide.set((s) => ({ goto: { id, line, n: ++gotoSeq }, modes: { ...s.modes, [id]: "src" } }));
}

/** openLive opens a live object read-only. kind is the table kind, such as Deployments. */
export async function openLive(kind: string, ns: string, name: string) {
  const id = `live:${kind}|${ns}|${name}`;
  bufs.delete(id);
  await activate(id);
}

export function closeTab(id: string) {
  const drop = () => {
    bufs.delete(id);
    ide.set((s) => {
      const i = s.tabs.indexOf(id);
      const tabs = s.tabs.filter((t) => t !== id);
      const dirty = { ...s.dirty };
      delete dirty[id];
      const active = s.active === id ? tabs[Math.min(i, tabs.length - 1)] ?? null : s.active;
      return { tabs, dirty, active };
    });
    persistUnsaved();
    if (!isLive(id)) void call("IdeBuffer", id, "", false);
    const a = ide.get().active;
    if (a) void ensureBuf(a);
  };
  if (!ide.get().dirty[id]) return drop();
  ask({
    title: `Close ${baseName(id)}`, cta: "Discard changes", danger: true, noDry: true, items: [id],
    note: "The file has unsaved changes. They are lost.",
    run: async () => {
      drop();
      return `Closed ${baseName(id)} without saving`;
    },
  });
}

const syncTimers = new Map<string, number>();

/** setText is called by the editor on every change of a buffer. */
export function setText(id: string, text: string) {
  const b = bufs.get(id);
  if (!b || b.ro) return;
  b.text = text;
  const dirty = text !== b.saved;
  ide.set((s) => {
    if (!!s.dirty[id] === dirty) return { edit: s.edit + 1 };
    const d = { ...s.dirty };
    if (dirty) d[id] = true;
    else delete d[id];
    return { dirty: d, edit: s.edit + 1 };
  });
  clearTimeout(syncTimers.get(id));
  syncTimers.set(id, window.setTimeout(() => void call("IdeBuffer", id, b.text, b.text !== b.saved), 150));
  persistUnsaved();
}

/** replaceText sets new text for a buffer. The editor applies it as one
 * change, so undo works. */
function replaceText(id: string, text: string) {
  const b = bufs.get(id);
  if (!b || b.text === text) return;
  setText(id, text);
  if (!editor?.replace(id, text)) ide.set((s) => ({ loaded: { ...s.loaded, [id]: (s.loaded[id] ?? 0) + 1 } }));
}

export async function save(id = ide.get().active) {
  if (!id || isLive(id)) return;
  const b = bufs.get(id);
  if (!b) return;
  const text = b.text;
  try {
    const st = await call<IdeState>("IdeWrite", id, text);
    b.saved = text;
    ide.set((s) => {
      const d = { ...s.dirty };
      if (b.text === b.saved) delete d[id];
      return { dirty: d, ws: st.root ? st : s.ws };
    });
    persistUnsaved();
    say(`Saved ${id}`);
  } catch (e) {
    fail(e);
  }
}

export async function saveAll() {
  for (const id of Object.keys(ide.get().dirty)) await save(id);
}

export function revert(id = ide.get().active) {
  if (!id || isLive(id)) return;
  const b = bufs.get(id);
  if (b) replaceText(id, b.saved);
}

/** reloadClean reads open files again. Files without unsaved changes get
 * the text on disk, and every file gets the text of the last commit. */
export async function reloadClean() {
  for (const id of ide.get().tabs) {
    const b = bufs.get(id);
    if (!b || !b.ready || b.ro) continue;
    try {
      const f = await call<FileData>("IdeRead", id);
      b.head = f.hasHead ? f.head : null;
      if (f.text === b.saved) continue;
      const clean = b.text === b.saved;
      b.saved = f.text;
      if (clean) replaceText(id, f.text);
      else log([{ t: `${id} changed on disk. The editor keeps your unsaved changes.`, c: "wa" }], false);
    } catch {
      // The file was deleted or moved. The tab keeps the text.
    }
  }
  ide.set((s) => ({ loaded: { ...s.loaded } }));
}

// ---- Checks ----

export async function quickFix(p: string, d: Diag) {
  if (!d.fix) return;
  if (!bufs.get(p)?.ready) await openFile(p);
  try {
    const next = await call<string>("IdeQuickFix", p, textOf(p), d.line, d.code);
    replaceText(p, next);
    say(`Quick fix: ${d.fix}`);
  } catch (e) {
    fail(e);
  }
}

export function fixAtCursor() {
  const s = ide.get();
  const p = s.active;
  if (!p || isLive(p)) return;
  const d = (s.diags[p] ?? []).find((x) => x.line === s.cur.line && x.fix);
  if (d) void quickFix(p, d);
  else say("No quick fix on this line");
}

// ---- Cluster actions ----

function manifestTarget(verb: string): string | null {
  const p = ide.get().active;
  if (!p || isLive(p) || !isYaml(p)) {
    say(`${verb} works on YAML manifests. Open one first.`);
    return null;
  }
  if (ui.get().cluster?.status !== "Connected") {
    say("Connect to a cluster first");
    return null;
  }
  return p;
}

export async function dryRun() {
  const p = manifestTarget("A dry run");
  if (!p || ide.get().busy) return;
  ide.set({ busy: "dry" });
  try {
    const r = await call<IdeRun>("IdeDryRun", p, textOf(p));
    log(r.lines);
  } catch (e) {
    log([{ t: `Dry-run failed: ${errText(e)}`, c: "er" }]);
  } finally {
    ide.set({ busy: "" });
  }
}

export async function askApply() {
  const p = manifestTarget("Apply");
  if (!p || ide.get().busy) return;
  ide.set({ busy: "plan" });
  try {
    const plan = await call<Plan>("IdePlan", p, textOf(p));
    if (plan.blocked.length) {
      const n = plan.blocked.length;
      ide.set({ panel: "problems", panelOpen: true });
      say(`Fix ${n} ${n === 1 ? "error" : "errors"} before you apply`);
      return;
    }
    ide.set({ plan, planFor: p });
  } catch (e) {
    log([{ t: `Apply preview failed: ${errText(e)}`, c: "er" }]);
    fail(e);
  } finally {
    ide.set({ busy: "" });
  }
}

export async function runApply(force: boolean) {
  const { planFor: p, plan } = ide.get();
  if (!p || !plan) return;
  ide.set({ busy: "apply" });
  try {
    if (ide.get().dirty[p]) await save(p);
    const r = await call<IdeRun>("IdeApply", p, textOf(p), force);
    log(r.lines);
    ide.set({ plan: null });
    say(r.ok ? `Applied to ${plan.ctx}` : "The apply failed for some objects. See the output panel.");
  } catch (e) {
    log([{ t: `Apply failed: ${errText(e)}`, c: "er" }]);
    fail(e);
  } finally {
    ide.set({ busy: "" });
  }
}

// ---- Git ----

export async function commit() {
  const s = ide.get();
  const n = s.ws?.changes.length ?? 0;
  if (!n) return say("There is nothing to commit");
  if (!s.msg.trim()) return say("Write a commit message first");
  if (s.busy) return;
  ide.set({ busy: "commit" });
  try {
    const r = await call<IdeRun>("IdeCommit", s.msg);
    log(r.lines);
    if (r.ok) {
      ide.set({ msg: "" });
      say(`Committed ${n} ${n === 1 ? "file" : "files"}`);
    } else say("git commit failed. See the output panel.");
  } catch (e) {
    log([{ t: `git commit failed: ${errText(e)}`, c: "er" }]);
    fail(e);
  } finally {
    ide.set({ busy: "" });
    await refreshWs();
  }
}

export async function push() {
  if (ide.get().busy) return;
  ide.set({ busy: "push" });
  try {
    const r = await call<IdeRun>("IdePush");
    log(r.lines);
    say(r.ok ? "Pushed" : "git push failed. See the output panel.");
  } catch (e) {
    log([{ t: errText(e), c: "er" }]);
    fail(e);
  } finally {
    ide.set({ busy: "" });
    await refreshWs();
  }
}

export async function refreshWs() {
  try {
    const st = await call<IdeState>("IdeRefresh");
    if (st.root) ide.set({ ws: st });
  } catch {
    // The next poll updates the state.
  }
}

export async function openSourceOf(kind: string, ns: string, name: string) {
  try {
    const src = await call<{ path: string; line: number }>("IdeFindSource", kind, ns, name);
    await openFile(src.path, { line: src.line });
  } catch (e) {
    fail(e);
  }
}
