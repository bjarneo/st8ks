// Mirrors of the Go types in internal/kube/prometheus.go.

export type MetricsStatus = "ok" | "none" | "forbidden" | "nodata" | "error";
export type MetricsRange = "1h" | "6h" | "24h" | "7d";

export interface PromSource {
  status: MetricsStatus; mode: "auto" | "service" | "url"; flavor?: string; target?: string; via?: "proxy" | "direct";
  message?: string; missing?: string; searched?: string[];
}
export interface MetricLine { kind: "request" | "limit"; label: string; v: number }
export interface MetricSeries { name: string; v: (number | null)[]; lines?: MetricLine[] }
export interface MetricPanel { total?: MetricSeries; parts: MetricSeries[]; more: number }
export interface MetricsData {
  status: MetricsStatus; message?: string; source: PromSource; range: MetricsRange;
  start: number; step: number; n: number; parts?: "container" | "pod"; cpu: MetricPanel; mem: MetricPanel;
}

export const METRIC_RANGES: MetricsRange[] = ["1h", "6h", "24h", "7d"];

const KINDS = new Set(["Pods", "Deployments", "StatefulSets", "DaemonSets", "Nodes"]);

/** hasMetrics tells if the detail panel of a kind has a Metrics tab. */
export function hasMetrics(kind: string): boolean {
  return KINDS.has(kind);
}
