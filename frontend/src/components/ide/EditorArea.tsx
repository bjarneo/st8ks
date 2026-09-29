import { useEffect, useMemo, useState } from "react";
import { call, errText } from "../../lib/bridge";
import { kindSingular, openDetail, setMode, ui } from "../../lib/app";
import { useStore } from "../../lib/store";
import {
  baseName, bufs, closeTab, ide, isKust, isLive, isYaml, liveParts, openSourceOf, showTab, textOf, type EdMode,
} from "../../lib/ide";
import type { LiveDiff, Plan } from "../../lib/types";
import { DiffView, diffRows, diffStat } from "../DiffView";
import { IdeEditor } from "./IdeEditor";
import { keyLabel } from "./Ide";

const NONE: string[] = [];

export function EditorArea() {
  const tabs = useStore(ide, (s) => s.tabs);
  const active = useStore(ide, (s) => s.active);
  return (
    <>
      <Tabs />
      {!tabs.length || !active ? <NoTab /> : <Editor id={active} />}
    </>
  );
}

function Tabs() {
  const tabs = useStore(ide, (s) => s.tabs);
  const active = useStore(ide, (s) => s.active);
  const dirty = useStore(ide, (s) => s.dirty);
  const diags = useStore(ide, (s) => s.diags);
  useEffect(() => {
    document.querySelector(".ide-tab.on")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [active]);
  return (
    <div className="ide-tabs">
      {tabs.map((id) => {
        const live = isLive(id);
        const ds = diags[id] ?? [];
        const e = ds.some((d) => d.sev === "error"), w = ds.some((d) => d.sev === "warning");
        const label = live ? liveParts(id)[2] : baseName(id);
        const title = live ? `Live ${kindSingular(liveParts(id)[0])} ${liveParts(id)[1]}/${liveParts(id)[2]}` : id;
        return (
          <div key={id} className={"ide-tab" + (id === active ? " on" : "")} title={title}
            onClick={() => showTab(id)}
            onMouseDown={(ev) => {
              if (ev.button === 1) {
                ev.preventDefault();
                closeTab(id);
              }
            }}>
            {live && <span className="ide-live">LIVE</span>}
            <span style={{ color: e ? "var(--er)" : w ? "var(--wa)" : undefined, fontStyle: live ? "italic" : undefined }}>{label}</span>
            <span className="x" onClick={(ev) => {
              ev.stopPropagation();
              closeTab(id);
            }}>{dirty[id] ? "●" : "✕"}</span>
          </div>
        );
      })}
    </div>
  );
}

function NoTab() {
  const mac = useStore(ui, (s) => s.platform === "darwin");
  return (
    <div className="ide-empty">
      <div className="ide-keys">
        <span>Go to a file</span><span className="k">{keyLabel(mac, "P")}</span>
        <span>Show or hide the panel</span><span className="k">{keyLabel(mac, "J")}</span>
        <span>Quick fix</span><span className="k">{keyLabel(mac, ".")}</span>
        <span>Save</span><span className="k">{keyLabel(mac, "S")}</span>
        <span>Dry-run, apply</span><span className="k">{mac ? "⌘↵, ⌘⇧↵" : "Ctrl ↵, Ctrl Shift ↵"}</span>
      </div>
    </div>
  );
}

function Editor({ id }: { id: string }) {
  const loaded = useStore(ide, (s) => s.loaded[id] ?? 0);
  const mode = useStore(ide, (s) => (isLive(id) ? "src" : s.modes[id] ?? "src"));
  const buf = bufs.get(id);
  const ready = !!buf?.ready;
  const err = buf?.err ?? "";
  void loaded;
  return (
    <>
      <Crumbs id={id} mode={mode} />
      {isLive(id) && <LiveBanner id={id} />}
      {!ready && <div className="ide-empty"><span className="spinner" /></div>}
      {ready && err && <div style={{ padding: 16 }}><div className="errbox">{err}</div></div>}
      {ready && !err && <IdeEditor id={id} hidden={mode !== "src"} />}
      {ready && !err && mode === "git" && <HeadDiff id={id} />}
      {ready && !err && mode === "live" && (isKust(id) ? <PlanDiff id={id} /> : <LiveDiffView id={id} />)}
    </>
  );
}

function Crumbs({ id, mode }: { id: string; mode: EdMode }) {
  const ctx = useStore(ui, (s) => s.cluster?.context ?? "cluster");
  const keys = useStore(ide, (s) => (s.active === id ? s.inspect?.crumbs : undefined)) ?? NONE;
  const segs = isLive(id) ? ["live", ctx, ...liveParts(id)] : id.split("/");
  const all = [...segs.map((t) => ({ t, c: "var(--fa)" })), ...keys.map((t) => ({ t, c: "var(--mu)" }))];
  const canModes = !isLive(id) && isYaml(id);
  const modes: [EdMode, string][] = [["src", "Source"], ["live", `Diff vs ${ctx}`], ["git", "Changes vs HEAD"]];
  return (
    <div className="ide-crumbs">
      <div className="path">
        {all.map((c, i) => (
          <span key={i} style={{ display: "contents" }}>
            <span style={{ color: c.c }}>{c.t}</span>
            {i < all.length - 1 && <span className="sep">{i === segs.length - 1 ? " ›" : "/"}</span>}
          </span>
        ))}
      </div>
      {canModes && (
        <div className="seg">
          {modes.map(([m, l]) => (
            <button key={m} className={mode === m ? "on" : ""} onClick={() => ide.set((s) => ({ modes: { ...s.modes, [id]: m } }))}>{l}</button>
          ))}
        </div>
      )}
    </div>
  );
}

function LiveBanner({ id }: { id: string }) {
  const ctx = useStore(ui, (s) => s.cluster?.context ?? "");
  const [kind, ns, name] = liveParts(id);
  const sing = kindSingular(kind);
  return (
    <div className="ide-banner">
      <span className="grow">Read-only · live {sing} {name} on {ctx}. Change it in Git, so a GitOps sync does not revert it.</span>
      <button className="btn xs" style={{ background: "var(--p1)" }} onClick={() => void openSourceOf(sing, ns, name)}>Open source file</button>
      <button className="btn xs" style={{ background: "var(--p1)" }} onClick={() => {
        setMode("cluster");
        openDetail({ kind, ns, name });
      }}>Open in Cluster</button>
    </div>
  );
}

function DiffHead({ title, rows }: { title: string; rows: ReturnType<typeof diffRows> }) {
  const [add, del] = diffStat(rows).split(" ");
  return (
    <div className="ide-diff-h">
      <span className="grow ell">{title}</span>
      <span className="mono small"><span className="ok">{add}</span> <span className="er">{del}</span></span>
    </div>
  );
}

function useText(id: string, delay: number): string {
  const edit = useStore(ide, (s) => s.edit);
  const [text, setText] = useState(() => textOf(id));
  useEffect(() => {
    const h = setTimeout(() => setText(textOf(id)), delay);
    return () => clearTimeout(h);
  }, [id, edit, delay]);
  return text;
}

function HeadDiff({ id }: { id: string }) {
  const text = useText(id, 150);
  const loaded = useStore(ide, (s) => s.loaded[id] ?? 0);
  const head = bufs.get(id)?.head ?? null;
  const rows = useMemo(() => (head === null ? [] : diffRows(head, text)), [head, text, loaded]); // eslint-disable-line react-hooks/exhaustive-deps
  const changed = rows.some((r) => r.s !== " ");
  return (
    <div className="ide-diff">
      <DiffHead title="HEAD → working copy" rows={rows} />
      {head === null && <div className="empty">This file is not in the last commit. It is new or the folder is not a Git repository.</div>}
      {head !== null && !changed && <div className="empty">No changes since the last commit.</div>}
      {changed && <DiffView rows={rows} />}
    </div>
  );
}

function LiveDiffView({ id }: { id: string }) {
  const text = useText(id, 400);
  const ctx = useStore(ui, (s) => (s.cluster?.status === "Connected" ? s.cluster.context : ""));
  const [live, setLive] = useState<LiveDiff | null>(null);
  const [err, setErr] = useState("");
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const h = setInterval(() => setTick((t) => t + 1), 5000);
    return () => clearInterval(h);
  }, []);
  useEffect(() => {
    let dead = false;
    call<LiveDiff>("IdeLiveText", id, text).then((r) => {
      if (!dead) {
        setLive(r);
        setErr("");
      }
    }, (e) => !dead && setErr(errText(e)));
    return () => {
      dead = true;
    };
  }, [id, text, ctx, tick]);
  const rows = useMemo(() => (live && !live.msg ? diffRows(live.text, text) : []), [live, text]);
  const changed = rows.some((r) => r.s !== " ");
  return (
    <div className="ide-diff">
      <DiffHead title={`Live objects on ${ctx || "the cluster"} → this file. Fields that the file does not set are hidden.`} rows={rows} />
      {err && <div style={{ padding: 14 }}><div className="errbox">{err}</div></div>}
      {!live && !err && <div className="empty flex"><span className="spinner" />Reading the live objects…</div>}
      {live?.msg && <div className="empty">{live.msg}</div>}
      {live && !live.msg && live.found === 0 && <div className="empty">No object of this file is on {ctx}. An apply creates {live.objects === 1 ? "it" : "them"}.</div>}
      {live && !live.msg && live.found > 0 && !changed && <div className="empty">In sync with the live {live.found === 1 ? "object" : "objects"} on {ctx}.</div>}
      {changed && <DiffView rows={rows} />}
    </div>
  );
}

/** PlanDiff shows what an apply of a kustomization changes, from a server
 * dry run, like kubectl diff -k. */
function PlanDiff({ id }: { id: string }) {
  const text = useText(id, 600);
  const ctx = useStore(ui, (s) => (s.cluster?.status === "Connected" ? s.cluster.context : ""));
  const [plan, setPlan] = useState<Plan | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!ctx) return;
    let dead = false;
    setErr("");
    call<Plan>("IdePlan", id, text).then((p) => !dead && setPlan(p), (e) => !dead && setErr(errText(e)));
    return () => {
      dead = true;
    };
  }, [id, text, ctx]);
  const items = useMemo(() => (plan?.items ?? []).map((it) => ({ it, rows: diffRows(it.before, it.after) })), [plan]);
  const all = useMemo(() => items.flatMap((x) => x.rows), [items]);
  return (
    <div className="ide-diff">
      <DiffHead title={`Server dry run of kustomize build on ${ctx || "the cluster"}: live objects → result`} rows={all} />
      {!ctx && <div className="empty">Connect to a cluster to compare the build with the live objects.</div>}
      {err && <div style={{ padding: 14 }}><div className="errbox">{err}</div></div>}
      {ctx && !plan && !err && <div className="empty flex"><span className="spinner" />Running a server dry run…</div>}
      {plan && plan.blocked.length > 0 && <div className="empty">Fix the {plan.blocked.length} errors in the problems panel. Then the diff shows.</div>}
      {items.map(({ it, rows }) => (
        <div key={it.resource + it.ns}>
          <div className="ide-diff-item">
            {it.resource}{it.ns ? ` · ${it.ns}` : ""} · <span style={{ color: it.err && !it.conflict ? "var(--er)" : it.verb === "unchanged" ? "var(--fa)" : "var(--ok)" }}>{it.err && !it.conflict ? "failed" : it.verb}</span>
          </div>
          {it.err && !it.conflict && <div style={{ padding: "0 14px 6px" }}><div className="errbox">{it.err}</div></div>}
          {it.conflict && <div style={{ padding: "0 14px 6px" }}><div className="warnbox">Other field managers own fields that this apply changes. {it.err}</div></div>}
          {(!it.err || it.conflict) && it.verb !== "unchanged" && <DiffView rows={rows} />}
        </div>
      ))}
    </div>
  );
}
