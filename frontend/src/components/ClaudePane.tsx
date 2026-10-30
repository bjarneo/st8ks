import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { call, errText, fromBase64, toBase64 } from "../lib/bridge";
import { scale, ui, type DockTab } from "../lib/app";
import { execRouter } from "../lib/streams";
import { termTheme } from "../lib/term";
import type { ClaudeInfo, ClaudeSession } from "../lib/types";
import { ClaudeMissing } from "./ClaudeSetup";
import { useScale } from "./VTable";

type State = "checking" | "missing" | "unsupported" | "starting" | "live" | "ended";

/** ClaudePane runs Claude Code in the dock. The st8ks MCP server and kubectl
 * in the session use the connected context. */
export function ClaudePane({ tab, visible }: { tab: DockTab; visible: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const sid = useRef("");
  const off = useRef<() => void>(() => {});
  const dead = useRef(false);
  const [state, setState] = useState<State>("checking");
  const [info, setInfo] = useState<ClaudeInfo | null>(null);
  const [sess, setSess] = useState<ClaudeSession | null>(null);
  const [msg, setMsg] = useState("");
  const z = useScale();

  useEffect(() => {
    const t = new Terminal({
      fontFamily: '"Geist Mono Variable", "Geist Mono", ui-monospace, monospace', fontSize: Math.round(12 * scale(ui.get().settings)),
      lineHeight: 1.2, cursorBlink: true, scrollback: 10000, theme: termTheme(), allowProposedApi: true, macOptionIsMeta: true,
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.open(host.current!);
    try {
      const gl = new WebglAddon();
      gl.onContextLoss(() => gl.dispose());
      t.loadAddon(gl);
    } catch {
      // The DOM renderer works everywhere.
    }
    term.current = t;
    fit.current = f;
    const data = t.onData((d) => {
      if (sid.current) call("ClaudeInput", sid.current, toBase64(d)).catch(() => {});
    });
    let rt = 0;
    const ro = new ResizeObserver(() => {
      clearTimeout(rt);
      rt = window.setTimeout(() => {
        if (!host.current?.offsetWidth) return;
        try {
          f.fit();
        } catch {
          return;
        }
        if (sid.current) void call("ClaudeResize", sid.current, t.cols, t.rows);
      }, 60);
    });
    ro.observe(host.current!);
    const mo = new MutationObserver(() => {
      t.options.theme = termTheme();
    });
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => {
      dead.current = true;
      ro.disconnect();
      mo.disconnect();
      data.dispose();
      off.current();
      if (sid.current) void call("ClaudeStop", sid.current);
      t.dispose();
    };
  }, []);

  useEffect(() => {
    const t = term.current;
    const size = Math.round(12 * z);
    if (!t || t.options.fontSize === size) return;
    t.options.fontSize = size;
    try {
      fit.current?.fit();
    } catch {
      return;
    }
    if (sid.current) void call("ClaudeResize", sid.current, t.cols, t.rows);
  }, [z]);

  const start = useCallback(async () => {
    const t = term.current;
    if (!t) return;
    setState("checking");
    setMsg("");
    let ci: ClaudeInfo;
    try {
      ci = await call<ClaudeInfo>("ClaudeInfo");
    } catch (e) {
      setState("ended");
      setMsg(errText(e));
      return;
    }
    if (dead.current) return;
    setInfo(ci);
    if (!ci.supported) return setState("unsupported");
    if (!ci.path) return setState("missing");
    setState("starting");
    try {
      fit.current?.fit();
    } catch {
      // The dock is minimized.
    }
    try {
      const s = await call<ClaudeSession>("ClaudeStart", t.cols, t.rows, tab.issue ?? "");
      if (dead.current) {
        void call("ClaudeStop", s.id);
        return;
      }
      sid.current = s.id;
      setSess(s);
      setState("live");
      off.current();
      off.current = execRouter.listen(s.id, (c) => {
        if (c.data) t.write(fromBase64(c.data));
        if (c.end) {
          sid.current = "";
          t.writeln(`\r\n\x1b[2m[Claude Code exited${c.err ? ": " + c.err : ""}]\x1b[0m`);
          setState("ended");
        }
      });
      t.focus();
    } catch (e) {
      setState("ended");
      setMsg(errText(e));
    }
  }, [tab.issue]);

  useEffect(() => {
    void start();
  }, [start]);

  useEffect(() => {
    if (!visible) return;
    requestAnimationFrame(() => {
      const t = term.current;
      try {
        fit.current?.fit();
      } catch {
        // The dock is minimized.
      }
      if (sid.current && t) void call("ClaudeResize", sid.current, t.cols, t.rows);
      t?.focus();
    });
  }, [visible]);

  const blocked = state === "missing" || state === "unsupported";
  const status = state === "live" && sess
    ? `${sess.context} · the st8ks MCP tools and kubectl use this context · ${sess.dir}`
    : state === "ended" ? (msg ? "Claude Code did not start." : "Claude Code exited.")
      : state === "starting" || state === "checking" ? "Starting Claude Code…" : "";
  return (
    <>
      <div className="logbar">
        <span className="diamond" style={{ background: "var(--ac)", width: 6, height: 6 }} />
        <span style={{ fontWeight: 500 }}>Claude Code</span>
        <span className="fa ell grow mono">{status}</span>
        {state === "ended" && <button className="btn xs" onClick={() => {
          term.current?.reset();
          void start();
        }}>Restart</button>}
      </div>
      {msg && <div className="errbox" style={{ margin: "6px 10px 0" }}>{msg}</div>}
      {state === "missing" && info && (
        <div style={{ padding: 18, maxWidth: 640 }}><ClaudeMissing info={info} retry={() => void start()} /></div>
      )}
      {state === "unsupported" && info && <div style={{ padding: 18, maxWidth: 640, lineHeight: 1.5 }} className="mu">{info.reason}</div>}
      <div className="term" style={{ display: blocked ? "none" : undefined }}><div ref={host} style={{ height: "100%" }} /></div>
    </>
  );
}
