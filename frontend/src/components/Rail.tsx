import { useMemo } from "react";
import { connect, ui } from "../lib/app";
import { useStore } from "../lib/store";
import { statusDot } from "./TopBar";

export function initials(names: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  const first = (n: string) => (n.replace(/^[^a-z0-9]+/i, "")[0] ?? "?").toUpperCase();
  const counts: Record<string, number> = {};
  for (const n of names) counts[first(n)] = (counts[first(n)] ?? 0) + 1;
  for (const n of names) {
    const f = first(n);
    if (counts[f] === 1) {
      out[n] = f;
      continue;
    }
    const parts = n.split(/[-_.:/@]+/).filter(Boolean);
    out[n] = (parts.length > 1 ? parts[0][0] + parts[1][0] : n.slice(0, 2)).toUpperCase();
  }
  return out;
}

export function Rail() {
  const contexts = useStore(ui, (s) => s.contexts);
  const cur = useStore(ui, (s) => s.cluster?.context);
  const curStatus = useStore(ui, (s) => s.cluster?.status);
  const ini = useMemo(() => initials(contexts.map((c) => c.name)), [contexts]);
  return (
    <div className="rail">
      {contexts.map((c) => {
        const on = c.name === cur;
        return (
          <button key={c.name} className={"rail-btn" + (on ? " on" : "")} title={`${c.name}${c.error ? "\n" + c.error : ""}`}
            onClick={() => {
              if (!on) void connect(c.name);
              else ui.set({ view: "overview", detail: null });
            }}>
            {ini[c.name]}
            <span className="st" style={{ background: statusDot(on ? curStatus : c.status) }} />
          </button>
        );
      })}
      <button className="rail-btn rail-add" title="Add cluster" onClick={() => ui.set({ view: "clusters", detail: null })}>+</button>
    </div>
  );
}
