import { useEffect, useState } from "react";
import { call } from "../lib/bridge";
import { connect, fail, say, ui } from "../lib/app";
import { useStore } from "../lib/store";
import { sumCounts } from "./Tree";
import { statusDot } from "./TopBar";

const EMPTY: never[] = [];

const KIND_LABEL: Record<string, string> = {
  flag: "--kubeconfig", env: "KUBECONFIG", default: "default", added: "added file", folder: "added folder", "in folder": "in folder", scan: "found in ~/.kube",
};

function shortPath(p: string): string {
  const home = p.match(/^(\/home\/[^/]+|\/Users\/[^/]+)/);
  return home ? "~" + p.slice(home[1].length) : p;
}

export function ClustersView() {
  const contexts = useStore(ui, (s) => s.contexts);
  const sources = useStore(ui, (s) => s.sources ?? EMPTY);
  const explicit = useStore(ui, (s) => s.explicit);
  const scan = useStore(ui, (s) => s.settings?.scanKubeDir ?? true);
  const cur = useStore(ui, (s) => s.cluster);
  const counts = useStore(ui, (s) => s.counts);
  const [path, setPath] = useState("");

  useEffect(() => {
    void call("ProbeContexts");
  }, []);

  const files = sources.filter((s) => s.kind !== "folder").length;
  const added = (m: string) => m && say(m);
  return (
    <div className="page" style={{ gap: 16 }}>
      <div className="page-head" style={{ flexWrap: "wrap" }}>
        <h1>Clusters</h1>
        <span className="meta-mono">{files} kubeconfig file{files === 1 ? "" : "s"} · {contexts.length} context{contexts.length === 1 ? "" : "s"}</span>
        <span style={{ flex: 1 }} />
        <button className="btn" onClick={() => void call("ProbeContexts")}>Check all</button>
        <button className="btn" disabled={explicit} onClick={() => call<string>("ImportKubeconfig").then(added, fail)}>Add file…</button>
        <button className="btn" disabled={explicit} onClick={() => call<string>("ImportKubeconfigFolder").then(added, fail)}>Add folder…</button>
      </div>
      {!contexts.length && <div className="dashed">No contexts found. Add a kubeconfig file or folder below.</div>}
      <div className="cl-grid">
        {contexts.map((c) => {
          const isCur = c.name === cur?.context;
          const status = isCur ? cur!.status : c.status;
          const nodes = isCur ? sumCounts(counts, "Nodes", [])[0] : c.nodes;
          const pods = isCur ? sumCounts(counts, "Pods", [])[0] : c.pods;
          return (
            <div key={c.name} className="card" style={{ borderColor: isCur ? "var(--ac)" : undefined }}>
              <div className="flex">
                <span className="dot lg" style={{ background: statusDot(status) }} />
                <span className="ell grow" style={{ fontWeight: 600, fontSize: 14 }} title={c.name}>{c.name}</span>
                <span className="small" style={{ color: statusDot(status) }}>{status}</span>
              </div>
              <div style={{ display: "grid", gridTemplateColumns: "auto 1fr", gap: "4px 14px", fontSize: 12 }}>
                <span className="fa">Distribution</span><span>{c.dist || "—"} {c.version}</span>
                <span className="fa">Region</span><span>{c.region || "—"}</span>
                <span className="fa">Nodes · Pods</span><span className="mono">{nodes || pods ? `${nodes} · ${pods}` : "—"}</span>
                <span className="fa">Namespace</span><span className="mono">{c.namespace || "default"}</span>
                <span className="fa">User</span><span className="mono ell" title={c.user}>{c.user}</span>
              </div>
              <code className="mono fa ell" style={{ fontSize: 10.5 }} title={c.server}>{c.server}</code>
              <code className="mono fa ell" style={{ fontSize: 10.5 }} title={c.source}>{shortPath(c.source)}</code>
              {c.error && status === "Unreachable" && <div className="errbox" style={{ maxHeight: 80, overflow: "auto" }}>{c.error}</div>}
              {isCur ? (
                <div className="ac" style={{ height: 26, display: "grid", placeItems: "center", fontSize: 12 }}>Current context</div>
              ) : (
                <button className="btn sm" onClick={() => void connect(c.name)}>Switch to this context</button>
              )}
            </div>
          );
        })}
      </div>

      <div className="col" style={{ gap: 10 }}>
        <div className="flex">
          <h2 style={{ fontSize: 13, fontWeight: 600 }}>Kubeconfig sources</h2>
          <span style={{ flex: 1 }} />
          <button className="btn sm" onClick={() => call("ReloadKubeconfigs").then(() => say("Kubeconfig files reloaded"), fail)}>Reload</button>
        </div>
        {explicit && (
          <div className="warnbox">st8ks started with --kubeconfig, so it reads only those files, as kubectl does. Start it without the flag to use KUBECONFIG, ~/.kube and added files.</div>
        )}
        <div className="box">
          {sources.map((s) => (
            <div key={s.path} className="issue-row" style={{ padding: "8px 14px", paddingLeft: s.kind === "in folder" ? 34 : 14 }}>
              <span className="dot sm" style={{ background: s.err ? "var(--er)" : s.contexts ? "var(--ok)" : "var(--fa)" }} />
              <div className="col grow" style={{ gap: 2 }}>
                <span className="mono ell" style={{ fontSize: 12 }} title={s.path}>{shortPath(s.path)}</span>
                <span className="small" style={{ color: s.err ? "var(--er)" : "var(--fa)" }}>
                  {KIND_LABEL[s.kind] ?? s.kind} · {s.contexts} context{s.contexts === 1 ? "" : "s"}{s.err ? " · " + s.err : ""}
                </span>
              </div>
              {s.removable && <button className="btn xs" onClick={() => call("RemoveKubeconfig", s.path).catch(fail)}>Remove</button>}
            </div>
          ))}
          {!sources.length && <div className="issue-row mu">No kubeconfig files were found.</div>}
        </div>
        {!explicit && (
          <>
            <div className="flex">
              <input className="input mono grow" placeholder="Path to a kubeconfig file or folder, for example ~/clusters" value={path} onChange={(e) => setPath(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && path.trim()) call<string>("AddKubeconfigPath", path).then((m) => {
                    added(m);
                    setPath("");
                  }, fail);
                }} />
              <button className="btn" disabled={!path.trim()} onClick={() => call<string>("AddKubeconfigPath", path).then((m) => {
                added(m);
                setPath("");
              }, fail)}>Add path</button>
            </div>
            <label className="flex small mu" style={{ cursor: "pointer" }}>
              <input type="checkbox" checked={scan} onChange={(e) => {
                const on = e.target.checked;
                ui.set((s) => ({ settings: s.settings ? { ...s.settings, scanKubeDir: on } : s.settings }));
                call("SetScanKubeDir", on).catch(fail);
              }} />
              Find other kubeconfig files in ~/.kube and one folder level below it
            </label>
            <span className="small fa" style={{ lineHeight: 1.5 }}>
              st8ks reads KUBECONFIG, or ~/.kube/config when KUBECONFIG is not set. It watches all sources and picks up new contexts, for example after aws eks update-kubeconfig. When two files define the same context name, the second one gets the file name as a suffix. To use only specific files, start st8ks with --kubeconfig path (more than once, or separated by colons) and optionally --context name.
            </span>
          </>
        )}
      </div>
    </div>
  );
}
