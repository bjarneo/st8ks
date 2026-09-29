import { useMemo, useState } from "react";
import { protectedWord } from "../../lib/app";
import { useStore } from "../../lib/store";
import { ide, runApply } from "../../lib/ide";
import { diffRows } from "../DiffView";

interface DLine { t: string; c: string }

/** ApplyDialog confirms an apply. It shows the changes from a server dry
 * run, the notes about GitOps and overlays, and field manager conflicts. */
export function ApplyDialog() {
  const plan = useStore(ide, (s) => s.plan)!;
  const busy = useStore(ide, (s) => s.busy === "apply");
  const [typed, setTyped] = useState("");
  const [force, setForce] = useState(false);
  const word = protectedWord(plan.ns, plan.ctx) ? plan.ctx : null;
  const conflicts = plan.items.filter((i) => i.conflict);
  const failed = plan.items.filter((i) => i.err && !i.conflict);
  const { lines, adds, dels, changed } = useMemo(() => {
    const out: DLine[] = [];
    let a = 0, d = 0, n = 0;
    for (const it of plan.items) {
      if (it.err && !it.conflict) continue;
      const rows = diffRows(it.before, it.after).filter((r) => r.s !== " ");
      if (!rows.length) continue;
      n++;
      out.push({ t: `@@ ${it.resource}${it.ns ? " · " + it.ns : ""} · ${it.verb}`, c: "hd" });
      for (const r of rows.slice(0, 400)) {
        if (r.s === "+") a++;
        else d++;
        out.push({ t: `${r.s} ${r.l}`, c: r.s === "+" ? "add" : "del" });
      }
      if (rows.length > 400) out.push({ t: `… ${rows.length - 400} more lines`, c: "fa" });
    }
    return { lines: out, adds: a, dels: d, changed: n };
  }, [plan]);
  const ok = (!word || typed === word) && (!conflicts.length || force) && !busy;
  const close = () => ide.set({ plan: null });
  const n = plan.items.length;
  return (
    <>
      <div className="modal-scrim" onClick={close} />
      <div className="modal wide" style={{ width: 620, gap: 12 }} onKeyDown={(e) => {
        if (e.key === "Enter" && (e.target as HTMLElement).tagName === "INPUT" && ok) void runApply(force);
      }}>
        <div style={{ fontSize: "calc(14px * var(--zf))", fontWeight: 600 }}>{plan.title}</div>
        <div className="mono small mu">
          {n} {n === 1 ? "object" : "objects"} · {changed} changed · <span className="ok">+{adds}</span> <span className="er">−{dels}</span>
        </div>
        <div className="ide-cf-diff">
          {lines.length ? lines.map((l, i) => <div key={i} className={l.c}>{l.t}</div>) : <div className="fa">No differences from the live objects. An apply takes ownership of the fields for the st8ks field manager.</div>}
        </div>
        {failed.map((f) => <div key={f.resource} className="errbox">{f.resource}: {f.err}</div>)}
        {plan.notes.map((t) => <div key={t} className="warnbox">{t}</div>)}
        {conflicts.length > 0 && (
          <div className="warnbox col" style={{ gap: 8 }}>
            <span>Other field managers own fields that this apply changes: {conflicts.map((c) => c.resource).join(", ")}.</span>
            {conflicts.map((c) => <span key={c.resource} className="mono small" style={{ whiteSpace: "pre-wrap" }}>{c.err}</span>)}
            <div className="flex noselect" style={{ cursor: "pointer" }} onClick={() => setForce(!force)}>
              <span className={"chk" + (force ? " on" : "")}>{force ? "✓" : ""}</span>Take over these fields (--force-conflicts)
            </div>
          </div>
        )}
        {word && (
          <label className="col" style={{ gap: 6, fontSize: "calc(12px * var(--zf))" }}>
            <span className="mu">This targets a protected context or namespace. Type <b className="mono" style={{ color: "var(--tx)" }}>{word}</b> to confirm.</span>
            <input className="input mono" autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} spellCheck={false} />
          </label>
        )}
        <div className="flex">
          <span className="grow small fa">Server-side apply · field manager st8ks{failed.length ? ` · ${failed.length} ${failed.length === 1 ? "object fails" : "objects fail"} the dry run` : ""}</span>
          <button className="btn" style={{ height: 30, padding: "0 14px" }} onClick={close}>Cancel</button>
          <button className="btn primary" style={{ height: 30, padding: "0 14px", opacity: ok ? 1 : 0.4 }} disabled={!ok} onClick={() => void runApply(force)}>
            {busy && <span className="spinner" />}Apply
          </button>
        </div>
      </div>
    </>
  );
}
