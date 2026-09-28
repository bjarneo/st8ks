import { call } from "../lib/bridge";
import { connect, fail, retry, say, ui } from "../lib/app";
import { useStore } from "../lib/store";

export function Toast() {
  const t = useStore(ui, (s) => s.toast);
  if (!t) return null;
  return <div className="toast" onClick={() => ui.set({ toast: null })}>{t}</div>;
}

export function DownView() {
  const cl = useStore(ui, (s) => s.cluster);
  const prev = useStore(ui, (s) => s.prevContext);
  return (
    <div className="center">
      <div className="col" style={{ maxWidth: 480, gap: 12, alignItems: "flex-start" }}>
        <div style={{ fontSize: 16, fontWeight: 600 }}>{cl?.context} is unreachable</div>
        <div className="mu" style={{ lineHeight: 1.55 }}>
          The API server{cl?.server ? " at " + cl.server : ""} did not respond. Cached resources are hidden so you never act on stale state.
        </div>
        {cl?.error && <div className="errbox" style={{ width: "100%" }}>{cl.error}</div>}
        <div className="flex">
          <button className="btn primary" onClick={() => void retry()}>Retry</button>
          {prev && <button className="btn" onClick={() => void connect(prev)}>Back to {prev}</button>}
          <button className="btn ghost" onClick={() => ui.set({ view: "clusters" })}>All clusters</button>
        </div>
      </div>
    </div>
  );
}

export function NoContexts() {
  const err = useStore(ui, (s) => s.loadErr);
  return (
    <div className="center">
      <div className="col" style={{ maxWidth: 480, gap: 12, alignItems: "flex-start" }}>
        <div style={{ fontSize: 16, fontWeight: 600 }}>No clusters found</div>
        <div className="mu" style={{ lineHeight: 1.55 }}>
          st8ks reads the kubeconfig files in KUBECONFIG, or ~/.kube/config. Import a kubeconfig file to add its contexts.
        </div>
        {err && <div className="errbox" style={{ width: "100%" }}>{err}</div>}
        <button className="btn primary" onClick={() => call<string>("ImportKubeconfig").then((m) => m && say(m), fail)}>Import kubeconfig…</button>
      </div>
    </div>
  );
}
