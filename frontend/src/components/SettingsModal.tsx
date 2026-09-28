import { useState } from "react";
import { call, errText } from "../lib/bridge";
import { fail, saveSettings, say, TEXT_SIZES, ui } from "../lib/app";
import { useStore } from "../lib/store";
import type { Settings } from "../lib/types";

const MODELS = ["claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5-1"];

function Seg<T extends string>({ value, options, onChange }: { value: T; options: [T, string][]; onChange: (v: T) => void }) {
  return (
    <div className="seg">
      {options.map(([v, l]) => <button key={v} className={value === v ? "on" : ""} onClick={() => onChange(v)}>{l}</button>)}
    </div>
  );
}

export function SettingsModal() {
  const s = useStore(ui, (st) => st.settings)!;
  const version = useStore(ui, (st) => st.version);
  const platform = useStore(ui, (st) => st.platform);
  const [key, setKey] = useState("");
  const [check, setCheck] = useState<{ ok: boolean; text: string } | null>(null);
  const [checking, setChecking] = useState(false);
  const test = () => {
    setChecking(true);
    setCheck(null);
    call<string>("CheckAssistant").then((text) => setCheck({ ok: true, text }), (e) => setCheck({ ok: false, text: errText(e) }))
      .finally(() => setChecking(false));
  };
  const [pattern, setPattern] = useState(s.protectedPattern);
  const [tail, setTail] = useState(String(s.logTail));
  const row = (label: string, el: React.ReactNode, hint?: string) => (
    <div className="flex" style={{ alignItems: "flex-start", gap: 14 }}>
      <div className="col" style={{ width: 170, flex: "none", gap: 2, paddingTop: 5 }}>
        <span>{label}</span>
        {hint && <span className="small fa" style={{ lineHeight: 1.4 }}>{hint}</span>}
      </div>
      <div className="grow">{el}</div>
    </div>
  );
  const save = (p: Partial<Settings>) => void saveSettings(p);
  return (
    <>
      <div className="modal-scrim" onClick={() => ui.set({ settingsOpen: false })} />
      <div className="modal wide">
        <div className="flex"><span style={{ fontSize: "calc(14px * var(--zf))", fontWeight: 600 }} className="grow">Settings</span>
          <button className="btn icon sm ghost fa" onClick={() => ui.set({ settingsOpen: false })}>✕</button></div>
        <div className="sec-label">Appearance</div>
        {row("Theme", <Seg value={s.theme} options={[["dark", "Dark"], ["light", "Light"]]} onChange={(v) => save({ theme: v })} />)}
        {row("Density", <Seg value={s.density} options={[["compact", "Compact"], ["comfortable", "Comfortable"]]} onChange={(v) => save({ density: v })} />)}
        {row("Text size", <Seg value={String(s.textSize || 100)} options={TEXT_SIZES.map((v) => [String(v), v + " %"] as [string, string])} onChange={(v) => save({ textSize: Number(v) })} />,
          `${platform === "darwin" ? "Cmd" : "Ctrl"} + and ${platform === "darwin" ? "Cmd" : "Ctrl"} − change it anywhere. ${platform === "darwin" ? "Cmd" : "Ctrl"} 0 resets it.`)}
        {row("Detail layout", <Seg value={s.detailLayout} options={[["drawer", "Drawer"], ["split", "Split"], ["page", "Page"]]} onChange={(v) => save({ detailLayout: v })} />)}
        {row("Overview nodes", <Seg value={s.overviewStyle} options={[["metrics", "Table"], ["nodemap", "Node map"]]} onChange={(v) => save({ overviewStyle: v })} />)}

        <div className="sec-label">Assistant</div>
        {row("Anthropic API key", (
          <div className="col" style={{ gap: 6 }}>
            <div className="flex">
              <input className="input mono grow" type="password" placeholder={s.hasKey ? "A key is set. Enter a new key to replace it." : "sk-ant-…"} value={key} onChange={(e) => setKey(e.target.value)} />
              <button className="btn primary" disabled={!key.trim()} onClick={() => call<Settings>("SetAPIKey", key).then((st) => {
                ui.set({ settings: { ...s, hasKey: st.hasKey } });
                setKey("");
                say("API key saved");
                test();
              }, fail)}>Save</button>
              {s.hasKey && <button className="btn" onClick={() => call<Settings>("SetAPIKey", "").then((st) => {
                ui.set({ settings: { ...s, hasKey: st.hasKey } });
                setCheck(null);
                say("API key removed");
              }, fail)}>Remove</button>}
              <button className="btn" disabled={checking} onClick={test}>{checking ? "Testing…" : "Test"}</button>
            </div>
            {check && <div className={check.ok ? "okbox" : "errbox"}>{check.text}</div>}
            <span className="small fa">The key is stored in the st8ks settings file, readable only by you. A key here goes straight to api.anthropic.com. Without one, st8ks uses ANTHROPIC_API_KEY or an ant CLI login.</span>
          </div>
        ), s.hasKey ? "A key is set." : "No key is set.")}
        {row("Model", (
          <select className="input mono" value={s.assistantModel} onChange={(e) => save({ assistantModel: e.target.value })}>
            {[...new Set([s.assistantModel, ...MODELS])].map((m) => <option key={m} value={m}>{m}</option>)}
          </select>
        ))}

        <div className="sec-label">Safety</div>
        {row("Protected names", (
          <input className="input mono" style={{ width: "100%" }} value={pattern} onChange={(e) => setPattern(e.target.value)}
            onBlur={() => {
              try {
                new RegExp(pattern);
                save({ protectedPattern: pattern });
              } catch {
                say("The pattern is not a valid regular expression");
              }
            }} />
        ), "A regular expression. Changes to a matching context or namespace need the name typed to confirm.")}

        <div className="sec-label">Logs</div>
        {row("Initial lines", (
          <input className="input mono" style={{ width: 120 }} type="number" min={100} value={tail} onChange={(e) => setTail(e.target.value)}
            onBlur={() => save({ logTail: Math.max(100, parseInt(tail, 10) || 5000) })} />
        ), "Lines to load before following a log.")}

        <div className="sec-label">Kubeconfig</div>
        {row("Files", (
          <button className="btn" onClick={() => ui.set({ settingsOpen: false, view: "clusters", detail: null })}>Manage kubeconfig sources…</button>
        ), "Add files or folders, or scan ~/.kube.")}
        <div className="small fa mono" style={{ borderTop: "1px solid var(--ln)", paddingTop: 10 }}>st8ks {version} · {platform}</div>
      </div>
    </>
  );
}
