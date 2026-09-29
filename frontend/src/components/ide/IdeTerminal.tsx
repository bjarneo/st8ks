import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { call, errText, fromBase64, toBase64 } from "../../lib/bridge";
import { scale, ui } from "../../lib/app";
import { useStore } from "../../lib/store";
import { execRouter } from "../../lib/streams";
import { termTheme } from "../../lib/term";
import { useScale } from "../VTable";

/** IdeTerminal is a shell in the workspace folder. kubectl in it uses the
 * target context of the IDE. */
export default function IdeTerminal({ visible }: { visible: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const sid = useRef("");
  const off = useRef<() => void>(() => {});
  const dead = useRef(false);
  const [state, setState] = useState<"idle" | "starting" | "live" | "ended">("idle");
  const [msg, setMsg] = useState("");
  const ctx = useStore(ui, (s) => s.cluster?.context ?? "");
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
      if (sid.current) call("IdeTermInput", sid.current, toBase64(d)).catch(() => {});
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
        if (sid.current) void call("IdeTermResize", sid.current, t.cols, t.rows);
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
      if (sid.current) void call("IdeTermStop", sid.current);
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
    if (sid.current) void call("IdeTermResize", sid.current, t.cols, t.rows);
  }, [z]);

  const start = useCallback(async () => {
    const t = term.current;
    if (!t) return;
    setState("starting");
    setMsg("");
    try {
      fit.current?.fit();
    } catch {
      // The panel is hidden.
    }
    try {
      const id = await call<string>("IdeTermStart", t.cols, t.rows);
      if (dead.current) {
        void call("IdeTermStop", id);
        return;
      }
      sid.current = id;
      setState("live");
      off.current();
      off.current = execRouter.listen(id, (c) => {
        if (c.data) t.write(fromBase64(c.data));
        if (c.end) {
          sid.current = "";
          t.writeln(`\r\n\x1b[2m[the shell exited${c.err ? ": " + c.err : ""}]\x1b[0m`);
          setState("ended");
        }
      });
      t.focus();
    } catch (e) {
      setState("ended");
      setMsg(errText(e));
    }
  }, []);

  useEffect(() => {
    if (!visible) return;
    if (state === "idle") void start();
    requestAnimationFrame(() => {
      const t = term.current;
      try {
        fit.current?.fit();
      } catch {
        // The panel is hidden.
      }
      if (sid.current && t) void call("IdeTermResize", sid.current, t.cols, t.rows);
      t?.focus();
    });
  }, [visible, state, start]);

  return (
    <>
      {(state === "ended" || ctx) && (
        <div className="flex" style={{ padding: "4px 10px", borderBottom: "1px solid var(--ln)", flex: "none" }}>
          <span className="fa small grow ell">
            {state === "ended" ? (msg ? "The shell did not start: " + msg : "The shell exited.") : `kubectl uses ${ctx}. It follows the target context of the IDE.`}
          </span>
          {state === "ended" && <button className="btn xs" onClick={() => {
            term.current?.reset();
            void start();
          }}>Restart</button>}
        </div>
      )}
      <div className="ide-term"><div ref={host} style={{ height: "100%" }} /></div>
    </>
  );
}
