import { useCallback, useEffect, useState } from "react";
import { call, copyText } from "../lib/bridge";
import { fail, say } from "../lib/app";
import type { ClaudeInfo } from "../lib/types";

/** useClaudeInfo finds the claude program and the command that adds st8ks
 * to Claude Code. */
export function useClaudeInfo(): [ClaudeInfo | null, () => void] {
  const [info, setInfo] = useState<ClaudeInfo | null>(null);
  const load = useCallback(() => {
    call<ClaudeInfo>("ClaudeInfo").then(setInfo, fail);
  }, []);
  useEffect(load, [load]);
  return [info, load];
}

/** CopyCmd shows a command with a Copy button. */
export function CopyCmd({ cmd }: { cmd: string }) {
  return (
    <div className="flex" style={{ gap: 6, alignItems: "flex-start" }}>
      <code className="input mono grow" style={{ height: "auto", minHeight: 26, padding: "4px 8px", fontSize: "calc(11.5px * var(--zf))", lineHeight: 1.6, wordBreak: "break-all", userSelect: "text", WebkitUserSelect: "text" }}>{cmd}</code>
      <button className="btn" onClick={() => copyText(cmd).then((ok) => say(ok ? "Copied" : "The clipboard is not available"), fail)}>Copy</button>
    </div>
  );
}

/** ClaudeMissing tells how to install Claude Code. */
export function ClaudeMissing({ info, retry }: { info: ClaudeInfo; retry?: () => void }) {
  return (
    <div className="col" style={{ gap: 8 }}>
      <span>claude is not on the PATH of your login shell. To install Claude Code, run this command:</span>
      <CopyCmd cmd={info.install} />
      {retry && <button className="btn sm" style={{ alignSelf: "flex-start" }} onClick={retry}>Try again</button>}
    </div>
  );
}

/** ClaudeSetup is the Claude Code row of Settings > Assistant. */
export function ClaudeSetup() {
  const [info, reload] = useClaudeInfo();
  if (!info) return <span className="fa small">Looking for claude…</span>;
  return (
    <div className="col" style={{ gap: 8 }}>
      {!info.supported && <span className="small fa" style={{ lineHeight: 1.45 }}>{info.reason}</span>}
      {info.supported && (info.path
        ? <span className="small fa mono" style={{ wordBreak: "break-all" }}>Found {info.path}</span>
        : <ClaudeMissing info={info} retry={reload} />)}
      <span className="small fa" style={{ lineHeight: 1.45 }}>To use the st8ks tools in Claude Code outside st8ks, run this command once. The server then reads your current kubeconfig context.</span>
      <CopyCmd cmd={info.addCmd} />
    </div>
  );
}
