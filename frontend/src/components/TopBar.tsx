import { useMemo, useState } from "react";
import { setMode, setNs, toggleDensity, toggleTheme, ui } from "../lib/app";
import { shallowEqual, useStore } from "../lib/store";
import { ensureLoaded, useTable } from "../lib/tables";

export function Logo() {
  return (
    <svg width="26" height="20" viewBox="0 0 26 20" style={{ flex: "none" }} aria-label="st8ks">
      <path d="M4 5.5C6.5 1.5 14 .8 19 2.2c4.3 1.2 6.3 4.6 5.6 8.3-.7 3.9-4.2 7.4-9.6 8.3-5.6.9-11.2-.6-13.4-3.9C.2 12.3 1.8 8.9 4 5.5Z" fill="oklch(0.62 0.15 28)" />
      <path d="M5.2 6.2C7.4 3 13.6 2.4 18 3.6c3.5 1 5.1 3.7 4.6 6.7-.6 3.2-3.5 6-8 6.8-4.7.7-9.3-.5-11.1-3.2-1.4-2.3-.2-5 1.7-7.7Z" fill="none" stroke="oklch(0.92 0.03 60)" strokeWidth="1.1" opacity=".55" />
      <text x="13.4" y="14.6" textAnchor="middle" fontFamily="Geist Mono Variable, monospace" fontWeight="600" fontSize="11.5" fill="oklch(0.97 0.02 70)">8</text>
    </svg>
  );
}

/** ModeSwitch switches between the cluster views and the IDE. */
export function ModeSwitch({ mode }: { mode: "cluster" | "ide" }) {
  return (
    <div className="seg">
      <button className={mode === "cluster" ? "on" : ""} onClick={() => setMode("cluster")}>Cluster</button>
      <button className={mode === "ide" ? "on" : ""} onClick={() => setMode("ide")}>IDE</button>
    </div>
  );
}

export function statusDot(status: string | undefined): string {
  if (status === "Connected") return "var(--ok)";
  if (status === "Unreachable") return "var(--er)";
  if (status === "Connecting") return "var(--wa)";
  return "var(--fa)";
}

function NsMenu({ close }: { close: () => void }) {
  const t = useTable("Namespaces");
  const sel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const [q, setQ] = useState("");
  const names = useMemo(() => {
    const all = [...t.rows.values()].map((r) => r.m).sort();
    const lq = q.toLowerCase();
    return lq ? all.filter((n) => n.includes(lq)) : all;
  }, [t.version, q]); // eslint-disable-line react-hooks/exhaustive-deps
  const toggle = (n: string) => setNs(sel.includes(n) ? sel.filter((x) => x !== n) : [...sel, n]);
  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="menu">
        <input className="input" style={{ width: "100%", marginBottom: 4 }} autoFocus placeholder="Filter namespaces" value={q} onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && names[0]) toggle(names[0]);
            if (e.key === "Escape") close();
          }} />
        <div className="menu-item" onClick={() => setNs([])}><span className="mark">{sel.length ? "" : "✓"}</span>All namespaces</div>
        <div className="menu-sep" />
        {names.map((n) => (
          <div key={n} className="menu-item mono" style={{ fontSize: "calc(12px * var(--zf))" }} onClick={() => toggle(n)}>
            <span className="mark" style={{ fontFamily: "var(--sans)" }}>{sel.includes(n) ? "✓" : ""}</span>{n}
          </div>
        ))}
        {!names.length && <div className="menu-item fa">{t.state === "ready" ? "No namespaces match." : "Loading…"}</div>}
      </div>
    </>
  );
}

export function TopBar() {
  const cl = useStore(ui, (s) => s.cluster);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const ai = useStore(ui, (s) => s.ai);
  const theme = useStore(ui, (s) => s.settings?.theme);
  const density = useStore(ui, (s) => s.settings?.density);
  const mac = useStore(ui, (s) => s.platform === "darwin");
  const [nsOpen, setNsOpen] = useState(false);
  const nsLabel = !nsSel.length ? "All namespaces" : nsSel.length === 1 ? nsSel[0] : `${nsSel.length} namespaces`;
  return (
    <div className="top" onDoubleClick={(e) => {
      if (e.target === e.currentTarget) window.runtime?.WindowToggleMaximise?.();
    }}>
      <div className="brand"><Logo /><b>st8ks</b></div>
      <button className="btn ctx-btn" title={cl?.server || cl?.context} onClick={() => ui.set({ view: "clusters", detail: null })}>
        <span className="dot" style={{ background: statusDot(cl?.status) }} />
        <span style={{ fontWeight: 500 }} className="ell">{cl?.context ?? "No context"}</span>
        {cl?.version && <span className="meta">{cl.dist} · {cl.version}</span>}
      </button>
      <div style={{ position: "relative" }}>
        <button className="btn" disabled={cl?.status !== "Connected"} onClick={() => {
          void ensureLoaded("Namespaces");
          setNsOpen(!nsOpen);
        }}>
          <span style={{ fontWeight: 500 }}>{nsLabel}</span><span className="fa">⌄</span>
        </button>
        {nsOpen && <NsMenu close={() => setNsOpen(false)} />}
      </div>
      <div style={{ flex: 1, display: "flex", justifyContent: "center", minWidth: 44 }}>
        <button className="btn search-btn" onClick={() => ui.set({ pal: true })}>
          <span className="ell" style={{ flex: 1, textAlign: "left" }}>Search resources or run a command…</span>
          <span className="kbd" style={{ border: "1px solid var(--ln)", padding: "1px 5px" }}>{mac ? "⌘K" : "Ctrl K"}</span>
        </button>
      </div>
      <button className={"btn" + (ai ? " accent" : "")} style={{ color: ai ? "var(--ac)" : "var(--mu)" }} onClick={() => ui.set({ ai: !ai })}>
        <span className="diamond" style={{ background: "currentColor", width: 6, height: 6 }} />Assistant<span className="kbd">A</span>
      </button>
      <div className="vsep" />
      <ModeSwitch mode="cluster" />
      <button className="btn" style={{ color: "var(--mu)" }} title="Toggle density" onClick={toggleDensity}>{density === "compact" ? "Compact" : "Comfortable"}</button>
      <button className="btn icon" style={{ color: "var(--mu)" }} title="Toggle theme" onClick={toggleTheme}>{theme === "dark" ? "☾" : "☀"}</button>
      <button className="btn icon" style={{ color: "var(--mu)" }} title="Settings" onClick={() => ui.set({ settingsOpen: true })}>⚙</button>
    </div>
  );
}
