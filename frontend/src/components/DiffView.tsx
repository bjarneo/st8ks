import { useMemo, useState } from "react";
import { diffLines } from "diff";

interface Line { s: " " | "+" | "-"; l: string; a: number | ""; b: number | "" }

export function diffRows(a: string, b: string): Line[] {
  const out: Line[] = [];
  let ai = 1, bi = 1;
  for (const p of diffLines(a, b)) {
    const lines = p.value.replace(/\n$/, "").split("\n");
    for (const l of lines) {
      if (p.added) out.push({ s: "+", l, a: "", b: bi++ });
      else if (p.removed) out.push({ s: "-", l, a: ai++, b: "" });
      else out.push({ s: " ", l, a: ai++, b: bi++ });
    }
  }
  return out;
}

export function diffStat(rows: Line[]): string {
  let add = 0, del = 0;
  for (const r of rows) {
    if (r.s === "+") add++;
    else if (r.s === "-") del++;
  }
  return `+${add} −${del}`;
}

const CONTEXT = 4;

/** DiffView shows a unified diff. Long runs of unchanged lines fold. */
export function DiffView({ rows }: { rows: Line[] }) {
  const [open, setOpen] = useState<Record<number, boolean>>({});
  const blocks = useMemo(() => {
    const out: ({ t: "line"; r: Line } | { t: "fold"; start: number; lines: Line[] })[] = [];
    let i = 0;
    while (i < rows.length) {
      if (rows[i].s !== " ") {
        out.push({ t: "line", r: rows[i++] });
        continue;
      }
      let j = i;
      while (j < rows.length && rows[j].s === " ") j++;
      const run = rows.slice(i, j);
      const head = i === 0 ? 0 : CONTEXT;
      const tail = j === rows.length ? 0 : CONTEXT;
      if (run.length > head + tail + 2) {
        run.slice(0, head).forEach((r) => out.push({ t: "line", r }));
        out.push({ t: "fold", start: i + head, lines: run.slice(head, run.length - tail) });
        run.slice(run.length - tail).forEach((r) => out.push({ t: "line", r }));
      } else {
        run.forEach((r) => out.push({ t: "line", r }));
      }
      i = j;
    }
    return out;
  }, [rows]);
  return (
    <div className="diff">
      {blocks.map((b, k) => {
        if (b.t === "fold") {
          if (open[b.start]) return b.lines.map((r, x) => <DiffLine key={`${k}-${x}`} r={r} />);
          return (
            <div key={k} className="ln" style={{ cursor: "pointer", color: "var(--fa)", background: "var(--p2)" }} onClick={() => setOpen({ ...open, [b.start]: true })}>
              <span className="a" /><span className="b" /><span className="s">⋯</span><span>{b.lines.length} unchanged lines</span>
            </div>
          );
        }
        return <DiffLine key={k} r={b.r} />;
      })}
    </div>
  );
}

function DiffLine({ r }: { r: Line }) {
  return (
    <div className={"ln" + (r.s === "+" ? " add" : r.s === "-" ? " del" : "")}>
      <span className="a">{r.a}</span><span className="b">{r.b}</span>
      <span className="s" style={{ color: r.s === "+" ? "var(--ok)" : r.s === "-" ? "var(--er)" : "var(--fa)" }}>{r.s === " " ? "" : r.s}</span>
      <span>{r.l || " "}</span>
    </div>
  );
}
