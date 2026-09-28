import { useState } from "react";
import { errText } from "../lib/bridge";
import { say, ui } from "../lib/app";
import { useStore } from "../lib/store";

export function Confirm() {
  const cf = useStore(ui, (s) => s.cf)!;
  const [typed, setTyped] = useState("");
  const [dry, setDry] = useState(false);
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ ok: boolean; text: string } | null>(null);
  const [input, setInput] = useState(cf.input?.value ?? "");
  const ok = !cf.word || typed === cf.word;

  const go = async () => {
    if (busy) return;
    if (!dry && !ok) return;
    setBusy(true);
    setRes(null);
    try {
      const msg = await cf.run(dry, cf.input ? input : undefined);
      if (dry) {
        setRes({ ok: true, text: msg });
      } else {
        ui.set({ cf: null });
        say(msg);
        cf.after?.();
      }
    } catch (e) {
      setRes({ ok: false, text: errText(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="modal-scrim" onClick={() => ui.set({ cf: null })} />
      <div className="modal" onKeyDown={(e) => {
        if (e.key === "Enter" && (e.target as HTMLElement).tagName === "INPUT") void go();
      }}>
        <div style={{ fontSize: 14, fontWeight: 600 }}>{cf.title}</div>
        <div className="itemlist">{cf.items.map((i) => <span key={i}>{i}</span>)}</div>
        <p style={{ margin: 0, lineHeight: 1.5 }} className="mu">{cf.note}</p>
        {cf.input && (
          <label className="col" style={{ gap: 6, fontSize: 12 }}>
            <span className="mu">{cf.input.label}</span>
            <input className="input mono" autoFocus type={cf.input.type ?? "text"} min={0} value={input} onChange={(e) => setInput(e.target.value)} />
          </label>
        )}
        {cf.word && (
          <label className="col" style={{ gap: 6, fontSize: 12 }}>
            <span className="mu">This touches a protected context or namespace. Type <b className="mono" style={{ color: "var(--tx)" }}>{cf.word}</b> to confirm.</span>
            <input className="input mono" autoFocus={!cf.input} value={typed} onChange={(e) => setTyped(e.target.value)} spellCheck={false} />
          </label>
        )}
        {res && <div className={res.ok ? "okbox" : "errbox"}>{res.ok ? "✓ " : ""}{res.text}</div>}
        <div className="flex">
          {!cf.noDry && (
            <div className="flex noselect" style={{ cursor: "pointer" }} onClick={() => {
              setDry(!dry);
              setRes(null);
            }}>
              <span className={"chk" + (dry ? " on" : "")}>{dry ? "✓" : ""}</span>Dry run first
            </div>
          )}
          <span style={{ flex: 1 }} />
          <button className="btn" style={{ height: 30, padding: "0 14px" }} onClick={() => ui.set({ cf: null })}>Cancel</button>
          <button className={"btn " + (dry ? "" : cf.danger ? "danger" : "primary")} style={{ height: 30, padding: "0 14px", opacity: ok || dry ? 1 : 0.4 }}
            disabled={busy || (!dry && !ok)} onClick={() => void go()}>
            {busy && <span className="spinner" />}{dry ? "Run dry-run" : cf.cta}
          </button>
        </div>
      </div>
    </>
  );
}
