import { useMemo } from "react";
import { goList, nsVisible, ui } from "../lib/app";
import { shallowEqual, useStore } from "../lib/store";
import { sumCounts } from "./Tree";

const ARGO = ["applications.argoproj.io"];
const FLUX = [
  "kustomizations.kustomize.toolkit.fluxcd.io", "helmreleases.helm.toolkit.fluxcd.io", "gitrepositories.source.toolkit.fluxcd.io",
  "ocirepositories.source.toolkit.fluxcd.io", "helmrepositories.source.toolkit.fluxcd.io", "helmcharts.source.toolkit.fluxcd.io",
  "buckets.source.toolkit.fluxcd.io",
];

interface Cell { label: string; total: number; failing: number; error: boolean; kind: string }

/** GitOpsHealth is a compact row of Argo CD and Flux counts under the
 * workload summary of the overview. A failing object is an object with an
 * issue. The row shows only when the cluster has objects of these kinds. */
export function GitOpsHealth() {
  const counts = useStore(ui, (s) => s.counts);
  const kinds = useStore(ui, (s) => s.kinds);
  const nsSel = useStore(ui, (s) => s.nsSel, shallowEqual);
  const issues = useStore(ui, (s) => s.issues);
  const cells = useMemo(() => {
    const cell = (label: string, list: string[]): Cell => {
      const served = list.filter((k) => kinds[k]);
      let total = 0;
      let first = "";
      for (const k of served) {
        const [n] = sumCounts(counts, k, nsSel);
        total += n;
        if (n && !first) first = k;
      }
      const bad = issues.filter((i) => served.includes(i.kind) && nsVisible(i.ns, nsSel));
      return { label, total, failing: Math.min(bad.length, total), error: bad.some((i) => i.sev === "er"), kind: bad[0]?.kind ?? first };
    };
    return [cell("Argo CD apps", ARGO), cell("Flux objects", FLUX)].filter((c) => c.total > 0);
  }, [counts, kinds, nsSel, issues]);
  if (!cells.length) return null;
  return (
    <div className="health" style={{ gridTemplateColumns: `repeat(${cells.length}, minmax(150px, 200px))`, alignSelf: "flex-start", marginTop: -12 }}>
      {cells.map((c) => {
        const tone = c.failing ? (c.error ? "var(--er)" : "var(--wa)") : undefined;
        return (
          <div key={c.label} onClick={() => goList(c.kind, c.failing ? "Unhealthy" : "All")}>
            <span className="mu">{c.label}</span>
            <span className="v" style={{ color: tone ?? "var(--tx)" }}>{c.total - c.failing}/{c.total}</span>
            <span className="small" style={{ color: tone ?? "var(--fa)" }}>{c.failing ? `${c.failing} failing` : "all healthy"}</span>
          </div>
        );
      })}
    </div>
  );
}
