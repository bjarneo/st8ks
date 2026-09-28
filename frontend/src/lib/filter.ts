// Filtering and sorting of table rows.

import type { Col, Row } from "./types";

const hayCache = new WeakMap<Row, string>();

function hay(r: Row): string {
  let h = hayCache.get(r);
  if (h === undefined) {
    h = (r.m + " " + (r.n ?? "") + " " + r.c.join(" ") + " " + (r.l ?? "")).toLowerCase();
    hayCache.set(r, h);
  }
  return h;
}

const alias: Record<string, string> = { ns: "namespace", status: "status", node: "node", type: "type", reason: "reason" };

/** compileQuery turns a filter such as "ns:prod status:crash app=web" into
 * a predicate. Tokens combine with AND. A leading "-" negates a token. */
export function compileQuery(q: string, cols: Col[]): ((r: Row) => boolean) | null {
  const toks = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (!toks.length) return null;
  const tests = toks.map((raw) => {
    const neg = raw.startsWith("-") && raw.length > 1;
    const tok = neg ? raw.slice(1) : raw;
    let test: (r: Row) => boolean;
    const m = tok.match(/^([a-z][a-z-]*):(.*)$/);
    if (m) {
      const key = alias[m[1]] ?? m[1];
      const val = m[2];
      if (key === "namespace") test = (r) => (r.n ?? "").toLowerCase().includes(val);
      else if (key === "name") test = (r) => r.m.toLowerCase().includes(val);
      else if (key === "label" || key === "l") test = (r) => (r.l ?? "").toLowerCase().split(" ").some((kv) => kv.includes(val));
      else {
        const ci = cols.findIndex((c) => c.l.toLowerCase().replace(/[^a-z]/g, "") === key.replace(/[^a-z]/g, ""));
        test = ci >= 0 ? (r) => (r.c[ci] ?? "").toLowerCase().includes(val) : (r) => hay(r).includes(tok);
      }
    } else if (tok.includes("=")) {
      test = (r) => (" " + (r.l ?? "").toLowerCase() + " ").includes(" " + tok + " ") || (r.l ?? "").toLowerCase().includes(tok);
    } else {
      test = (r) => hay(r).includes(tok);
    }
    return neg ? (r: Row) => !test(r) : test;
  });
  return (r) => {
    for (const t of tests) if (!t(r)) return false;
    return true;
  };
}

// natKey makes a string that sorts like a natural sort with plain "<":
// numbers get padded, so "pod-2" comes before "pod-10".
const keyCache = new WeakMap<Row, (string | undefined)[]>();
function natKey(r: Row, i: number, s: string): string {
  let arr = keyCache.get(r);
  if (!arr) keyCache.set(r, (arr = []));
  let k = arr[i];
  if (k === undefined) {
    k = s.toLowerCase().replace(/\d+/g, (d) => d.padStart(12, "0"));
    arr[i] = k;
  }
  return k;
}

function ratioVal(s: string): number {
  const m = s.match(/^(\d+)\/(\d+)$/);
  if (!m) return parseFloat(s) || 0;
  const b = +m[2];
  return b ? +m[1] / b : 0;
}

/** sortRows sorts in place by one column. */
export function sortRows(rows: Row[], cols: Col[], i: number, dir: 1 | -1, metrics: Map<string, [number, number]>) {
  const c = cols[i];
  if (!c) return rows;
  let key: (r: Row) => number | string;
  switch (c.t) {
    case "name": key = (r) => natKey(r, -1, r.m); break;
    case "ns": key = (r) => natKey(r, -2, r.n ?? ""); break;
    case "age": key = (r) => -r.t; break;
    case "num": case "time": key = (r) => parseFloat(r.c[i]) || 0; break;
    case "ready": key = (r) => ratioVal(r.c[i] ?? ""); break;
    case "cpu": case "cpupct": key = (r) => metrics.get(r.u)?.[0] ?? -1; break;
    case "mem": case "mempct": key = (r) => metrics.get(r.u)?.[1] ?? -1; break;
    default: key = (r) => natKey(r, i, r.c[i] ?? "");
  }
  const keyed = rows.map((r) => [key(r), r] as const);
  keyed.sort((a, b) => {
    const x = a[0], y = b[0];
    const d = typeof x === "number" && typeof y === "number" ? x - y : x < y ? -1 : x > y ? 1 : 0;
    return d === 0 ? (a[1].m < b[1].m ? -1 : a[1].m > b[1].m ? 1 : 0) : d * dir;
  });
  for (let k = 0; k < rows.length; k++) rows[k] = keyed[k][1];
  return rows;
}

/** defaultSort orders by namespace and name. It uses plain string order,
 * which is much faster than a collator on large tables. */
export function defaultSort(rows: Row[]) {
  rows.sort((a, b) => {
    const x = a.n ?? "", y = b.n ?? "";
    if (x !== y) return x < y ? -1 : 1;
    return a.m < b.m ? -1 : a.m > b.m ? 1 : 0;
  });
  return rows;
}
