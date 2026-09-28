// Live resource tables. Each kind has one store that holds its rows in a
// Map. Deltas from the backend change the Map in place, and subscribers get
// one notification per animation frame at most.

import { useCallback, useSyncExternalStore } from "react";
import { call, fetchTable, on } from "./bridge";
import type { Delta, Row } from "./types";

export class Table {
  rows = new Map<string, Row>();
  metrics = new Map<string, [number, number]>();
  version = 0;
  synced = false;
  err = "";
  available = true;
  state: "empty" | "loading" | "ready" = "empty";
  loadedAt = 0;
  private v = -1;
  private queue: Delta[] = [];
  private subs = new Set<() => void>();
  private pending = false;
  private loadSeq = 0;

  constructor(public kind: string) {}

  subscribe = (fn: () => void) => {
    this.subs.add(fn);
    return () => {
      this.subs.delete(fn);
    };
  };

  getVersion = () => this.version;

  private notify() {
    if (this.pending) return;
    this.pending = true;
    requestAnimationFrame(() => {
      this.pending = false;
      this.version++;
      this.subs.forEach((f) => f());
    });
  }

  async load(): Promise<void> {
    const seq = ++this.loadSeq;
    this.state = "loading";
    this.queue = [];
    try {
      const s = await fetchTable(this.kind);
      if (seq !== this.loadSeq) return;
      this.rows = new Map(s.rows.map((r) => [r.u, r]));
      this.v = s.v;
      this.synced = s.synced;
      this.err = s.err ?? "";
      this.available = s.available;
      if (s.metrics) this.metrics = new Map(Object.entries(s.metrics));
      this.state = "ready";
      this.loadedAt = Date.now();
      const q = this.queue;
      this.queue = [];
      for (const d of q) this.apply(d);
    } catch (e) {
      if (seq !== this.loadSeq) return;
      this.state = "empty";
      this.err = e instanceof Error ? e.message : String(e);
    }
    this.notify();
  }

  apply(d: Delta) {
    if (this.state === "loading") {
      this.queue.push(d);
      return;
    }
    if (this.state !== "ready" || d.v <= this.v) return;
    this.v = d.v;
    if (d.up) for (const r of d.up) this.rows.set(r.u, r);
    if (d.del) for (const u of d.del) this.rows.delete(u);
    this.synced = d.synced;
    this.err = d.err ?? "";
    this.notify();
  }

  setMetrics(m: Record<string, [number, number]>) {
    this.metrics = new Map(Object.entries(m));
    this.notify();
  }

  reset() {
    this.loadSeq++;
    this.rows = new Map();
    this.metrics = new Map();
    this.state = "empty";
    this.v = -1;
    this.synced = false;
    this.err = "";
    this.queue = [];
    this.notify();
  }
}

const tables = new Map<string, Table>();
let watched = new Set<string>();

export function table(kind: string): Table {
  let t = tables.get(kind);
  if (!t) {
    t = new Table(kind);
    tables.set(kind, t);
  }
  return t;
}

/** useTable re-renders the component when the table changes. */
export function useTable(kind: string): Table {
  const t = table(kind);
  useSyncExternalStore(t.subscribe, t.getVersion, t.getVersion);
  return t;
}

/** useTables re-renders when any of the tables changes. */
export function useTables(kinds: string[]): number {
  const key = kinds.join("|");
  const sub = useCallback(
    (fn: () => void) => {
      const offs = key.split("|").filter(Boolean).map((k) => table(k).subscribe(fn));
      return () => offs.forEach((o) => o());
    },
    [key],
  );
  const snap = useCallback(() => key.split("|").filter(Boolean).reduce((a, k) => (a * 31 + table(k).version) | 0, 7), [key]);
  return useSyncExternalStore(sub, snap, snap);
}

export function resetTables() {
  watched = new Set();
  for (const t of tables.values()) t.reset();
}

let watchSeq = 0;
let watchChain: Promise<void> = Promise.resolve();

/** setWatched tells the backend which kinds to stream. Kinds that start to
 * be watched load a fresh snapshot, because they missed changes before.
 * Calls run one after another, and a call that a newer one replaced does
 * nothing, so the backend never ends up with an old set. */
export function setWatched(kinds: string[]): Promise<void> {
  const seq = ++watchSeq;
  watchChain = watchChain.then(async () => {
    if (seq !== watchSeq) return;
    const next = new Set(kinds);
    const added = kinds.filter((k) => !watched.has(k) || table(k).state === "empty");
    for (const k of watched) {
      if (!next.has(k)) table(k).state = table(k).state === "ready" ? "empty" : table(k).state;
    }
    watched = next;
    await call("Watch", kinds).catch(() => {});
    await Promise.all(added.map((k) => table(k).load()));
  });
  return watchChain;
}

/** ensureLoaded reads a table without a live watch. A copy older than
 * maxAge milliseconds loads again. */
export function ensureLoaded(kind: string, maxAge = 10_000): Promise<void> {
  const t = table(kind);
  if (t.state === "loading" || watched.has(kind)) return Promise.resolve();
  if (t.state === "ready" && Date.now() - t.loadedAt < maxAge) return Promise.resolve();
  return t.load();
}

/** reloadTable drops a table and loads it again if it is watched. */
export async function reloadTable(kind: string) {
  const t = table(kind);
  t.reset();
  if (watched.has(kind)) await t.load();
}

export function isWatched(kind: string): boolean {
  return watched.has(kind);
}

on<Delta>("rows", (d) => {
  const t = tables.get(d.kind);
  if (t && watched.has(d.kind)) t.apply(d);
});

on<{ pods?: Record<string, [number, number]>; nodes?: Record<string, [number, number]> }>("metrics", (m) => {
  if (m.pods) table("Pods").setMetrics(m.pods);
  if (m.nodes) table("Nodes").setMetrics(m.nodes);
});
