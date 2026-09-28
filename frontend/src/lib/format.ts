// Formatting helpers shared by all views.

export function dur(sec: number): string {
  if (sec < 0) sec = 0;
  if (sec < 60) return `${Math.floor(sec)}s`;
  if (sec < 600) return `${Math.floor(sec / 60)}m${Math.floor(sec % 60)}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 8 * 3600) {
    const m = Math.floor((sec % 3600) / 60);
    return m ? `${Math.floor(sec / 3600)}h${m}m` : `${Math.floor(sec / 3600)}h`;
  }
  if (sec < 48 * 3600) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}

/** age formats the time since a unix timestamp. */
export function age(unix: number, now: number): string {
  if (!unix) return "—";
  return dur(now - unix);
}

/** rel formats a unix timestamp relative to now, in the past or future. */
export function rel(unix: number, now: number): string {
  if (!unix) return "—";
  const d = now - unix;
  if (d >= 0) return dur(d);
  return "in " + dur(-d);
}

export function cpu(m: number): string {
  if (m < 1000) return `${Math.round(m)}m`;
  return `${(m / 1000).toFixed(m < 10000 ? 2 : 1).replace(/\.?0+$/, "")}`;
}

export function bytes(b: number): string {
  const k = 1024;
  if (b >= k ** 4) return `${(b / k ** 4).toFixed(1).replace(/\.0$/, "")}Ti`;
  if (b >= k ** 3) return `${(b / k ** 3).toFixed(1).replace(/\.0$/, "")}Gi`;
  if (b >= k ** 2) return `${Math.round(b / k ** 2)}Mi`;
  if (b >= k) return `${Math.round(b / k)}Ki`;
  return `${b}`;
}

export function cores(m: number): string {
  const c = m / 1000;
  return c >= 10 ? c.toFixed(0) : c.toFixed(1).replace(/\.0$/, "");
}

export function gib(b: number): string {
  const g = b / 1024 ** 3;
  return g >= 10 ? g.toFixed(0) : g.toFixed(1).replace(/\.0$/, "");
}

/** tone maps a backend tone byte to a CSS color variable. */
export function toneVar(t: string | undefined, fallback = "var(--mu)"): string {
  switch (t) {
    case "o": return "var(--ok)";
    case "w": return "var(--wa)";
    case "e": return "var(--er)";
    case "m": return "var(--fa)";
  }
  return fallback;
}

export function pctColor(p: number): string {
  return p > 90 ? "var(--er)" : p > 75 ? "var(--wa)" : "var(--ac)";
}

export function singular(k: string): string {
  if (k === "Endpoints") return "Endpoints";
  if (k.endsWith("Classes") || k.endsWith("Ingresses")) return k.slice(0, -2);
  if (k.endsWith("Policies") || k.endsWith("ies")) return k.slice(0, -3) + "y";
  return k.replace(/s$/, "");
}

export function plural(n: number, one: string, many = one + "s"): string {
  return `${n} ${n === 1 ? one : many}`;
}

let clock = Math.floor(Date.now() / 1000);
const clockSubs = new Set<() => void>();
setInterval(() => {
  clock = Math.floor(Date.now() / 1000);
  clockSubs.forEach((f) => f());
}, 5000);

/** A shared clock that ticks every 5 seconds, for relative times. */
export const clockStore = {
  get: () => clock,
  subscribe(fn: () => void) {
    clockSubs.add(fn);
    return () => {
      clockSubs.delete(fn);
    };
  },
};
