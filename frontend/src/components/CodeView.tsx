import { lazy, Suspense } from "react";
import type { Props } from "./CodeEditor";

// CodeMirror loads on first use, so the app starts faster.
const Editor = lazy(() => import("./CodeEditor"));

export function CodeView(p: Props) {
  return (
    <Suspense fallback={<div className="empty flex"><span className="spinner" />Loading editor…</div>}>
      <Editor {...p} />
    </Suspense>
  );
}
