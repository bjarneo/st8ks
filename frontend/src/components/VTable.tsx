import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { flushSync } from "react-dom";
import { scale, ui } from "../lib/app";
import { age, bytes, clockStore, cpu, pctColor, rel, toneVar } from "../lib/format";
import { useStore } from "../lib/store";
import type { Col, Row } from "../lib/types";


export function useNow(): number {
  return useSyncExternalStore(clockStore.subscribe, clockStore.get, clockStore.get);
}

/** useScale returns the text size factor from the settings. */
export function useScale(): number {
  return useStore(ui, (s) => scale(s.settings));
}

/** useRowHeight returns the row height in pixels. It matches --rh. */
export function useRowHeight(): number {
  const z = useScale();
  const comfortable = useStore(ui, (s) => s.settings?.density === "comfortable");
  return Math.round((comfortable ? 36 : 28) * z);
}

function BarCell({ v, cap, text }: { v: number | undefined; cap: number; text: string }) {
  const p = v !== undefined && cap > 0 ? Math.round((v * 100) / cap) : -1;
  return (
    <>
      <div className="minibar">{p >= 0 && <div style={{ width: `${Math.min(100, p)}%`, background: pctColor(p) }} />}</div>
      <span className="bartxt">{text}</span>
    </>
  );
}

export const Cell = memo(function Cell({ c, r, i: pos, m, now }: { c: Col; r: Row; i: number; m?: [number, number]; now: number }) {
  const i = c.i ?? pos;
  const tone = r.k[i];
  switch (c.t) {
    case "name":
      return <div className="cell" title={r.m}><span className="t nm">{r.m}</span></div>;
    case "ns":
      return <div className="cell"><span className="t mono">{r.n}</span></div>;
    case "age":
      return <div className="cell"><span className="t mono" style={{ color: "var(--fa)" }}>{age(r.t, now)}</span></div>;
    case "status":
      return (
        <div className="cell" title={r.c[i]}>
          <span className="dot sm" style={{ background: toneVar(tone, "var(--fa)") }} />
          <span className="t" style={{ color: toneVar(tone, "var(--mu)") }}>{r.c[i]}</span>
        </div>
      );
    case "cpu": {
      const cap = r.x?.[0] ?? 0;
      return <div className="cell"><BarCell v={m?.[0]} cap={cap} text={m ? cpu(m[0]) : "—"} /></div>;
    }
    case "mem": {
      const cap = r.x?.[1] ?? 0;
      return <div className="cell"><BarCell v={m?.[1]} cap={cap} text={m ? bytes(m[1]) : "—"} /></div>;
    }
    case "cpupct": {
      const cap = r.x?.[0] ?? 0;
      const p = m && cap ? Math.round((m[0] * 100) / cap) : -1;
      return <div className="cell"><BarCell v={m?.[0]} cap={cap} text={p >= 0 ? p + "%" : "—"} /></div>;
    }
    case "mempct": {
      const cap = r.x?.[1] ?? 0;
      const p = m && cap ? Math.round((m[1] * 100) / cap) : -1;
      return <div className="cell"><BarCell v={m?.[1]} cap={cap} text={p >= 0 ? p + "%" : "—"} /></div>;
    }
    case "time": {
      const v = parseInt(r.c[i] ?? "", 10);
      return <div className="cell"><span className="t mono" style={{ color: "var(--fa)" }}>{v ? rel(v, now) : "—"}</span></div>;
    }
    case "mono": case "ready": case "num":
      return <div className="cell" title={r.c[i]}><span className="t mono" style={tone && tone !== "-" ? { color: toneVar(tone) } : undefined}>{r.c[i]}</span></div>;
    default:
      return <div className="cell" title={r.c[i]}><span className="t" style={tone && tone !== "-" ? { color: toneVar(tone) } : undefined}>{r.c[i]}</span></div>;
  }
});

interface RowProps {
  r: Row;
  cols: Col[];
  grid: string;
  y: number;
  chk: boolean;
  act: boolean;
  cur: boolean;
  m0?: number;
  m1?: number;
  now: number;
  onOpen: (r: Row) => void;
  onCheck?: (r: Row) => void;
}

const TRow = memo(function TRow({ r, cols, grid, y, chk, act, cur, m0, m1, now, onOpen, onCheck }: RowProps) {
  const m: [number, number] | undefined = m0 === undefined ? undefined : [m0, m1 ?? 0];
  return (
    <div className={"trow" + (act ? " act" : chk ? " sel" : "") + (cur ? " cur" : "")} style={{ gridTemplateColumns: grid, transform: `translateY(${y}px)` }} onClick={() => onOpen(r)}>
      {onCheck && (
        <div className={"chk" + (chk ? " on" : "")} style={{ marginLeft: 6 }} onClick={(e) => {
          e.stopPropagation();
          onCheck(r);
        }}>{chk ? "✓" : ""}</div>
      )}
      {cols.map((c, i) => <Cell key={i} c={c} r={r} i={i} m={m} now={now} />)}
    </div>
  );
});

export interface VTableProps {
  rows: Row[];
  cols: Col[];
  metrics?: Map<string, [number, number]>;
  isChecked?: (r: Row) => boolean;
  isActive?: (r: Row) => boolean;
  onOpen: (r: Row) => void;
  onCheck?: (r: Row) => void;
  header?: ReactNode;
  cursor?: number;
  minWidth?: number;
  empty?: ReactNode;
}

/** VTable renders only the visible rows. Rows have a fixed height, so the
 * visible range comes from the scroll offset without measuring. */
export function VTable({ rows, cols, metrics, isChecked, isActive, onOpen, onCheck, header, cursor = -1, minWidth = 880, empty }: VTableProps) {
  const ref = useRef<HTMLDivElement>(null);
  const rh = useRowHeight();
  const z = useScale();
  // The header height matches --hh.
  const head = Math.round(30 * z);
  const now = useNow();
  const [view, setView] = useState({ top: 0, h: 800 });
  const raf = useRef(0);
  const onScroll = useCallback(() => {
    if (raf.current) return;
    raf.current = requestAnimationFrame(() => {
      raf.current = 0;
      const el = ref.current;
      // Render in this frame, before the paint, so that new rows do not show one frame late.
      if (el) flushSync(() => setView({ top: el.scrollTop, h: el.clientHeight }));
    });
  }, []);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setView({ top: el.scrollTop, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  useEffect(() => {
    const el = ref.current;
    if (!el || cursor < 0) return;
    const y = head + cursor * rh;
    if (y < el.scrollTop + head) el.scrollTop = y - head;
    else if (y + rh > el.scrollTop + el.clientHeight) el.scrollTop = y + rh - el.clientHeight;
  }, [cursor, rh, head]);
  // Pixel column widths grow with the text size.
  const px = (w: string) => (z === 1 ? w : w.replace(/(\d+(?:\.\d+)?)px/g, (_, n: string) => Math.round(Number(n) * z) + "px"));
  const grid = (onCheck ? px("34px") + " " : "") + cols.map((c) => px(c.w)).join(" ");
  // The browser clamps the scroll offset when the list gets shorter, but the scroll event comes a frame later.
  const top = Math.min(view.top, Math.max(0, head + rows.length * rh + 40 - view.h));
  // Render one screen of extra rows on each side, so that a fast scroll does not show empty space.
  const over = Math.max(8, Math.ceil(view.h / rh));
  const start = Math.max(0, Math.floor((top - head) / rh) - over);
  const end = Math.min(rows.length, Math.ceil((top + view.h) / rh) + over);
  const slice = rows.slice(start, end);
  return (
    <div className="vwrap" ref={ref} onScroll={onScroll}>
      <div style={{ position: "relative", minWidth: Math.round(minWidth * z), height: head + rows.length * rh + 40 }}>
        <div className="thead" style={{ gridTemplateColumns: grid }}>{header}</div>
        <div style={{ position: "absolute", top: head, left: 0, right: 0 }}>
          {slice.map((r, k) => {
            const i = start + k;
            const m = metrics?.get(r.u);
            return (
              <TRow key={r.u} r={r} cols={cols} grid={grid} y={i * rh} chk={isChecked ? isChecked(r) : false} act={isActive ? isActive(r) : false}
                cur={i === cursor} m0={m?.[0]} m1={m?.[1]} now={now} onOpen={onOpen} onCheck={onCheck} />
            );
          })}
        </div>
        {!rows.length && empty && <div style={{ position: "absolute", top: head, left: 0, right: 0 }}>{empty}</div>}
      </div>
    </div>
  );
}
