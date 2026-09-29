import { lazy, Suspense, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ui } from "../../lib/app";
import { useStore } from "../../lib/store";
import { baseName, dirName, ide, openFile, quickFix, type PanelTab } from "../../lib/ide";
import type { Diag } from "../../lib/types";
import { keyLabel } from "./Ide";

// xterm.js loads when the terminal opens first.
const IdeTerminal = lazy(() => import("./IdeTerminal"));

const MAX_PROBLEMS = 500;
const sevColor = (s: string) => (s === "error" ? "var(--er)" : s === "warning" ? "var(--wa)" : "var(--ac)");
const toneColor: Record<string, string> = { ok: "var(--ok)", er: "var(--er)", wa: "var(--wa)", fa: "var(--fa)", tx: "var(--tx)" };

function usePanelHeight(): [number, (e: React.MouseEvent) => void] {
  const [h, setH] = useState(() => {
    try {
      return Math.max(120, parseInt(localStorage.getItem("st8ks.ide.panelH") ?? "", 10) || 230);
    } catch {
      return 230;
    }
  });
  const start = (e: React.MouseEvent) => {
    e.preventDefault();
    const y0 = e.clientY, h0 = h;
    const move = (ev: MouseEvent) => setH(Math.max(100, Math.min(window.innerHeight - 220, h0 + (y0 - ev.clientY))));
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      setH((v) => {
        try {
          localStorage.setItem("st8ks.ide.panelH", String(v));
        } catch {
          // Storage is not available.
        }
        return v;
      });
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };
  return [h, start];
}

export function Panel({ visible }: { visible: boolean }) {
  const open = useStore(ide, (s) => s.panelOpen);
  const panel = useStore(ide, (s) => s.panel);
  const count = useStore(ide, (s) => {
    let n = 0;
    for (const p in s.diags) for (const d of s.diags[p]) if (d.sev !== "info") n++;
    return n;
  });
  const mac = useStore(ui, (s) => s.platform === "darwin");
  const [h, startDrag] = usePanelHeight();
  // The terminal stays mounted after it opens once, so the shell keeps running.
  const [termOn, setTermOn] = useState(false);
  useLayoutEffect(() => {
    if (open && panel === "terminal") setTermOn(true);
  }, [open, panel]);
  if (!open && !termOn) return null;
  const tabs: [PanelTab, string, number][] = [["problems", "Problems", count], ["terminal", "Terminal", 0], ["output", "Output", 0]];
  return (
    <div className="ide-panel" style={{ height: h, display: open ? undefined : "none" }}>
      <div className="ide-resize" onMouseDown={startDrag} />
      <div className="ide-ptabs">
        {tabs.map(([id, l, n]) => (
          <button key={id} className={"ide-ptab" + (panel === id ? " on" : "")} onClick={() => ide.set({ panel: id })}>
            {l}{n > 0 && <span className="ide-count">{n}</span>}
          </button>
        ))}
        <span style={{ flex: 1 }} />
        <button className="btn ghost fa" style={{ height: "100%", borderRadius: 0 }} title={`Close (${keyLabel(mac, "J")})`} onClick={() => ide.set({ panelOpen: false })}>✕</button>
      </div>
      {panel === "problems" && <Problems />}
      {panel === "output" && <Output />}
      {termOn && (
        <div className="col" style={{ flex: 1, minHeight: 0, display: panel === "terminal" ? "flex" : "none" }}>
          <Suspense fallback={<div className="empty flex"><span className="spinner" />Starting the terminal…</div>}>
            <IdeTerminal visible={visible && open && panel === "terminal"} />
          </Suspense>
        </div>
      )}
    </div>
  );
}

function Problems() {
  const diags = useStore(ide, (s) => s.diags);
  const mac = useStore(ui, (s) => s.platform === "darwin");
  const groups = useMemo(() => {
    const out: { path: string; items: Diag[] }[] = [];
    for (const p of Object.keys(diags).sort()) if (diags[p].length) out.push({ path: p, items: diags[p] });
    return out;
  }, [diags]);
  let shown = 0;
  const total = groups.reduce((n, g) => n + g.items.length, 0);
  return (
    <div className="ide-pbody" style={{ padding: "4px 0" }}>
      {!groups.length && <div className="empty" style={{ padding: "10px 14px" }}>No problems in the workspace.</div>}
      {groups.map((g) => {
        if (shown >= MAX_PROBLEMS) return null;
        const items = g.items.slice(0, MAX_PROBLEMS - shown);
        shown += items.length;
        return (
          <div key={g.path}>
            <div className="ide-pgrp"><span style={{ fontWeight: 500 }}>{baseName(g.path)}</span><span className="mono fa small">{dirName(g.path)}</span></div>
            {items.map((d, i) => (
              <div key={i} className="ide-prob" onClick={() => void openFile(g.path, { line: d.line })}>
                <span className="dot" style={{ width: 8, height: 8, background: sevColor(d.sev) }} />
                <span className="msg">{d.msg}{d.live && <span className="ide-badge">from cluster</span>}</span>
                <span className="code">{d.code} · Ln {d.line + 1}</span>
                {d.fix && (
                  <button className="btn xs" style={{ color: "var(--ac)", background: "var(--p1)" }} title={`Quick fix (${keyLabel(mac, ".")} on the line)`}
                    onClick={(e) => {
                      e.stopPropagation();
                      void quickFix(g.path, d);
                    }}>{d.fix}</button>
                )}
              </div>
            ))}
          </div>
        );
      })}
      {total > MAX_PROBLEMS && <div className="empty small">The list shows {MAX_PROBLEMS} of {total} problems. Fix the first ones to see more.</div>}
    </div>
  );
}

function Output() {
  const out = useStore(ide, (s) => s.out);
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [out]);
  return (
    <div className="ide-pbody" ref={box}>
      <div className="ide-out">
        {!out.length && <div className="fa">Dry runs, applies, rollouts and Git commands write here.</div>}
        {out.map((l, i) => (
          <div key={i}><span className="ts">{l.ts}</span><span style={{ color: toneColor[l.c ?? ""] ?? "var(--mu)" }}>{l.t}</span></div>
        ))}
      </div>
    </div>
  );
}
