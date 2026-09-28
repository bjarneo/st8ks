// Thin typed access to the Wails runtime and the Go App methods.

import type { Snapshot } from "./types";

type Fn = (...args: unknown[]) => Promise<unknown>;

declare global {
  interface Window {
    go?: { main?: { App?: Record<string, Fn> } };
    runtime?: {
      EventsOn: (name: string, cb: (...data: unknown[]) => void) => () => void;
      ClipboardSetText: (text: string) => Promise<boolean>;
      BrowserOpenURL: (url: string) => void;
      WindowToggleMaximise?: () => void;
    };
  }
}

/** call invokes a bound Go method on App. */
export function call<T>(method: string, ...args: unknown[]): Promise<T> {
  const fn = window.go?.main?.App?.[method];
  if (!fn) return Promise.reject(new Error(`backend method ${method} is not available`));
  return fn(...args) as Promise<T>;
}

/** on subscribes to a backend event. It returns the unsubscribe function. */
export function on<T>(name: string, cb: (data: T) => void): () => void {
  if (!window.runtime) return () => {};
  return window.runtime.EventsOn(name, (data: unknown) => cb(data as T));
}

/** fetchTable loads a full table over the asset server. */
export async function fetchTable(kind: string): Promise<Snapshot> {
  const res = await fetch(`/_st8ks/table?kind=${encodeURIComponent(kind)}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`table ${kind}: ${res.status}`);
  return (await res.json()) as Snapshot;
}

export function copyText(text: string): Promise<boolean> {
  if (window.runtime?.ClipboardSetText) return window.runtime.ClipboardSetText(text);
  return navigator.clipboard.writeText(text).then(() => true, () => false);
}

export function errText(e: unknown): string {
  if (e instanceof Error) return e.message;
  if (typeof e === "string") return e;
  return String(e);
}

const enc = new TextEncoder();

export function toBase64(s: string): string {
  const bytes = enc.encode(s);
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin);
}

export function fromBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
