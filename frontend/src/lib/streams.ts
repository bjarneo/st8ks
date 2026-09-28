// Routes log and terminal events to the pane that owns the stream. Chunks
// that arrive before the pane registers are kept for a short time, because
// the backend can emit before the start call returns.

import { on } from "./bridge";
import type { AiChunk, ExecChunk, LogChunk } from "./types";

function router<T extends { id: string }>(event: string) {
  const handlers = new Map<string, (c: T) => void>();
  const pending = new Map<string, { at: number; chunks: T[] }>();
  on<T>(event, (c) => {
    const h = handlers.get(c.id);
    if (h) return h(c);
    const p = pending.get(c.id) ?? { at: Date.now(), chunks: [] };
    p.chunks.push(c);
    pending.set(c.id, p);
  });
  setInterval(() => {
    const cutoff = Date.now() - 15000;
    for (const [id, p] of pending) if (p.at < cutoff) pending.delete(id);
  }, 5000);
  return {
    listen(id: string, h: (c: T) => void): () => void {
      handlers.set(id, h);
      const p = pending.get(id);
      if (p) {
        pending.delete(id);
        p.chunks.forEach(h);
      }
      return () => {
        handlers.delete(id);
      };
    },
  };
}

export const logRouter = router<LogChunk>("log");
export const execRouter = router<ExecChunk>("exec");
export const aiRouter = router<AiChunk>("ai");
