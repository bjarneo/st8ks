import { useState } from "react";
import { call } from "../lib/bridge";
import { fail, ui } from "../lib/app";
import { shallowEqual, useStore } from "../lib/store";
import { sumCounts } from "./Tree";
import { statusDot } from "./TopBar";

export function StatusBar() {
  const cl = useStore(ui, (s) => s.cluster);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const warn = useStore(ui, (s) => sumCounts(s.counts, "Events", s.nsSel)[1]);
  const fw = useStore(ui, (s) => s.forwards);
  const mac = useStore(ui, (s) => s.platform === "darwin");
  const [fwOpen, setFwOpen] = useState(false);
  const nsLabel = !nsSel.length ? "All namespaces" : nsSel.length === 1 ? nsSel[0] : `${nsSel.length} namespaces`;
  return (
    <div className="status">
      <span className="flex" style={{ gap: 6 }}><span className="dot sm" style={{ background: statusDot(cl?.status) }} />{cl?.context ?? "—"}</span>
      {cl?.version && <span>{cl.dist} · {cl.version}{cl.region ? " · " + cl.region : ""}</span>}
      <span>ns: {nsLabel}</span>
      {cl?.status === "Connected" && (
        <span className="link wa" onClick={() => ui.set({ view: "events", detail: null })}>▲ {warn} warnings</span>
      )}
      {fw.length > 0 && (
        <span style={{ position: "relative" }}>
          <span className="link ac" onClick={() => setFwOpen(!fwOpen)}>⇄ {fw.length} port-forward{fw.length > 1 ? "s" : ""}</span>
          {fwOpen && (
            <>
              <div className="scrim" onClick={() => setFwOpen(false)} />
              <div className="menu" style={{ top: "auto", bottom: 22, width: 360, fontFamily: "var(--sans)" }}>
                {fw.map((f) => (
                  <div key={f.id} className="menu-item" style={{ cursor: "default" }}>
                    <a className="mono ell grow" style={{ fontSize: "calc(11.5px * var(--zf))" }} href="#" onClick={(e) => {
                      e.preventDefault();
                      call("OpenURL", `http://localhost:${f.local}`);
                    }}>localhost:{f.local} → {f.ns}/{f.pod}:{f.remote}</a>
                    <button className="btn xs" onClick={() => call("StopForward", f.id).catch(fail)}>Stop</button>
                  </div>
                ))}
                <div className="menu-sep" />
                <div className="menu-item" onClick={() => {
                  call("StopAllForwards").catch(fail);
                  setFwOpen(false);
                }}>Stop all port-forwards</div>
              </div>
            </>
          )}
        </span>
      )}
      <span style={{ flex: 1 }} />
      {cl?.status === "Connected" && <span>{cl.metrics ? "metrics-server ✓" : "metrics-server ✕"}</span>}
      <span>{mac ? "⌘K" : "Ctrl K"} commands · / filter · Esc close</span>
    </div>
  );
}
