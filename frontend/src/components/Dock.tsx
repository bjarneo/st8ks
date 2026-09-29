import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { call, errText, fromBase64, toBase64 } from "../lib/bridge";
import { closeDock, fail, say, scale, ui, type DockTab } from "../lib/app";
import { useStore } from "../lib/store";
import { execRouter, logRouter } from "../lib/streams";
import { termTheme } from "../lib/term";
import type { ContainerModel, ObjectDoc } from "../lib/types";
import { useScale } from "./VTable";

// ---- Logs ----

interface LogLine { ts: string; lvl: "" | "DEBUG" | "INFO" | "WARN" | "ERROR"; text: string; low: string }

const MAX_LINES = 100_000;
const errRe = /\b(ERROR|ERR|FATAL|PANIC|CRITICAL|CRIT|EMERG|ALERT)\b|level=(error|fatal|panic)|"level":\s*"(error|fatal|panic)"|\bE\d{4} /i;
const warnRe = /\b(WARN|WARNING)\b|level=warn|"level":\s*"warn/i;
const infoRe = /\b(INFO|NOTICE)\b|level=info|"level":\s*"info/i;
const debugRe = /\b(DEBUG|TRACE)\b|level=(debug|trace)|"level":\s*"(debug|trace)/i;

function parseLine(raw: string): LogLine {
  let ts = "", text = raw;
  const sp = raw.indexOf(" ");
  if (sp > 18 && raw.charCodeAt(4) === 45 && raw[10] === "T") {
    ts = raw.slice(0, sp);
    text = raw.slice(sp + 1);
    const dot = ts.indexOf(".");
    if (dot > 0) ts = ts.slice(0, Math.min(dot + 4, ts.length - 1)) + "Z";
  }
  const lvl = errRe.test(text) ? "ERROR" : warnRe.test(text) ? "WARN" : infoRe.test(text) ? "INFO" : debugRe.test(text) ? "DEBUG" : "";
  return { ts, lvl, text, low: text.toLowerCase() };
}

function Highlight({ text, q }: { text: string; q: string }) {
  if (!q) return <>{text}</>;
  const lt = text.toLowerCase();
  const out: React.ReactNode[] = [];
  let i = 0, k = 0;
  for (;;) {
    const j = lt.indexOf(q, i);
    if (j < 0) break;
    out.push(text.slice(i, j), <mark key={k++}>{text.slice(j, j + q.length)}</mark>);
    i = j + q.length;
  }
  out.push(text.slice(i));
  return <>{out}</>;
}

function useContainers(ns: string, pod: string) {
  const [doc, setDoc] = useState<ObjectDoc | null>(null);
  const [err, setErr] = useState("");
  const reload = useCallback(() => {
    call<ObjectDoc>("GetObject", { kind: "Pods", ns, name: pod }).then((d) => {
      setDoc(d);
      setErr("");
    }, (e) => setErr(errText(e)));
  }, [ns, pod]);
  useEffect(reload, [reload]);
  return { doc, err, reload };
}

function defaultContainer(cs: ContainerModel[]): string {
  return (cs.find((c) => !c.type) ?? cs[0])?.name ?? "";
}

function LogsPane({ tab }: { tab: DockTab }) {
  // The line height matches --lh.
  const lineH = Math.round(20 * useScale());
  const { doc } = useContainers(tab.ns, tab.pod);
  const containers = doc?.pod?.containers ?? [];
  const [container, setContainer] = useState(tab.container ?? "");
  const [previous, setPrevious] = useState(!!tab.previous);
  const [ts, setTs] = useState(true);
  const [wrap, setWrap] = useState(false);
  const [lvl, setLvl] = useState<"all" | "warn" | "error">("all");
  const [q, setQ] = useState("");
  const [status, setStatus] = useState<"loading" | "live" | "waiting" | "ended">("loading");
  const [err, setErr] = useState("");
  const buf = useRef<LogLine[]>([]);
  const [ver, setVer] = useState(0);
  const raf = useRef(0);
  const box = useRef<HTMLDivElement>(null);
  const [view, setView] = useState({ top: 0, h: 240 });
  const follow = useRef(true);

  useEffect(() => {
    if (!container && containers.length) setContainer(tab.container ?? defaultContainer(containers));
  }, [containers, container, tab.container]);

  useEffect(() => {
    if (!container) return;
    buf.current = [];
    setVer((v) => v + 1);
    setStatus("loading");
    setErr("");
    follow.current = true;
    let id = "", off = () => {}, dead = false;
    const bump = () => {
      if (raf.current) return;
      raf.current = requestAnimationFrame(() => {
        raf.current = 0;
        setVer((v) => v + 1);
      });
    };
    call<string>("StartLogs", { ns: tab.ns, pod: tab.pod, container, previous, tail: 0 }).then((sid) => {
      if (dead) return void call("StopLogs", sid);
      id = sid;
      off = logRouter.listen(sid, (c) => {
        if (c.lines) {
          const b = buf.current;
          for (const l of c.lines) b.push(parseLine(l));
          if (b.length > MAX_LINES) {
            // Trim to a new array, so the filter cache starts again.
            buf.current = b.slice(b.length - MAX_LINES + 10_000);
          }
          setStatus("live");
          bump();
        }
        if (c.waiting) setStatus("waiting");
        if (c.end) {
          setStatus("ended");
          if (c.err) setErr(c.err);
        } else if (c.err && c.waiting) setErr(c.err);
      });
    }, (e) => {
      setStatus("ended");
      setErr(errText(e));
    });
    return () => {
      dead = true;
      off();
      if (id) void call("StopLogs", id);
    };
  }, [container, previous, tab.ns, tab.pod]);

  const ql = q.toLowerCase();
  // The filter runs only over lines that arrived since the last frame. It
  // starts again when the query, the level or the buffer changes.
  const fcache = useRef<{ key: string; buf: LogLine[]; seen: number; out: LogLine[] }>({ key: "", buf: [], seen: 0, out: [] });
  const lines = useMemo(() => {
    const b = buf.current;
    if (lvl === "all" && !ql) return b;
    const key = lvl + "\0" + ql;
    let c = fcache.current;
    if (c.key !== key || c.buf !== b || c.seen > b.length) c = fcache.current = { key, buf: b, seen: 0, out: [] };
    const match = (l: LogLine) => (lvl === "all" || (lvl === "error" ? l.lvl === "ERROR" : l.lvl === "ERROR" || l.lvl === "WARN")) && (!ql || l.low.includes(ql));
    for (let i = c.seen; i < b.length; i++) if (match(b[i])) c.out.push(b[i]);
    c.seen = b.length;
    return c.out.slice();
  }, [ver, lvl, ql]); // eslint-disable-line react-hooks/exhaustive-deps

  useLayoutEffect(() => {
    const el = box.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [lines, wrap, ver]);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setView({ top: el.scrollTop, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const onScroll = () => {
    const el = box.current!;
    follow.current = el.scrollTop + el.clientHeight >= el.scrollHeight - lineH * 2;
    setView({ top: el.scrollTop, h: el.clientHeight });
  };

  const start = wrap ? Math.max(0, lines.length - 3000) : Math.max(0, Math.floor(view.top / lineH) - 20);
  const end = wrap ? lines.length : Math.min(lines.length, Math.ceil((view.top + view.h) / lineH) + 20);
  const cur = containers.find((c) => c.name === container);
  const canPrev = (cur?.restarts ?? 0) > 0 || !!cur?.last || previous;
  const followLabel = status === "waiting" ? "Waiting for the container to start…" : status === "ended" ? "Stream ended" : status === "loading" ? "Connecting…" : previous ? "Previous container" : "Following";

  return (
    <>
      <div className="logbar">
        {containers.length > 1 ? (
          <select value={container} onChange={(e) => setContainer(e.target.value)}>
            {containers.map((c) => <option key={c.name} value={c.name}>{c.name}{c.type ? ` (${c.type})` : ""}</option>)}
          </select>
        ) : (
          <span className="chip" style={{ borderRadius: 4 }}>container: {container || "…"}</span>
        )}
        {canPrev && <button className={"btn" + (previous ? " on" : "")} onClick={() => setPrevious(!previous)}>Previous container{previous ? " ✓" : ""}</button>}
        <button className={"btn" + (ts ? " on" : "")} onClick={() => setTs(!ts)}>Timestamps{ts ? " ✓" : ""}</button>
        <button className={"btn" + (wrap ? " on" : "")} onClick={() => setWrap(!wrap)}>Wrap{wrap ? " ✓" : ""}</button>
        <div className="seg" style={{ padding: 1 }}>
          {(["all", "warn", "error"] as const).map((l) => (
            <button key={l} className={lvl === l ? "on" : ""} style={{ height: 20 }} onClick={() => setLvl(l)}>{l === "all" ? "All" : l === "warn" ? "Warn+" : "Error"}</button>
          ))}
        </div>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search logs" style={{ width: 200 }} />
        {q && <span className="fa">{lines.length} lines</span>}
        <span style={{ flex: 1 }} />
        <span className="flex fa" style={{ gap: 6 }}>
          <span className={"dot sm" + (status === "live" && !previous ? " pulse" : "")} style={{ background: status === "ended" ? "var(--fa)" : status === "waiting" ? "var(--wa)" : "var(--ok)" }} />
          {followLabel}
        </span>
        <button className="btn" onClick={() => {
          follow.current = true;
          const el = box.current;
          if (el) el.scrollTop = el.scrollHeight;
        }}>↓ End</button>
        <button className="btn" onClick={() => call<string>("SaveLogs", { ns: tab.ns, pod: tab.pod, container, previous, tail: 0 }).then((m) => m && say(m), fail)}>Download</button>
      </div>
      {err && <div className="errbox" style={{ margin: "6px 10px 0" }}>{err}</div>}
      <div className="logs" ref={box} onScroll={onScroll}>
        {!lines.length && status !== "loading" && <div style={{ padding: "6px 12px" }} className="fa">{buf.current.length ? "No lines match." : "No log lines yet."}</div>}
        {wrap ? (
          lines.slice(start, end).map((l, i) => (
            <div key={start + i} className={"logl wrap" + (l.lvl === "ERROR" ? " err" : "")}>
              {ts && l.ts && <span className="ts">{l.ts}</span>}
              <span className="lv" style={{ color: l.lvl === "ERROR" ? "var(--er)" : l.lvl === "WARN" ? "var(--wa)" : "var(--fa)" }}>{l.lvl}</span>
              <span><Highlight text={l.text} q={ql} /></span>
            </div>
          ))
        ) : (
          <div style={{ height: lines.length * lineH + 12, position: "relative", minWidth: "max-content" }}>
            {lines.slice(start, end).map((l, i) => (
              <div key={start + i} className={"logl" + (l.lvl === "ERROR" ? " err" : "")} style={{ top: (start + i) * lineH + 6 }}>
                {ts && <span className="ts">{l.ts}</span>}
                <span className="lv" style={{ color: l.lvl === "ERROR" ? "var(--er)" : l.lvl === "WARN" ? "var(--wa)" : "var(--fa)" }}>{l.lvl}</span>
                <span><Highlight text={l.text} q={ql} /></span>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

// ---- Terminal ----

function TermPane({ tab, visible }: { tab: DockTab; visible: boolean }) {
  const { doc, err: docErr, reload } = useContainers(tab.ns, tab.pod);
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const sid = useRef("");
  const offExec = useRef<() => void>(() => {});
  const disposed = useRef(false);
  const [state, setState] = useState<"checking" | "blocked" | "connecting" | "live" | "ended">("checking");
  const [msg, setMsg] = useState("");
  const [image, setImage] = useState("busybox:1.36");
  const [target, setTarget] = useState(tab.container ?? "");
  const started = useRef(false);
  const z = useScale();

  // Create the terminal once.
  useEffect(() => {
    const t = new Terminal({
      fontFamily: '"Geist Mono Variable", "Geist Mono", ui-monospace, monospace', fontSize: Math.round(12 * scale(ui.get().settings)), lineHeight: 1.2,
      cursorBlink: true, scrollback: 10000, theme: termTheme(), allowProposedApi: true, macOptionIsMeta: true,
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
    const onData = t.onData((d) => {
      if (sid.current) call("ExecInput", sid.current, toBase64(d)).catch(() => {});
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
        if (sid.current) void call("ExecResize", sid.current, t.cols, t.rows);
      }, 60);
    });
    ro.observe(host.current!);
    const mo = new MutationObserver(() => {
      t.options.theme = termTheme();
    });
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => {
      ro.disconnect();
      mo.disconnect();
      onData.dispose();
      disposed.current = true;
      offExec.current();
      if (sid.current) void call("StopExec", sid.current);
      t.dispose();
    };
  }, []);

  // Follow the text size setting. The resize observer fits the new size.
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
    if (sid.current) void call("ExecResize", sid.current, t.cols, t.rows);
  }, [z]);

  const connect = useCallback(async (container: string, attach: boolean) => {
    const t = term.current!;
    setState("connecting");
    try {
      fit.current?.fit();
    } catch {
      /* hidden */
    }
    try {
      const id = await call<string>("StartExec", { ns: tab.ns, pod: tab.pod, container, attach, cols: t.cols, rows: t.rows });
      if (disposed.current) {
        void call("StopExec", id);
        return;
      }
      sid.current = id;
      setState("live");
      if (attach) t.writeln("\x1b[2mAttached. If you do not see a prompt, press Enter.\x1b[0m");
      offExec.current();
      offExec.current = execRouter.listen(id, (c) => {
        if (c.data) t.write(fromBase64(c.data));
        if (c.end) {
          sid.current = "";
          t.writeln(`\r\n\x1b[2m[session ended${c.err ? ": " + c.err : ""}]\x1b[0m`);
          setState("ended");
          setMsg(c.err ?? "");
        }
      });
      if (visible) t.focus();
    } catch (e) {
      setState("ended");
      setMsg(errText(e));
    }
  }, [tab.ns, tab.pod, visible]);

  useEffect(() => {
    if (!doc?.pod || started.current) return;
    const cs = doc.pod.containers.filter((c) => c.type !== "init");
    const name = tab.container ?? defaultContainer(cs);
    setTarget(name);
    const c = cs.find((x) => x.name === name);
    started.current = true;
    if (c?.running) void connect(name, false);
    else setState("blocked");
  }, [doc, connect, tab.container]);

  useEffect(() => {
    if (!visible) return;
    const t = term.current;
    requestAnimationFrame(() => {
      try {
        fit.current?.fit();
      } catch {
        /* hidden */
      }
      if (sid.current && t) void call("ExecResize", sid.current, t.cols, t.rows);
      t?.focus();
    });
  }, [visible]);

  const startDebug = async () => {
    setState("connecting");
    setMsg("");
    try {
      const name = await call<string>("StartDebug", tab.ns, tab.pod, target, image);
      if (disposed.current) return;
      say(`Ephemeral container ${name} (${image}) attached to ${tab.pod}`);
      await connect(name, true);
    } catch (e) {
      setState("blocked");
      setMsg(errText(e));
    }
  };

  const cur = doc?.pod?.containers.find((c) => c.name === target);
  return (
    <>
      {state === "blocked" && (
        <div className="col" style={{ padding: 18, gap: 10, maxWidth: 620, alignItems: "flex-start" }}>
          <span style={{ fontWeight: 500 }}>Cannot exec: container {target} is not running ({cur?.state ?? doc?.pod?.phase ?? "unknown"})</span>
          <span className="mu" style={{ lineHeight: 1.5 }}>
            Attach an ephemeral debug container that shares the pod’s process namespace instead. This is the same as kubectl debug -it --image={image} --target={target}.
          </span>
          <div className="flex">
            <input className="input mono" style={{ width: 240, height: 26 }} value={image} onChange={(e) => setImage(e.target.value)} />
            <button className="btn sm primary" onClick={() => void startDebug()}>Start debug container</button>
          </div>
          {msg && <div className="errbox">{msg}</div>}
        </div>
      )}
      {docErr && state === "checking" && <div className="errbox" style={{ margin: 10 }}>{docErr}</div>}
      {state === "ended" && (
        <div className="flex" style={{ padding: "6px 10px", borderBottom: "1px solid var(--ln)" }}>
          <span className="fa small grow">{msg ? "The session ended: " + msg : "The session ended."}</span>
          <button className="btn xs" onClick={() => {
            started.current = false;
            term.current?.reset();
            reload();
          }}>Reconnect</button>
          <button className="btn xs" onClick={() => setState("blocked")}>Debug container…</button>
        </div>
      )}
      <div className="term" style={{ display: state === "blocked" ? "none" : undefined }}><div ref={host} style={{ height: "100%" }} /></div>
    </>
  );
}

// ---- Dock ----

export function Dock() {
  const dock = useStore(ui, (s) => s.dock);
  const active = useStore(ui, (s) => s.dockActive);
  const min = useStore(ui, (s) => s.dockMin);
  const [h, setH] = useState(() => {
    try {
      return Math.max(160, parseInt(localStorage.getItem("st8ks.dockH") ?? "", 10) || 280);
    } catch {
      return 280;
    }
  });
  const act = dock.find((t) => t.id === active) ?? dock[0];
  const startDrag = (e: React.MouseEvent) => {
    const y0 = e.clientY, h0 = h;
    const move = (ev: MouseEvent) => setH(Math.max(140, Math.min(window.innerHeight - 200, h0 + (y0 - ev.clientY))));
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      setH((v) => {
        try {
          localStorage.setItem("st8ks.dockH", String(v));
        } catch {
          /* storage unavailable */
        }
        return v;
      });
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };
  return (
    <div className="dock">
      {!min && <div className="dock-resize" onMouseDown={startDrag} />}
      <div className="dock-tabs">
        {dock.map((t) => (
          <div key={t.id} className={"dock-tab" + (t.id === act?.id ? " on" : "")} onClick={() => ui.set({ dockActive: t.id, dockMin: false })}>
            {t.type === "logs" ? "Logs" : "Shell"} · {t.pod}{t.container ? `/${t.container}` : ""}
            <span className="x" onClick={(e) => {
              e.stopPropagation();
              closeDock(t.id);
            }}>✕</span>
          </div>
        ))}
        <span style={{ flex: 1 }} />
        <button className="btn ghost fa" style={{ width: 32, height: "100%", borderRadius: 0 }} title={min ? "Restore" : "Minimize"} onClick={() => ui.set({ dockMin: !min })}>{min ? "⌃" : "⌄"}</button>
        <button className="btn ghost fa" style={{ width: 32, height: "100%", borderRadius: 0 }} title="Close all" onClick={() => ui.set({ dock: [], dockActive: null })}>✕</button>
      </div>
      <div className="dock-body" style={{ height: min ? 0 : h, overflow: "hidden" }}>
        {dock.map((t) => (
          <div key={t.id} className="col" style={{ display: t.id === act?.id ? "flex" : "none", height: "100%" }}>
            {t.type === "logs" ? <LogsPane tab={t} /> : <TermPane tab={t} visible={t.id === act?.id && !min} />}
          </div>
        ))}
      </div>
    </div>
  );
}
