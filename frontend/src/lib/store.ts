import { useCallback, useRef, useSyncExternalStore } from "react";

export interface Store<T> {
  get(): T;
  set(patch: Partial<T> | ((s: T) => Partial<T>)): void;
  subscribe(fn: () => void): () => void;
}

/** createStore makes a small external store. A component re-renders only
 * when the value its selector returns changes. */
export function createStore<T extends object>(init: T): Store<T> {
  let state = init;
  const subs = new Set<() => void>();
  return {
    get: () => state,
    set(patch) {
      const next = typeof patch === "function" ? patch(state) : patch;
      let changed = false;
      for (const k in next) {
        if (!Object.is(state[k], next[k])) {
          changed = true;
          break;
        }
      }
      if (!changed) return;
      state = { ...state, ...next };
      subs.forEach((f) => f());
    },
    subscribe(fn) {
      subs.add(fn);
      return () => {
        subs.delete(fn);
      };
    },
  };
}

export function shallowEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || typeof b !== "object" || !a || !b) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const ka = Object.keys(a), kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) {
    if (!Object.is((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k])) return false;
  }
  return true;
}

export function useStore<T, S>(store: Store<T>, sel: (s: T) => S, eq: (a: S, b: S) => boolean = Object.is): S {
  const last = useRef<{ s: S } | null>(null);
  const selRef = useRef(sel);
  selRef.current = sel;
  const get = useCallback(() => {
    const next = selRef.current(store.get());
    if (last.current && eq(last.current.s, next)) return last.current.s;
    last.current = { s: next };
    return next;
  }, [store, eq]);
  return useSyncExternalStore(store.subscribe, get, get);
}
