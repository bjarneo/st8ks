import { useEffect, useMemo, useRef, useState } from "react";
import { changeTextSize, connect, saveSettings, setMode, toggleDensity, toggleTheme, ui } from "../../lib/app";
import { useStore } from "../../lib/store";
import {
  askApply, baseName, closeFolder, closeTab, commit, dirName, dryRun, fixAtCursor, gotoLine, ide, isLive, openFile, openFolder, push,
  refreshWs, reloadClean, revert, save, saveAll,
} from "../../lib/ide";

interface PItem { label: string; sub: string; grp: string; run: () => void }

function score(text: string, q: string): number {
  const l = text.toLowerCase();
  const i = l.indexOf(q);
  if (i === 0) return 0;
  if (i > 0) return 1 + (/[-_ ./]/.test(l[i - 1]) ? 0 : 5) + i / 1000;
  let j = 0, gaps = 0, last = -1;
  for (let k = 0; k < l.length && j < q.length; k++) {
    if (l[k] === q[j]) {
      if (last >= 0 && k - last > 1) gaps++;
      last = k;
      j++;
    }
  }
  return j === q.length ? 20 + gaps : -1;
}

export function IdePalette() {
  const [q, setQ] = useState("");
  const [idx, setIdx] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const files = useStore(ide, (s) => s.ws?.files);
  const diags = useStore(ide, (s) => s.diags);
  const active = useStore(ide, (s) => s.active);
  const contexts = useStore(ui, (s) => s.contexts);
  const cur = useStore(ui, (s) => s.cluster?.context);
  const settings = useStore(ui, (s) => s.settings);
  const close = () => ide.set({ pal: false });

  useEffect(() => {
    input.current?.focus();
  }, []);

  const items = useMemo(() => {
    const out: PItem[] = [];
    for (const f of files ?? []) out.push({ label: baseName(f), sub: f, grp: "File", run: () => void openFile(f) });
    for (const p of Object.keys(diags).sort()) {
      for (const d of diags[p]) {
        out.push({ label: d.msg, sub: `${baseName(p)}:${d.line + 1}`, grp: d.sev === "error" ? "Error" : d.sev === "warning" ? "Warning" : "Info", run: () => void openFile(p, { line: d.line }) });
      }
    }
    const cmd = (label: string, sub: string, run: () => unknown) => out.push({ label, sub, grp: "Command", run: () => void run() });
    if (active && !isLive(active)) {
      cmd("Save file", "Ctrl S", () => save());
      cmd("Revert file", "Discard unsaved changes", () => revert());
      cmd("Quick fix on this line", "Ctrl .", fixAtCursor);
      cmd("Server dry run", "Ctrl Enter", dryRun);
      cmd(`Apply to ${cur ?? "the cluster"}`, "Ctrl Shift Enter", askApply);
      cmd("Diff with the live objects", cur ?? "", () => ide.set((s) => ({ modes: { ...s.modes, [active]: "live" } })));
      cmd("Changes since the last commit", "Git", () => ide.set((s) => ({ modes: { ...s.modes, [active]: "git" } })));
      cmd("Go to the first line", "", () => gotoLine(active, 0));
    }
    if (active) cmd("Close tab", "", () => closeTab(active));
    cmd("Save all files", "", saveAll);
    cmd("Show or hide the panel", "Ctrl J", () => ide.set((s) => ({ panelOpen: !s.panelOpen })));
    cmd("Open terminal", "Shell in the workspace", () => ide.set({ panel: "terminal", panelOpen: true }));
    cmd("Show problems", "", () => ide.set({ panel: "problems", panelOpen: true }));
    cmd("Show output", "", () => ide.set({ panel: "output", panelOpen: true }));
    cmd("Explorer", "Side bar", () => ide.set({ view: "explorer" }));
    cmd("Live objects", "Side bar", () => ide.set({ view: "cluster" }));
    cmd("Source control", "Side bar", () => ide.set({ view: "git" }));
    cmd("Commit all changes", "Git", commit);
    cmd("Push", "Git", push);
    cmd("Reload files and Git status", "", () => refreshWs().then(reloadClean));
    cmd("Open folder…", "Workspace", () => openFolder());
    cmd("Close folder", "Workspace", closeFolder);
    cmd(settings?.ideHideInspector ? "Show the inspector" : "Hide the inspector", "Layout", () => saveSettings({ ideHideInspector: !settings?.ideHideInspector }));
    cmd(settings?.ideLensOff ? "Turn the error lens on" : "Turn the error lens off", "Editor", () => saveSettings({ ideLensOff: !settings?.ideLensOff }));
    cmd("Toggle light / dark theme", "Appearance", toggleTheme);
    cmd("Toggle compact density", "Appearance", toggleDensity);
    cmd("Text size: larger", "Ctrl +", () => changeTextSize(1));
    cmd("Text size: smaller", "Ctrl −", () => changeTextSize(-1));
    cmd("Open the Cluster view", "st8ks", () => setMode("cluster"));
    cmd("Settings", "", () => ui.set({ settingsOpen: true }));
    for (const c of contexts) {
      if (c.name !== cur) out.push({ label: `Switch context: ${c.name}`, sub: [c.dist, c.version].filter(Boolean).join(" ") || c.server, grp: "Context", run: () => void connect(c.name) });
    }
    return out;
  }, [files, diags, active, contexts, cur, settings]);

  const res = useMemo(() => {
    const lq = q.trim().toLowerCase();
    if (!lq) {
      const f = items.filter((i) => i.grp === "File");
      const recent = ide.get().tabs.filter((t) => !isLive(t)).slice(-5).reverse();
      const top = recent.map((t) => f.find((i) => i.sub === t)).filter(Boolean) as PItem[];
      return [...top, ...items.filter((i) => i.grp === "Error" || i.grp === "Warning").slice(0, 4), ...items.filter((i) => i.grp === "Command").slice(0, 6)];
    }
    const scored: [number, PItem][] = [];
    for (const it of items) {
      const s = Math.min(score(it.label, lq), it.grp === "File" ? score(it.sub, lq) + 0.5 : Infinity);
      if (s >= 0 && s !== Infinity) scored.push([s, it]);
    }
    scored.sort((a, b) => a[0] - b[0] || a[1].label.length - b[1].label.length);
    return scored.slice(0, 60).map((x) => x[1]);
  }, [items, q]);

  const sel = Math.min(idx, Math.max(0, res.length - 1));
  useEffect(() => {
    list.current?.querySelector(".pal-item.on")?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const run = (it: PItem | undefined) => {
    if (!it) return;
    close();
    it.run();
  };
  return (
    <>
      <div className="overlay" onClick={close} />
      <div className="palette">
        <input ref={input} value={q} placeholder="Go to a file, a problem or a command…" spellCheck={false}
          onChange={(e) => {
            setQ(e.target.value);
            setIdx(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") close();
            else if (e.key === "ArrowDown") {
              e.preventDefault();
              setIdx(Math.min(sel + 1, res.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setIdx(Math.max(0, sel - 1));
            } else if (e.key === "Enter") {
              e.preventDefault();
              run(res[sel]);
            }
          }} />
        <div className="pal-list" ref={list}>
          {!res.length && <div style={{ padding: "14px 10px" }} className="mu">No matches.</div>}
          {res.map((it, i) => (
            <div key={i} className={"pal-item" + (i === sel ? " on" : "")} onMouseMove={() => i !== sel && setIdx(i)} onClick={() => run(it)}>
              <span className="grp" style={{ color: it.grp === "Error" ? "var(--er)" : it.grp === "Warning" ? "var(--wa)" : undefined }}>{it.grp}</span>
              <span className="ell grow">{it.label}</span>
              <span className="small fa ell" style={{ maxWidth: 240 }}>{it.grp === "File" ? dirName(it.sub) : it.sub}</span>
            </div>
          ))}
        </div>
        <div className="pal-foot"><span>↑↓ navigate</span><span>↵ open</span><span>esc close</span></div>
      </div>
    </>
  );
}
