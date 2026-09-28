import { useEffect, useRef } from "react";
import { Compartment, EditorState } from "@codemirror/state";
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from "@codemirror/view";
import { bracketMatching, foldGutter, HighlightStyle, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { highlightSelectionMatches, search, searchKeymap } from "@codemirror/search";
import { yaml } from "@codemirror/lang-yaml";
import { tags } from "@lezer/highlight";

const theme = EditorView.theme({
  "&": { fontSize: "12px", color: "var(--tx)", backgroundColor: "transparent", height: "100%" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.65" },
  ".cm-content": { padding: "10px 0", caretColor: "var(--ac)" },
  ".cm-gutters": { backgroundColor: "transparent", border: "none", color: "var(--fa)" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 12px 0 10px", opacity: ".6", minWidth: "44px" },
  ".cm-foldGutter .cm-gutterElement": { padding: "0 4px", opacity: ".6" },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklab, var(--p2) 70%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--tx)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--ac)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: "var(--acw) !important" },
  ".cm-panels": { backgroundColor: "var(--p2)", color: "var(--tx)", borderColor: "var(--ln)" },
  ".cm-panels input, .cm-panels button": { fontSize: "12px" },
  ".cm-searchMatch": { backgroundColor: "var(--waw)", outline: "1px solid var(--wa)" },
  ".cm-selectionMatch": { backgroundColor: "var(--acw)" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--p3)", border: "none", color: "var(--mu)" },
});

const highlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: "var(--ac)" },
  { tag: tags.comment, color: "var(--fa)", fontStyle: "italic" },
  { tag: [tags.string, tags.special(tags.string), tags.content], color: "var(--tx)" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "var(--wa)" },
  { tag: [tags.meta, tags.punctuation, tags.separator, tags.typeName, tags.labelName], color: "var(--fa)" },
]);

export interface Props {
  value: string;
  readOnly: boolean;
  onChange?: (v: string) => void;
  autoFocus?: boolean;
}

/** CodeView is a YAML viewer and editor. CodeMirror renders only the visible
 * lines, so large objects stay fast. */
export default function CodeEditor({ value, readOnly, onChange, autoFocus }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const ro = useRef(new Compartment());
  const cb = useRef(onChange);
  cb.current = onChange;

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(), highlightActiveLineGutter(), foldGutter(), history(), drawSelection(), highlightActiveLine(),
          bracketMatching(), indentOnInput(), yaml(), syntaxHighlighting(highlight), search({ top: true }),
          highlightSelectionMatches(), EditorState.tabSize.of(2),
          keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
          theme,
          ro.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) cb.current?.(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    if (autoFocus && !readOnly) v.focus();
    return () => {
      v.destroy();
      view.current = null;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    view.current?.dispatch({ effects: ro.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) });
  }, [readOnly]);

  useEffect(() => {
    const v = view.current;
    if (!v) return;
    const cur = v.state.doc.toString();
    if (cur === value) return;
    // Replace only the changed middle part, so the view keeps its scroll
    // position when a live object updates.
    let a = 0;
    const max = Math.min(cur.length, value.length);
    while (a < max && cur.charCodeAt(a) === value.charCodeAt(a)) a++;
    let b = 0;
    while (b < max - a && cur.charCodeAt(cur.length - 1 - b) === value.charCodeAt(value.length - 1 - b)) b++;
    v.dispatch({ changes: { from: a, to: cur.length - b, insert: value.slice(a, value.length - b) } });
  }, [value]);

  return <div className="cm-host" ref={host} />;
}
