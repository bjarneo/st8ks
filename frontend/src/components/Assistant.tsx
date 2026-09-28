import { useEffect, useRef, useState } from "react";
import { call, errText } from "../lib/bridge";
import { nsVisible, openDetail, runIssueAction, ui } from "../lib/app";
import { shallowEqual, useStore } from "../lib/store";
import { aiRouter } from "../lib/streams";
import type { AiMsg, AskStart } from "../lib/types";

interface Conv { msgs: AiMsg[]; cmds: string[]; busy: boolean; err: string; req: string }

// The last "Explain" request that started an analysis. It lives outside the
// component, because the panel mounts in the same update that asks.
let handledAuto = 0;

// Conversations live outside React so they survive closing the panel.
const convs = new Map<string, Conv>();
const listeners = new Set<() => void>();
const bump = () => listeners.forEach((f) => f());

function conv(key: string): Conv {
  let c = convs.get(key);
  if (!c) {
    c = { msgs: [], cmds: [], busy: false, err: "", req: "" };
    convs.set(key, c);
  }
  return c;
}

async function send(key: string, question: string) {
  const c = conv(key);
  if (c.busy) return;
  const history = c.msgs.slice();
  if (question) c.msgs = [...c.msgs, { role: "user", text: question }];
  c.msgs = [...c.msgs, { role: "assistant", text: "" }];
  c.busy = true;
  c.err = "";
  const req = Math.random().toString(36).slice(2);
  c.req = req;
  bump();
  const off = aiRouter.listen(req, (ch) => {
    if (c.req !== req) return;
    if (ch.delta) {
      const last = c.msgs[c.msgs.length - 1];
      c.msgs = [...c.msgs.slice(0, -1), { ...last, text: last.text + ch.delta }];
    }
    if (ch.done) {
      c.busy = false;
      if (ch.err) c.err = ch.err;
      const last = c.msgs[c.msgs.length - 1];
      if (last && last.role === "assistant" && !last.text) c.msgs = c.msgs.slice(0, -1);
      off();
    }
    bump();
  });
  try {
    const st = await call<AskStart>("Ask", req, key, history, question);
    c.cmds = [...new Set([...c.cmds, ...(st.cmds ?? [])])];
    bump();
  } catch (e) {
    off();
    c.busy = false;
    c.err = errText(e);
    c.msgs = c.msgs.slice(0, -1);
    bump();
  }
}

function useConv(key: string): Conv {
  const [, set] = useState(0);
  useEffect(() => {
    const f = () => set((n) => n + 1);
    listeners.add(f);
    return () => {
      listeners.delete(f);
    };
  }, []);
  return conv(key);
}

export function Assistant() {
  const issueId = useStore(ui, (s) => s.aiIssue);
  const auto = useStore(ui, (s) => s.aiAuto);
  const all = useStore(ui, (s) => s.issues);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const applied = useStore(ui, (s) => s.applied);
  const hasKey = useStore(ui, (s) => s.settings?.hasKey);
  const model = useStore(ui, (s) => s.settings?.assistantModel);
  const issues = all.filter((i) => nsVisible(i.ns, nsSel));
  const issue = issueId ? all.find((i) => i.id === issueId) : undefined;
  const key = issueId ?? "";
  const c = useConv(key);
  const [input, setInput] = useState("");
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (auto !== handledAuto) {
      handledAuto = auto;
      if (issueId && !conv(issueId).msgs.length) void send(issueId, "");
    }
  }, [auto, issueId]);
  useEffect(() => {
    const el = body.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [c.msgs]);

  const cmds = [...new Set([...(issue?.cmds ?? []), ...c.cmds])];
  const wasApplied = issueId ? applied[issueId] : 0;
  return (
    <div className="ai">
      <div className="ai-head">
        <span className="diamond" /><span style={{ fontWeight: 600 }}>Assistant</span>
        <span className="chip" style={{ fontFamily: "var(--sans)" }}>read-only</span>
        <span style={{ flex: 1 }} />
        {issueId && <button className="btn xs ghost mu" onClick={() => ui.set({ aiIssue: null })}>All issues</button>}
        <button className="btn icon sm ghost fa" onClick={() => ui.set({ ai: false })}>✕</button>
      </div>
      <div className="ai-body" ref={body}>
        {!issueId && (
          <>
            <span className="mu" style={{ lineHeight: 1.5 }}>
              I read cluster state, logs and events to explain failures. I never change anything. Every fix waits for your confirmation.
            </span>
            {!issues.length && <span className="ok">No open issues in this cluster.</span>}
            {issues.map((i) => (
              <div key={i.id} className="ai-issue" onClick={() => ui.set({ aiIssue: i.id })}>
                <span className="dot" style={{ background: i.sev === "er" ? "var(--er)" : "var(--wa)", marginTop: 5 }} />
                <div className="col" style={{ gap: 2 }}>
                  <span style={{ fontWeight: 500 }}>{i.title}</span>
                  <span className="meta-mono">{i.ns ? i.ns + " · " : ""}{i.reason}</span>
                </div>
              </div>
            ))}
          </>
        )}
        {issueId && !issue && (
          <span className="ok">{wasApplied ? "✓ Resolved. The issue no longer shows in the cluster." : "This issue is resolved or no longer exists."}</span>
        )}
        {issue && (
          <>
            <div className="col" style={{ gap: 4 }}>
              <span style={{ fontWeight: 600, fontSize: "calc(13px * var(--zf))" }}>{issue.title}</span>
              <a href="#" className="mono small" onClick={(e) => {
                e.preventDefault();
                openDetail({ kind: issue.kind, ns: issue.ns, name: issue.obj });
              }}>{issue.ns ? issue.ns + "/" : ""}{issue.obj}</a>
              {issue.pods && <span className="meta-mono">{issue.pods.length} pods: {issue.pods.slice(0, 4).join(", ")}{issue.pods.length > 4 ? " …" : ""}</span>}
            </div>
            <p style={{ margin: 0, lineHeight: 1.55 }}>{issue.summary}</p>
            {(issue.evidence ?? []).length > 0 && (
              <div className="col" style={{ gap: 6 }}>
                <span className="sec-label">Evidence</span>
                {(issue.evidence ?? []).filter(Boolean).map((e, k) => (
                  <div key={k} className="flex mu" style={{ alignItems: "flex-start", lineHeight: 1.45 }}><span className="fa">–</span><span style={{ wordBreak: "break-word" }}>{e}</span></div>
                ))}
              </div>
            )}
            <div className="fixbox">
              <span className="ac small" style={{ fontWeight: 500 }}>Suggested fix</span>
              <span style={{ lineHeight: 1.5 }}>{issue.fix}</span>
              {issue.action && !wasApplied && (
                <button className="btn sm primary" style={{ alignSelf: "flex-start" }} onClick={() => void runIssueAction(issue)}>{issue.action.label}</button>
              )}
              {!!wasApplied && <span className="ok">✓ Applied. Watching for recovery…</span>}
            </div>
            {cmds.length > 0 && (
              <div className="col" style={{ gap: 4 }}>
                <span className="sec-label">Read-only commands</span>
                {cmds.map((cmd) => <code key={cmd} className="mono mu" style={{ fontSize: "calc(10.5px * var(--zf))", lineHeight: 1.5, wordBreak: "break-all" }}>$ {cmd}</code>)}
              </div>
            )}
            {!c.msgs.length && !c.busy && (
              <button className="btn sm accent" style={{ alignSelf: "flex-start" }} onClick={() => void send(key, "")}>Ask Claude for a deeper analysis</button>
            )}
          </>
        )}
        {c.msgs.map((m, k) => (
          <div key={k} className={"msg" + (m.role === "user" ? " user" : "")}>
            <span className="fa small">{m.role === "user" ? "You" : "Claude"}</span>
            <span>{m.text || (c.busy && k === c.msgs.length - 1 ? <span className="flex fa"><span className="spinner" />Reading cluster data…</span> : "")}</span>
          </div>
        ))}
        {c.err && (
          <div className="errbox" style={{ fontFamily: "var(--sans)", fontSize: "calc(12px * var(--zf))" }}>
            {c.err}
            {!hasKey && <div style={{ marginTop: 8 }}><button className="btn xs" onClick={() => ui.set({ settingsOpen: true })}>Add API key</button></div>}
          </div>
        )}
      </div>
      <div className="ai-foot">
        <input className="input" value={input} disabled={c.busy} placeholder={issue ? "Ask a follow-up…" : "Ask about this cluster…"}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && input.trim()) {
              void send(key, input.trim());
              setInput("");
            }
          }} />
        <div className="fa small" style={{ marginTop: 6 }}>{model} · answers use cluster data, logs and events</div>
      </div>
    </div>
  );
}
