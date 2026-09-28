import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { call, errText } from "../lib/bridge";
import { ask, fail, protectedWord, say, ui } from "../lib/app";
import { rel, toneVar } from "../lib/format";
import { shallowEqual, useStore } from "../lib/store";
import type { HelmDetail, HelmRelease } from "../lib/types";
import { CodeView } from "./CodeView";
import { useNow } from "./VTable";

const GRID = "minmax(160px,1.4fr) 120px minmax(160px,1.4fr) 100px 50px 100px 70px";

export function HelmView() {
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const selKey = useStore(ui, (s) => s.helmSel);
  const [all, setAll] = useState<HelmRelease[] | null>(null);
  const [err, setErr] = useState("");
  const [detail, setDetail] = useState<HelmDetail | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [upRes, setUpRes] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const now = useNow();

  const load = useCallback(() => call<HelmRelease[]>("HelmReleases").then((r) => {
    setAll(r);
    setErr("");
  }, (e) => setErr(errText(e))), []);
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), 10000);
    return () => clearInterval(t);
  }, [load]);

  const rels = useMemo(() => (all ?? []).filter((h) => !nsSel.length || nsSel.includes(h.ns)), [all, nsSel]);
  const sel = rels.find((h) => `${h.ns}/${h.name}` === selKey) ?? rels[0];
  const key = sel ? `${sel.ns}/${sel.name}` : "";
  const keyRef = useRef(key);
  keyRef.current = key;
  const loadDetail = useCallback(() => {
    if (!sel) return setDetail(null);
    const want = key;
    call<HelmDetail>("HelmDetail", sel.ns, sel.name).then((d) => {
      // Drop a response for a release that is no longer selected.
      if (keyRef.current === want) setDetail(d);
    }, fail);
  }, [key, sel?.rev]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setDetail(null);
    setEditing(null);
    setUpRes(null);
  }, [key]);
  useEffect(loadDetail, [loadDetail]);

  const refresh = () => {
    void load();
    loadDetail();
  };
  const upgrade = async (dry: boolean) => {
    if (!sel || editing === null) return;
    setBusy(true);
    setUpRes(null);
    try {
      const msg = await call<string>("HelmUpgradeValues", sel.ns, sel.name, editing, dry);
      if (dry) setUpRes({ ok: true, text: msg });
      else {
        say(msg);
        setEditing(null);
        refresh();
      }
    } catch (e) {
      setUpRes({ ok: false, text: errText(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grow" style={{ display: "flex" }}>
      <div className="col grow">
        <div className="view-head">
          <h1>Helm releases</h1>
          <span className="meta-mono">helm v3 · secrets driver · {rels.length}</span>
          <span style={{ flex: 1 }} />
          <button className="btn" onClick={refresh}>Refresh</button>
        </div>
        {err && <div className="warnbox" style={{ margin: "0 18px 10px" }}>{err}</div>}
        <div className="grow" style={{ overflow: "auto", borderTop: "1px solid var(--ln)" }}>
          <div className="thead" style={{ gridTemplateColumns: GRID, padding: "0 18px", cursor: "default" }}>
            <span>Release</span><span>Namespace</span><span>Chart</span><span>App</span><span>Rev</span><span>Status</span><span>Updated</span>
          </div>
          {all === null && <div className="empty flex"><span className="spinner" />Loading releases…</div>}
          {all !== null && !rels.length && <div className="empty">No Helm releases{nsSel.length ? " in the selected namespaces" : ""}.</div>}
          {rels.map((h) => {
            const on = sel && h.name === sel.name && h.ns === sel.ns;
            return (
              <div key={h.ns + "/" + h.name} className="trow" style={{ position: "static", gridTemplateColumns: GRID, padding: "0 18px", background: on ? "var(--acw)" : undefined }}
                onClick={() => ui.set({ helmSel: `${h.ns}/${h.name}` })}>
                <span className="ell" style={{ fontWeight: 500 }}>{h.name}</span>
                <span className="mono small mu ell">{h.ns}</span>
                <span className="mono small mu ell">{h.chart}</span>
                <span className="mono small mu ell">{h.app}</span>
                <span className="mono small mu">{h.rev}</span>
                <span className="flex" style={{ gap: 6, color: toneVar(h.tone) }}><span className="dot sm" style={{ background: toneVar(h.tone) }} />{h.status}</span>
                <span className="mono small fa">{rel(h.updated, now)}</span>
              </div>
            );
          })}
        </div>
      </div>
      {sel && (
        <div className="side">
          <div className="col" style={{ gap: 4 }}>
            <div className="flex"><span style={{ fontSize: "calc(15px * var(--zf))", fontWeight: 600 }}>{sel.name}</span><span className="small" style={{ color: toneVar(sel.tone) }}>● {sel.status}</span></div>
            <span className="meta-mono">{sel.ns} · {sel.chart} · app {sel.app}</span>
          </div>
          <div className="flex" style={{ gap: 6 }}>
            <button className="btn sm primary" disabled={!detail || detail.release.name !== sel.name || detail.release.ns !== sel.ns} onClick={() => {
              setEditing(detail?.values.startsWith("# No user-supplied") ? "" : detail?.values ?? "");
              setUpRes(null);
            }}>Upgrade…</button>
            <button className="btn sm danger-text" onClick={() => ask({
              title: `Uninstall ${sel.name}`, danger: true, cta: "Uninstall", items: [`helm release ${sel.ns}/${sel.name}`],
              note: "Deletes every resource that this release created. Resources with the helm.sh/resource-policy: keep annotation stay.",
              word: protectedWord([sel.ns], sel.name) ?? sel.name,
              run: (dry) => call<string>("HelmUninstall", sel.ns, sel.name, dry),
              after: refresh,
            })}>Uninstall…</button>
          </div>
          {editing !== null && (
            <div className="col" style={{ gap: 8 }}>
              <span className="sec-label">Values for the upgrade (same chart {sel.chart})</span>
              <div style={{ height: 280, border: "1px solid var(--ln)", borderRadius: "var(--r)", background: "var(--bg)", display: "flex" }}>
                <CodeView value={editing} readOnly={false} autoFocus onChange={setEditing} />
              </div>
              {upRes && <div className={upRes.ok ? "okbox" : "errbox"}>{upRes.text}</div>}
              <div className="flex">
                <button className="btn sm ghost fa" onClick={() => setEditing(null)}>Cancel</button>
                <span style={{ flex: 1 }} />
                <button className="btn sm" disabled={busy} onClick={() => void upgrade(true)}>Dry run</button>
                <button className="btn sm primary" disabled={busy} onClick={() => {
                  const word = protectedWord([sel.ns], sel.name);
                  if (word) {
                    ask({ title: `Upgrade ${sel.name}`, cta: "Upgrade", items: [`helm release ${sel.ns}/${sel.name}`], note: "Applies the edited values with the same chart version.", word, noDry: true, run: () => call<string>("HelmUpgradeValues", sel.ns, sel.name, editing, false), after: () => { setEditing(null); refresh(); } });
                  } else void upgrade(false);
                }}>Upgrade</button>
              </div>
            </div>
          )}
          <div className="col">
            <span className="sec-label" style={{ marginBottom: 8 }}>Revision history</span>
            {!detail && <span className="flex fa"><span className="spinner" />Loading…</span>}
            {detail?.revisions.map((r, i) => (
              <div key={r.rev} className="rev">
                <span className="mono" style={{ fontWeight: 500 }}>{r.rev}</span>
                <div className="col" style={{ gap: 2 }}>
                  <span style={{ color: toneVar(r.tone), fontSize: "calc(12px * var(--zf))" }}>{r.status} · <span className="mu">app {r.app}</span></span>
                  <span className="mu" style={{ fontSize: "calc(12px * var(--zf))", lineHeight: 1.4, wordBreak: "break-word" }}>{r.desc}</span>
                  <span className="mono fa" style={{ fontSize: "calc(10px * var(--zf))" }}>{r.chart} · {rel(r.updated, now)} ago</span>
                </div>
                {i > 0 && (
                  <button className="btn xs" onClick={() => ask({
                    title: `Roll back ${sel.name} to revision ${r.rev}`, cta: "Roll back", items: [`helm release ${sel.ns}/${sel.name}`],
                    note: `Creates revision ${(detail.revisions[0]?.rev ?? 0) + 1} from revision ${r.rev}. Chart hooks run.`,
                    word: protectedWord([sel.ns], sel.name),
                    run: (dry) => call<string>("HelmRollback", sel.ns, sel.name, r.rev, dry),
                    after: refresh,
                  })}>Roll back</button>
                )}
              </div>
            ))}
          </div>
          {detail && (
            <div className="col" style={{ gap: 8 }}>
              <span className="sec-label">Values</span>
              <pre className="pre">{detail.values}</pre>
            </div>
          )}
          {detail?.notes && (
            <details>
              <summary className="sec-label" style={{ cursor: "pointer" }}>Notes</summary>
              <pre className="pre" style={{ marginTop: 8 }}>{detail.notes}</pre>
            </details>
          )}
        </div>
      )}
    </div>
  );
}
