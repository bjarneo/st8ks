import { useEffect, useRef } from "react";
import { Compartment, EditorSelection, EditorState, Facet, Prec, RangeSet, StateEffect, StateField, type Extension, type Range } from "@codemirror/state";
import {
  Decoration, EditorView, GutterMarker, ViewPlugin, WidgetType, drawSelection, gutter, highlightActiveLine, highlightActiveLineGutter,
  keymap, lineNumbers, type DecorationSet, type ViewUpdate,
} from "@codemirror/view";
import { HighlightStyle, bracketMatching, foldGutter, indentOnInput, indentUnit, syntaxHighlighting, syntaxTree } from "@codemirror/language";
import { defaultKeymap, history, historyKeymap, indentLess, indentMore } from "@codemirror/commands";
import { highlightSelectionMatches, search, searchKeymap } from "@codemirror/search";
import { yaml } from "@codemirror/lang-yaml";
import { tags } from "@lezer/highlight";
import { ui } from "../../lib/app";
import { useStore } from "../../lib/store";
import { askApply, bufs, dryRun, fixAtCursor, ide, isLive, isYaml, registerEditor, save, setText } from "../../lib/ide";
import type { Diag } from "../../lib/types";

// ---- Diagnostics ----

const setDiags = StateEffect.define<Diag[]>();
let lensOn = true;

class LensWidget extends WidgetType {
  constructor(readonly msg: string, readonly sev: string) {
    super();
  }
  eq(o: LensWidget) {
    return o.msg === this.msg && o.sev === this.sev;
  }
  toDOM() {
    const s = document.createElement("span");
    s.className = "cm-lens " + this.sev;
    s.textContent = this.msg;
    return s;
  }
  ignoreEvent() {
    return false;
  }
}

class DotMarker extends GutterMarker {
  constructor(readonly sev: string) {
    super();
  }
  eq(o: DotMarker) {
    return o.sev === this.sev;
  }
  toDOM() {
    const s = document.createElement("span");
    s.className = "cm-dg-dot " + this.sev;
    return s;
  }
}
const dots = { error: new DotMarker("error"), warning: new DotMarker("warning"), info: new DotMarker("info") };
const rank = (s: string) => (s === "error" ? 3 : s === "warning" ? 2 : 1);

interface DiagState { deco: DecorationSet; marks: RangeSet<GutterMarker>; list: Diag[] }

function buildDiags(state: EditorState, list: Diag[]): DiagState {
  const doc = state.doc;
  const top = new Map<number, Diag>();
  for (const d of list) {
    const n = Math.min(Math.max(d.line + 1, 1), doc.lines);
    const cur = top.get(n);
    if (!cur || rank(d.sev) > rank(cur.sev)) top.set(n, d);
  }
  const deco: Range<Decoration>[] = [];
  const marks: Range<GutterMarker>[] = [];
  for (const d of list) {
    const n = Math.min(Math.max(d.line + 1, 1), doc.lines);
    const line = doc.line(n);
    if (d.sev !== "info") {
      const from = line.from + Math.min(Math.max(d.col, 0), line.length);
      const to = d.end < 0 ? line.to : line.from + Math.min(d.end, line.length);
      if (to > from) deco.push(Decoration.mark({ class: "cm-u-" + d.sev }).range(from, to));
    }
  }
  for (const [n, d] of top) {
    const line = doc.line(n);
    if (d.sev === "error") deco.push(Decoration.line({ class: "cm-l-er" }).range(line.from));
    if (lensOn) deco.push(Decoration.widget({ widget: new LensWidget(d.msg, d.sev), side: 1 }).range(line.to));
    marks.push(dots[d.sev].range(line.from));
  }
  return { deco: Decoration.set(deco, true), marks: RangeSet.of(marks, true), list };
}

const diagField = StateField.define<DiagState>({
  create: () => ({ deco: Decoration.none, marks: RangeSet.empty, list: [] }),
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setDiags)) return buildDiags(tr.state, e.value);
    if (tr.docChanged) return { deco: v.deco.map(tr.changes), marks: v.marks.map(tr.changes), list: v.list };
    return v;
  },
  provide: (f) => EditorView.decorations.from(f, (v) => v.deco),
});

const diagGutter = gutter({
  class: "cm-dg",
  markers: (v) => v.state.field(diagField).marks,
  initialSpacer: () => dots.info,
});

// ---- Highlighting ----

const highlight = HighlightStyle.define([
  { tag: [tags.definition(tags.propertyName), tags.propertyName], color: "var(--ac)" },
  { tag: [tags.lineComment, tags.comment], color: "var(--fa)" },
  { tag: tags.string, color: "var(--syS)" },
  { tag: tags.special(tags.string), color: "var(--fa)" },
  { tag: tags.content, color: "var(--tx)" },
  { tag: [tags.separator, tags.punctuation, tags.meta, tags.typeName], color: "var(--fa)" },
  { tag: tags.labelName, color: "var(--wa)" },
]);

const numRe = /^(true|false|True|False|TRUE|FALSE|null|~|yes|no|on|off|-?\d+(\.\d+)?(e[+-]?\d+)?%?)$/;

/** literals colors numbers, booleans and Go templates, which the YAML
 * grammar does not tell apart from plain text. */
const literals = ViewPlugin.fromClass(class {
  decorations: DecorationSet;
  constructor(v: EditorView) {
    this.decorations = this.build(v);
  }
  update(u: ViewUpdate) {
    if (u.docChanged || u.viewportChanged || syntaxTree(u.startState) !== syntaxTree(u.state)) this.decorations = this.build(u.view);
  }
  build(v: EditorView) {
    const out: Range<Decoration>[] = [];
    for (const { from, to } of v.visibleRanges) {
      syntaxTree(v.state).iterate({
        from, to,
        enter: (n) => {
          if (n.name !== "Literal" || n.node.parent?.name === "Key") return;
          const t = v.state.doc.sliceString(n.from, n.to);
          if (numRe.test(t)) out.push(Decoration.mark({ class: "cm-yn" }).range(n.from, n.to));
          else if (t.includes("{{")) out.push(Decoration.mark({ class: "cm-yt" }).range(n.from, n.to));
        },
      });
    }
    return Decoration.set(out, true);
  }
}, { decorations: (p) => p.decorations });

const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "calc(12.5px * var(--zf))", color: "var(--tx)", backgroundColor: "var(--bg)" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.6" },
  ".cm-content": { padding: "8px 0 40px", caretColor: "var(--tx)" },
  ".cm-line": { padding: "0 48px 0 14px" },
  ".cm-gutters": { backgroundColor: "var(--bg)", border: "none", borderRight: "1px solid var(--ln)", color: "var(--fa)" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 8px 0 2px", minWidth: "30px", fontSize: "calc(11px * var(--zf))" },
  ".cm-foldGutter .cm-gutterElement": { padding: "0 4px 0 0", opacity: ".6" },
  ".cm-activeLine": { backgroundColor: "var(--p2)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--tx)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--tx)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: "var(--acw) !important" },
  ".cm-panels": { backgroundColor: "var(--p2)", color: "var(--tx)", borderColor: "var(--ln)" },
  ".cm-panels input, .cm-panels button": { fontSize: "calc(12px * var(--zf))" },
  ".cm-searchMatch": { backgroundColor: "var(--waw)", outline: "1px solid var(--wa)" },
  ".cm-selectionMatch": { backgroundColor: "var(--acw)" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--p3)", border: "none", color: "var(--mu)" },
});

// ---- Keys ----

function tabKey(v: EditorView): boolean {
  if (v.state.readOnly) return false;
  if (v.state.selection.ranges.some((r) => !r.empty)) return indentMore(v);
  v.dispatch(v.state.replaceSelection("  "), { scrollIntoView: true, userEvent: "input" });
  return true;
}

const run = (fn: () => unknown) => () => {
  fn();
  return true;
};

const ideKeys = Prec.highest(keymap.of([
  { key: "Mod-s", run: run(() => void save()), preventDefault: true },
  { key: "Mod-.", run: run(fixAtCursor) },
  { key: "Mod-Enter", run: run(() => void dryRun()) },
  { key: "Shift-Mod-Enter", run: run(() => void askApply()) },
  { key: "Mod-j", run: run(() => ide.set((s) => ({ panelOpen: !s.panelOpen }))) },
  { key: "Mod-p", run: run(() => ide.set((s) => ({ pal: !s.pal }))) },
  { key: "Mod-k", run: run(() => ide.set((s) => ({ pal: !s.pal }))) },
  { key: "Tab", run: tabKey, shift: indentLess },
]));

// ---- States ----

const tabId = Facet.define<string, string>({ combine: (v) => v[0] ?? "" });
const roConf = new Compartment();
const states = new Map<string, EditorState>();

let curRaf = 0;
function trackCursor(state: EditorState) {
  cancelAnimationFrame(curRaf);
  curRaf = requestAnimationFrame(() => {
    const head = state.selection.main.head;
    const line = state.doc.lineAt(head);
    const cur = ide.get().cur;
    if (cur.line !== line.number - 1 || cur.col !== head - line.from + 1) ide.set({ cur: { line: line.number - 1, col: head - line.from + 1 } });
  });
}

const listener = EditorView.updateListener.of((u) => {
  const id = u.state.facet(tabId);
  if (u.docChanged) setText(id, u.state.doc.toString());
  if (u.selectionSet || u.docChanged) trackCursor(u.state);
});

const base: Extension[] = [
  diagGutter, lineNumbers(), foldGutter(), highlightActiveLineGutter(), history(), drawSelection(), highlightActiveLine(),
  bracketMatching(), indentOnInput(), syntaxHighlighting(highlight), literals, search({ top: true }), highlightSelectionMatches(),
  EditorState.tabSize.of(2), indentUnit.of("  "), ideKeys,
  keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap]), theme, diagField, listener,
];

function makeState(id: string, text: string): EditorState {
  const ro = isLive(id);
  return EditorState.create({
    doc: text,
    extensions: [base, tabId.of(id), isYaml(id) || isLive(id) ? yaml() : [], roConf.of(EditorState.readOnly.of(ro))],
  });
}

/** minimalChange replaces only the part of a document that differs. */
function minimalChange(cur: string, next: string) {
  let a = 0;
  const max = Math.min(cur.length, next.length);
  while (a < max && cur.charCodeAt(a) === next.charCodeAt(a)) a++;
  let b = 0;
  while (b < max - a && cur.charCodeAt(cur.length - 1 - b) === next.charCodeAt(next.length - 1 - b)) b++;
  return { from: a, to: cur.length - b, insert: next.slice(a, next.length - b) };
}

/** IdeEditor shows the buffer of one tab. One editor view serves all tabs,
 * and each tab keeps its own state with undo history. */
export function IdeEditor({ id, hidden }: { id: string; hidden: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const shown = useRef<string>("");

  useEffect(() => {
    const v = new EditorView({ parent: host.current! });
    view.current = v;
    registerEditor({
      replace(tid, text) {
        if (shown.current !== tid) return false;
        const cur = v.state.doc.toString();
        if (cur !== text) v.dispatch({ changes: minimalChange(cur, text), userEvent: "input.replace" });
        return true;
      },
    });
    return () => {
      if (shown.current) states.set(shown.current, v.state);
      registerEditor(null);
      v.destroy();
      view.current = null;
    };
  }, []);

  // Show the tab, and follow text that changes outside the editor.
  const loaded = useStore(ide, (s) => s.loaded[id] ?? 0);
  useEffect(() => {
    const v = view.current;
    const b = bufs.get(id);
    if (!v || !b?.ready) return;
    if (shown.current !== id) {
      if (shown.current) states.set(shown.current, v.state);
      let st = states.get(id) ?? makeState(id, b.text);
      const cur = st.doc.toString();
      if (cur !== b.text) st = st.update({ changes: minimalChange(cur, b.text) }).state;
      v.setState(st);
      shown.current = id;
      pushDiags(v, id);
      trackCursor(v.state);
      goToPending(v, id);
    } else {
      const cur = v.state.doc.toString();
      if (cur !== b.text) v.dispatch({ changes: minimalChange(cur, b.text) });
    }
  }, [id, loaded]);

  // Diagnostics and the error lens.
  useEffect(() => {
    let last: unknown = null;
    let lastLens = lensOn;
    const sync = () => {
      const v = view.current;
      if (!v || !shown.current) return;
      const d = ide.get().diags[shown.current];
      lensOn = !ui.get().settings?.ideLensOff;
      if (d === last && lensOn === lastLens) return;
      last = d;
      lastLens = lensOn;
      pushDiags(v, shown.current);
    };
    const a = ide.subscribe(sync);
    const b = ui.subscribe(sync);
    return () => {
      a();
      b();
    };
  }, []);

  // Go to a line of the tab that shows.
  useEffect(() => {
    const go = () => {
      if (view.current && shown.current) goToPending(view.current, shown.current);
    };
    go();
    return ide.subscribe(go);
  }, []);

  useEffect(() => {
    if (!hidden) requestAnimationFrame(() => view.current?.requestMeasure());
  }, [hidden]);

  return <div className="ide-cm" ref={host} style={{ display: hidden ? "none" : undefined }} />;
}

function pushDiags(v: EditorView, id: string) {
  v.dispatch({ effects: setDiags.of(ide.get().diags[id] ?? []) });
}

/** goToPending moves the cursor to the line that openFile asked for. */
let goneTo = 0;
function goToPending(v: EditorView, id: string) {
  const g = ide.get().goto;
  if (!g || g.id !== id || goneTo === g.n) return;
  goneTo = g.n;
  const n = Math.min(Math.max(g.line + 1, 1), v.state.doc.lines);
  const line = v.state.doc.line(n);
  const start = line.from + (line.text.length - line.text.trimStart().length);
  v.dispatch({ selection: EditorSelection.range(start, line.to), effects: EditorView.scrollIntoView(start, { y: "center" }) });
  v.focus();
}
