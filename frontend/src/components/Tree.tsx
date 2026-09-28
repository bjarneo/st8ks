import { memo } from "react";
import { nav, saveSettings, ui } from "../lib/app";
import { shallowEqual, useStore } from "../lib/store";
import type { Counts, TreeItem } from "../lib/types";

const NONE: Record<string, boolean> = {};

export function sumCounts(counts: Counts, kind: string, nsSel: string[]): [number, number] {
  const m = counts[kind];
  if (!m) return [0, 0];
  let t = 0, b = 0;
  if (!nsSel.length) {
    for (const k in m) {
      t += m[k][0];
      b += m[k][1];
    }
  } else {
    // Cluster-scoped kinds use the empty namespace key.
    if (m[""]) {
      t += m[""][0];
      b += m[""][1];
    }
    for (const ns of nsSel) {
      if (m[ns]) {
        t += m[ns][0];
        b += m[ns][1];
      }
    }
  }
  return [t, b];
}

const Item = memo(function Item({ it, active, count, bad }: { it: TreeItem; active: boolean; count: string; bad: number }) {
  return (
    <div className={"tree-item" + (active ? " on" : "")} title={it.label} onClick={() => nav(it)}>
      <span className="ell" style={{ flex: 1 }}>{it.label}</span>
      {bad > 0 && <span className="bad"><span className="dot" style={{ width: 5, height: 5, background: "var(--er)" }} />{bad}</span>}
      <span className="n">{count}</span>
    </div>
  );
});

export function Tree() {
  const tree = useStore(ui, (s) => s.tree);
  const counts = useStore(ui, (s) => s.counts);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const view = useStore(ui, (s) => s.view);
  const kind = useStore(ui, (s) => s.kind);
  const collapsed = useStore(ui, (s) => s.settings?.collapsed ?? NONE);
  const kinds = useStore(ui, (s) => s.kinds);
  return (
    <div className="tree">
      {tree.map((sec) => {
        const open = !collapsed[sec.id];
        return (
          <div key={sec.id} className="tree-sec">
            <div className="tree-head" onClick={() => void saveSettings({ collapsed: { ...collapsed, [sec.id]: open } })}>
              <span className="chev">{open ? "⌄" : "›"}</span>{sec.label}
            </div>
            {open && sec.items.map((it) => {
              const active = it.view ? view === it.view : view === "list" && kind === it.kind;
              let count = "", bad = 0;
              if (it.kind) {
                const started = it.kind in counts;
                const [t, b] = sumCounts(counts, it.kind, kinds[it.kind]?.namespaced ? nsSel : []);
                count = started ? String(t) : "";
                bad = b;
              } else if (it.view === "events") {
                bad = sumCounts(counts, "Events", nsSel)[1];
              }
              return <Item key={it.view ?? it.kind} it={it} active={active} count={count} bad={bad} />;
            })}
          </div>
        );
      })}
    </div>
  );
}
