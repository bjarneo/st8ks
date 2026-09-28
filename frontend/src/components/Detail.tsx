import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { call, errText } from "../lib/bridge";
import {
  ask, closeDetail, confirmCordon, confirmDelete, confirmRestart, fail, kindInfo, kindSingular, notePendingFixApplied,
  openAI, openDetail, openDock, protectedWord, say, ui, type YMode,
} from "../lib/app";
import { age, rel, toneVar } from "../lib/format";
import { useStore } from "../lib/store";
import { useTable } from "../lib/tables";
import type { ContainerModel, ObjectDoc } from "../lib/types";
import { CodeView } from "./CodeView";
import { DiffView, diffRows, diffStat } from "./DiffView";
import { useNow } from "./VTable";

function Actions({ doc }: { doc: ObjectDoc | null }) {
  const d = useStore(ui, (s) => s.detail)!;
  const info = kindInfo(d.kind);
  const acts = info?.actions ?? [];
  if (d.create || !doc) return null;
  const ref = { kind: d.kind, ns: d.ns, name: d.name };
  const has = (a: string) => acts.includes(a);
  return (
    <>
      {has("logs") && <button className="btn sm" onClick={() => openDock("logs", d.ns, d.name)}>Logs<span className="kbd">L</span></button>}
      {has("shell") && <button className="btn sm" onClick={() => openDock("term", d.ns, d.name)}>Shell<span className="kbd">S</span></button>}
      {has("restart") && <button className="btn sm" onClick={() => confirmRestart(d.kind, [ref])}>Restart…</button>}
      {has("scale") && doc.replicas !== undefined && (
        <button className="btn sm" onClick={() => ask({
          title: `Scale ${kindSingular(d.kind).toLowerCase()}/${d.name}`,
          cta: "Scale", items: [`${d.ns}/${d.name}`],
          note: `The workload has ${doc.replicas} replica(s) now.`,
          input: { label: "Replicas", value: String(doc.replicas), type: "number" },
          word: protectedWord([d.ns], d.name),
          run: (dry, v) => {
            const n = parseInt(v ?? "", 10);
            if (!(n >= 0)) return Promise.reject(new Error("Enter a replica count of 0 or more."));
            return call<string>("Scale", ref, n, dry);
          },
        })}>Scale…</button>
      )}
      {has("cordon") && <button className="btn sm" onClick={() => confirmCordon(d.name, !doc.unschedulable)}>{doc.unschedulable ? "Uncordon…" : "Cordon…"}</button>}
      {has("trigger") && (
        <button className="btn sm" onClick={() => ask({
          title: `Run cronjob/${d.name} now`, cta: "Run now", items: [`${d.ns}/${d.name}`],
          note: "Creates a Job from the CronJob template, like kubectl create job --from=cronjob.",
          run: (dry) => call<string>("TriggerCronJob", d.ns, d.name, dry),
        })}>Run now…</button>
      )}
      {has("suspend") && (
        <button className="btn sm" onClick={() => ask({
          title: `${doc.suspended ? "Resume" : "Suspend"} cronjob/${d.name}`, cta: doc.suspended ? "Resume" : "Suspend", items: [`${d.ns}/${d.name}`],
          note: doc.suspended ? "The CronJob creates Jobs on its schedule again." : "The CronJob stops creating new Jobs. Running Jobs continue.",
          run: (dry) => call<string>("SuspendCronJob", d.ns, d.name, !doc.suspended, dry),
        })}>{doc.suspended ? "Resume…" : "Suspend…"}</button>
      )}
      {has("edit") && <button className="btn sm" onClick={() => ui.set((s) => ({ dtab: "yaml", ymode: "edit", draft: s.draft ?? doc.yaml }))}>Edit</button>}
      {has("delete") && <button className="btn sm danger-text" onClick={() => confirmDelete(d.kind, [ref])}>Delete…</button>}
    </>
  );
}

function PodOverview({ doc }: { doc: ObjectDoc }) {
  const pod = doc.pod!;
  const issues = useStore(ui, (s) => s.issues);
  const forwards = useStore(ui, (s) => s.forwards);
  const ctx = useStore(ui, (s) => s.cluster?.context);
  const now = useNow();
  const issue = issues.find((i) => i.ns === doc.ns && (i.obj === doc.name || i.pods?.includes(doc.name)));
  const meta: [string, string][] = [
    ["Namespace", doc.ns], ["Node", pod.node], ["Pod IP", pod.ip], ["QoS class", pod.qos], ["Service account", pod.sa],
    ["Controlled by", doc.owner ? `${kindSingular(doc.owner.kind)}/${doc.owner.name}` : "—"],
    ["Created", age(doc.created, now) + " ago"], ["Restarts", String(pod.restarts)],
  ];
  const fwd = async (c: ContainerModel, port: number) => {
    const on = forwards.find((f) => f.context === ctx && f.ns === doc.ns && f.pod === doc.name && f.remote === port);
    if (on) return call("StopForward", on.id).catch(fail);
    try {
      const f = await call<{ local: number }>("StartForward", doc.ns, doc.name, port);
      say(`Forwarding localhost:${f.local} → ${doc.name}:${port} (${c.name})`);
    } catch (e) {
      fail(e);
    }
  };
  return (
    <>
      {issue && (
        <div className="banner" style={{ background: issue.sev === "er" ? "var(--erw)" : "var(--waw)" }}>
          <span>{issue.reason} — {issue.summary}</span>
          <div className="flex" style={{ gap: 6 }}>
            <button className="btn sm primary" onClick={() => openAI(issue.id, true)}>Explain &amp; suggest fix</button>
            <button className="btn sm" style={{ background: "var(--p1)" }} onClick={() => openDock("logs", doc.ns, doc.name, undefined, pod.restarts > 0)}>View logs</button>
          </div>
        </div>
      )}
      <div className="kv">{meta.map(([k, v]) => [<span key={k}>{k}</span>, <span key={k + "v"} title={v}>{v}</span>])}</div>
      {doc.labels.length > 0 && <div className="flex" style={{ flexWrap: "wrap", gap: 4 }}>{doc.labels.map((l) => <span key={l} className="tag">{l}</span>)}</div>}
      <div className="col" style={{ gap: 6 }}>
        <div className="sec-label">Conditions</div>
        <div className="conds">{pod.conds.map((c) => <span key={c.k} className="cond">{c.k}<span className="mono small" style={{ color: toneVar(c.t) }}>{c.v}</span></span>)}</div>
      </div>
      <div className="col" style={{ gap: 8 }}>
        <div className="sec-label">Containers</div>
        {pod.containers.map((c) => (
          <div key={c.type + c.name} className="ctr">
            <div className="flex">
              <span style={{ fontWeight: 600 }}>{c.name}</span>
              {c.type && <span className="chip">{c.type}</span>}
              <span style={{ flex: 1 }} />
              <button className="btn xs" onClick={() => openDock("logs", doc.ns, doc.name, c.name)}>Logs</button>
              {c.type !== "init" && <button className="btn xs" onClick={() => openDock("term", doc.ns, doc.name, c.name)}>Shell</button>}
              {c.ports.map((p) => {
                const on = forwards.some((f) => f.context === ctx && f.ns === doc.ns && f.pod === doc.name && f.remote === p.port);
                return (
                  <button key={p.port} className="btn xs" style={{ color: on ? "var(--ac)" : undefined }} onClick={() => void fwd(c, p.port)}>
                    {on ? `Stop :${p.port}` : `Forward :${p.port}`}
                  </button>
                );
              })}
            </div>
            <div className="kv">
              <span>Image</span><span style={{ wordBreak: "break-all" }}>{c.image}</span>
              <span>State</span><span style={{ color: toneVar(c.stateTone), fontFamily: "var(--sans)" }}>{c.state}</span>
              {c.last && <><span>Last state</span><span style={{ color: "var(--er)", fontFamily: "var(--sans)" }}>{c.last}</span></>}
              <span>Resources</span><span>{c.resources}</span>
              <span>Ports</span><span>{c.ports.length ? c.ports.map((p) => `${p.port}/${p.proto}${p.name ? " (" + p.name + ")" : ""}`).join(", ") : "—"}</span>
              <span>Probes</span><span>{c.probes}</span>
              <span>Mounts</span><span>{c.mounts}</span>
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

function GenericOverview({ doc }: { doc: ObjectDoc }) {
  const now = useNow();
  const [allAnn, setAllAnn] = useState(false);
  return (
    <>
      <div className="kv wide">
        {doc.summary.map((m) => {
          const v = m.v.startsWith("@") ? rel(parseInt(m.v.slice(1), 10), now) : m.v;
          return [<span key={m.k}>{m.k}</span>, <span key={m.k + "v"} style={{ color: m.t && m.t !== "-" ? toneVar(m.t) : "var(--tx)" }}>{v || "—"}</span>];
        })}
        <span>Created</span><span>{age(doc.created, now)} ago</span>
        {doc.owner && <><span>Controlled by</span><a href="#" onClick={(e) => { e.preventDefault(); openDetail(doc.owner!); }}>{kindSingular(doc.owner.kind)}/{doc.owner.name}</a></>}
        {doc.helm && <><span>Helm release</span><a href="#" onClick={(e) => { e.preventDefault(); ui.set({ view: "helm", helmSel: `${doc.helm!.ns}/${doc.helm!.name}`, detail: null }); }}>{doc.helm.ns}/{doc.helm.name}</a></>}
      </div>
      {doc.labels.length > 0 && (
        <div className="col" style={{ gap: 6 }}>
          <div className="sec-label">Labels</div>
          <div className="flex" style={{ flexWrap: "wrap", gap: 4 }}>{doc.labels.map((l) => <span key={l} className="tag">{l}</span>)}</div>
        </div>
      )}
      {doc.annotations.length > 0 && (
        <div className="col" style={{ gap: 6 }}>
          <div className="sec-label">Annotations</div>
          <div className="kv wide" style={{ gridTemplateColumns: "minmax(140px, 1fr) 2fr" }}>
            {(allAnn ? doc.annotations : doc.annotations.slice(0, 8)).map((a) => [
              <span key={a.k} className="mono small ell" title={a.k}>{a.k}</span>,
              <span key={a.k + "v"} className="small" title={a.v} style={{ whiteSpace: allAnn ? "pre-wrap" : "nowrap" }}>{a.v.length > 300 && !allAnn ? a.v.slice(0, 300) + "…" : a.v}</span>,
            ])}
          </div>
          {doc.annotations.length > 8 && <button className="btn xs" style={{ alignSelf: "flex-start" }} onClick={() => setAllAnn(!allAnn)}>{allAnn ? "Show fewer" : `Show all ${doc.annotations.length}`}</button>}
        </div>
      )}
    </>
  );
}

function YamlTab({ doc, reload }: { doc: ObjectDoc | null; reload: () => void }) {
  const d = useStore(ui, (s) => s.detail)!;
  const ymode = useStore(ui, (s) => s.ymode);
  const draftState = useStore(ui, (s) => s.draft);
  const [dry, setDry] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const live = doc?.yaml ?? "";
  const draft = draftState ?? live;
  const changed = !!d.create || draft !== live;
  const rows = useMemo(() => (ymode === "diff" ? diffRows(live, draft) : []), [ymode, live, draft]);
  const modes: [YMode, string][] = d.create ? [["edit", "Edit"]] : [["live", "Live"], ["edit", "Edit"], ["diff", "Diff"]];
  useEffect(() => setDry(null), [draftState]);

  const run = async (isDry: boolean) => {
    setBusy(true);
    try {
      const msg = d.create ? await call<string>("Create", draft, d.ns || "default", isDry) : await call<string>("Apply", draft, isDry);
      if (isDry) {
        setDry({ ok: true, text: msg + " · schema valid · admission webhooks passed" });
      } else {
        say(msg);
        if (d.create) closeDetail();
        else {
          notePendingFixApplied(d.kind, d.ns, d.name);
          ui.set({ ymode: "live", draft: null });
          reload();
        }
      }
    } catch (e) {
      setDry({ ok: false, text: errText(e) });
    } finally {
      setBusy(false);
    }
  };

  const managed = doc?.pod && doc.owner && doc.owner.kind !== "Jobs" && !d.create;
  return (
    <div className="col" style={{ minHeight: "100%" }}>
      <div className="ybar">
        <div className="seg">
          {modes.map(([m, l]) => (
            <button key={m} className={ymode === m ? "on" : ""} onClick={() => ui.set((s) => ({ ymode: m, draft: m === "live" ? s.draft : s.draft ?? live }))}>{l}</button>
          ))}
        </div>
        {ymode === "diff" && <span className="mono small mu">{diffStat(rows)}</span>}
        <span style={{ flex: 1 }} />
        {changed && (
          <>
            {!d.create && <button className="btn sm ghost fa" onClick={() => ui.set({ draft: null, ymode: "live" })}>Reset</button>}
            <button className="btn sm" disabled={busy} onClick={() => void run(true)}>Server dry-run</button>
            <button className="btn sm primary" disabled={busy} onClick={() => void run(false)}>{d.create ? "Create" : "Apply"}</button>
          </>
        )}
      </div>
      {managed && (
        <div className="warnbox flex" style={{ margin: "12px 18px 0" }}>
          <span className="grow" style={{ whiteSpace: "normal" }}>This Pod is managed by {kindSingular(doc!.owner!.kind)} {doc!.owner!.name}. Edits here are limited to mutable fields and are lost when the Pod is replaced.</span>
          <button className="btn xs" style={{ background: "var(--p1)" }} onClick={() => openDetail(doc!.managed ?? doc!.owner!, { dtab: "yaml", ymode: "edit" })}>Edit owner</button>
        </div>
      )}
      {dry && <div className={dry.ok ? "okbox" : "errbox"} style={{ margin: "12px 18px 0" }}>{dry.ok ? "✓ " : ""}{dry.text}</div>}
      {ymode === "live" && <CodeView value={live} readOnly />}
      {ymode === "edit" && <CodeView value={draft} readOnly={false} autoFocus onChange={(v) => ui.set({ draft: v })} />}
      {ymode === "diff" && (
        <>
          {!changed && <div className="empty">No changes against the live object. Switch to Edit to change it.</div>}
          {changed && <DiffView rows={rows} />}
        </>
      )}
    </div>
  );
}

export function Detail() {
  const d = useStore(ui, (s) => s.detail)!;
  const layout = useStore(ui, (s) => s.settings?.detailLayout ?? "drawer");
  const dtab = useStore(ui, (s) => s.dtab);
  const nonce = useStore(ui, (s) => s.detailNonce);
  const [doc, setDoc] = useState<ObjectDoc | null>(null);
  const [err, setErr] = useState("");
  const [gone, setGone] = useState(false);
  const seq = useRef(0);
  const docRef = useRef<ObjectDoc | null>(null);
  docRef.current = doc;

  const load = useCallback(async () => {
    if (d.create) return;
    const my = ++seq.current;
    try {
      const o = await call<ObjectDoc>("GetObject", { kind: d.kind, ns: d.ns, name: d.name });
      if (my !== seq.current) return;
      setDoc(o);
      setErr("");
      setGone(false);
    } catch (e) {
      if (my !== seq.current) return;
      const msg = errText(e);
      // "deleted" applies only to an object that the panel showed before.
      if (/not found/i.test(msg) && docRef.current) setGone(true);
      else setErr(msg);
    }
  }, [d.kind, d.ns, d.name, d.create]);

  useEffect(() => {
    setDoc(null);
    setErr("");
    setGone(false);
    void load();
  }, [load, nonce]);

  // Reload when the row of this object changes in the live table.
  const t = useTable(d.kind);
  const row = doc ? t.rows.get(doc.uid) : undefined;
  const prevRow = useRef(row);
  useEffect(() => {
    const prev = prevRow.current;
    prevRow.current = row;
    if (!doc || prev === row || !prev) return;
    // A row that disappears can be an object that was created again with
    // the same name, such as a StatefulSet pod. The reload finds the new
    // object, or marks the panel as deleted.
    const h = setTimeout(() => void load(), row ? 400 : 800);
    return () => clearTimeout(h);
  }, [row]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (dtab === "events" || dtab === "related") void load();
  }, [dtab]); // eslint-disable-line react-hooks/exhaustive-deps

  const now = useNow();
  const tabs: [typeof dtab, string, string][] = d.create ? [["yaml", "YAML", ""]] : [["overview", "Overview", "o"], ["yaml", "YAML", "y"], ["events", "Events", "e"], ["related", "Related", "r"]];
  const status = doc?.status;
  return (
    <div className={"detail " + layout}>
      <div className="dhead">
        <div className="flex">
          {layout === "page" && <button className="btn xs" onClick={closeDetail}>← Back</button>}
          <span className="kindtag">{kindSingular(d.kind)}</span>
          <span className="meta-mono">{d.ns || "cluster"}</span>
          <span style={{ flex: 1 }} />
          <Actions doc={doc} />
          <button className="btn icon sm ghost fa" title="Close (Esc)" onClick={closeDetail}>✕</button>
        </div>
        <div className="flex" style={{ gap: 10 }}>
          <h2>{d.create ? `New ${kindSingular(d.kind)}` : d.name}</h2>
          {status && !d.create && (
            <span className="flex small" style={{ gap: 6, color: toneVar(doc?.tone), whiteSpace: "nowrap" }}>
              <span className="dot" style={{ background: toneVar(doc?.tone) }} />{status}
            </span>
          )}
          {gone && <span className="chip" style={{ color: "var(--er)" }}>deleted</span>}
        </div>
        <div className="tabs">
          {tabs.map(([id, label, kbd]) => (
            <button key={id} className={"tab" + (dtab === id ? " on" : "")} onClick={() => ui.set({ dtab: id })}>
              {label}{id === "events" && doc?.events.length ? <span className="mono small fa">{doc.events.length}</span> : null}<span className="kbd">{kbd}</span>
            </button>
          ))}
        </div>
      </div>
      <div className="dbody">
        {err && <div className="errbox" style={{ margin: 18 }}>{err}</div>}
        {!doc && !err && !d.create && <div className="empty flex"><span className="spinner" />Loading…</div>}
        {dtab === "overview" && doc && (
          <div className="dpad">{doc.pod ? <PodOverview doc={doc} /> : <GenericOverview doc={doc} />}</div>
        )}
        {dtab === "yaml" && (doc || d.create) && <YamlTab doc={doc} reload={() => void load()} />}
        {dtab === "events" && doc && (
          <div style={{ padding: "8px 0" }}>
            {!doc.events.length && <div className="empty">No events. The API server keeps events for one hour by default.</div>}
            {doc.events.map((e, i) => (
              <div key={i} className="ev-item">
                <div className="flex" style={{ alignItems: "baseline" }}>
                  <span style={{ color: e.type === "Warning" ? "var(--wa)" : "var(--fa)", fontWeight: 500 }}>{e.reason}</span>
                  <span className="meta-mono">×{e.count} · {age(e.last, now)} ago</span>
                </div>
                <span className="mu" style={{ lineHeight: 1.45, wordBreak: "break-word" }}>{e.msg}</span>
              </div>
            ))}
          </div>
        )}
        {dtab === "related" && doc && (
          <div style={{ padding: "8px 0" }}>
            {!doc.related.length && <div className="empty">No owner, selector or reference relationships found.</div>}
            {doc.related.map((r, i) => {
              const known = !!kindInfo(r.kind);
              return (
                <div key={i} className="rel-row" style={{ cursor: known ? "pointer" : "default" }} onClick={() => known && openDetail({ kind: r.kind, ns: r.ns, name: r.name })}>
                  <span className="fa">{r.rel}</span>
                  <span className="mono small mu">{kindSingular(r.kind)}</span>
                  <span className="ell" style={{ color: known ? "var(--ac)" : "var(--mu)" }}>{r.ns && r.ns !== d.ns ? r.ns + "/" : ""}{r.name}</span>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
