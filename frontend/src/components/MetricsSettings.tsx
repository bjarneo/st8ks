import { useState, type ReactNode } from "react";
import { call, errText } from "../lib/bridge";
import { saveSettings, ui } from "../lib/app";
import type { PromSource } from "../lib/metrics";
import { useStore } from "../lib/store";

type Mode = "auto" | "service" | "url";
type Row = (label: string, el: ReactNode, hint?: string) => ReactNode;

const modeOf = (v: string): Mode => (!v ? "auto" : /^https?:\/\//i.test(v) ? "url" : "service");

function describe(src: PromSource): { ok: boolean; text: string } {
  if (src.status !== "ok") return { ok: false, text: src.message ?? src.status };
  const via = src.via === "proxy" ? " through the API server proxy" : "";
  return { ok: true, text: `${src.flavor} at ${src.target}${via}. ${src.message ?? ""}`.trim() };
}

/** MetricsSettings is the Metrics section of Settings. */
export function MetricsSettings({ row }: { row: Row }) {
  const saved = useStore(ui, (s) => s.settings?.metricsSource ?? "");
  const [mode, setMode] = useState<Mode>(modeOf(saved));
  const [text, setText] = useState(saved);
  const [check, setCheck] = useState<{ ok: boolean; text: string } | null>(null);
  const [checking, setChecking] = useState(false);

  const test = () => {
    setChecking(true);
    setCheck(null);
    call<PromSource>("MetricsSource").then((src) => setCheck(describe(src)), (e) => setCheck({ ok: false, text: errText(e) }))
      .finally(() => setChecking(false));
  };
  const commit = async (v: string) => {
    v = v.trim();
    if (v === saved) return;
    if (v && modeOf(v) !== mode) {
      setCheck({ ok: false, text: mode === "url" ? "Enter a URL that starts with http:// or https://." : "Enter a Service as namespace/name:port." });
      return;
    }
    await saveSettings({ metricsSource: v });
    test();
  };
  const pick = (m: Mode) => {
    setMode(m);
    setCheck(null);
    if (m === "auto") {
      setText("");
      void saveSettings({ metricsSource: "" }).then(test);
    } else if (modeOf(text) !== m) {
      setText("");
    }
  };
  const hint = mode === "service"
    ? "namespace/name:port of a Service, with an optional path. st8ks connects through the API server proxy with your kubeconfig credentials."
    : mode === "url"
      ? "st8ks connects to the URL directly, without the cluster credentials."
      : "st8ks finds Prometheus or VictoriaMetrics in the cluster and connects through the API server proxy.";

  return row("Prometheus", (
    <div className="col" style={{ gap: 6 }}>
      <div className="flex">
        <div className="seg">
          {([["auto", "Auto"], ["service", "Service"], ["url", "URL"]] as [Mode, string][]).map(([m, l]) => (
            <button key={m} className={mode === m ? "on" : ""} onClick={() => pick(m)}>{l}</button>
          ))}
        </div>
        {mode !== "auto" && (
          <input className="input mono grow" value={text} spellCheck={false}
            placeholder={mode === "service" ? "monitoring/prometheus-server:80" : "http://localhost:9090"}
            onChange={(e) => setText(e.target.value)} onBlur={() => void commit(text)}
            onKeyDown={(e) => e.key === "Enter" && void commit(text)} />
        )}
        <button className="btn" disabled={checking} onClick={test}>{checking ? "Testing…" : "Test"}</button>
      </div>
      {check && <div className={check.ok ? "okbox" : "errbox"}>{check.text}</div>}
      <span className="small fa">{hint}{mode === "service" && " For a VictoriaMetrics cluster: vm/vmselect-main:8481/select/0/prometheus."}</span>
    </div>
  ), "The source of the CPU and memory history in the Metrics tab.");
}
