// Mirrors of the Go types in internal/kube, internal/settings and app.go.

export type ColType =
  | "name" | "ns" | "age" | "text" | "mono" | "status" | "ready" | "num"
  | "cpu" | "mem" | "cpupct" | "mempct" | "time";

export interface Col { l: string; t: ColType; w: string; i?: number }

export interface Row {
  u: string; n?: string; m: string; t: number; c: string[]; k: string;
  b?: boolean; x?: number[]; l?: string;
}

export interface KindInfo {
  name: string; label: string; kind: string; api: string; group: string; version: string;
  resource: string; namespaced: boolean; cols: Col[]; actions: string[]; custom: boolean;
}

export interface TreeItem { label: string; kind?: string; view?: string }
export interface TreeSection { id: string; label: string; items: TreeItem[] }

export interface ContextInfo {
  name: string; cluster: string; server: string; user: string; namespace: string; source: string;
  status: string; error?: string; dist: string; version: string; region: string;
  nodes: number; pods: number; current: boolean;
}

export interface ClusterState {
  context: string; status: string; error?: string; server: string; dist: string;
  version: string; region: string; metrics: boolean;
}

export interface Snapshot {
  kind: string; v: number; rows: Row[]; synced: boolean; err?: string;
  metrics?: Record<string, [number, number]>; available: boolean;
}

export interface Delta { kind: string; v: number; up?: Row[]; del?: string[]; synced: boolean; err?: string }

export type Counts = Record<string, Record<string, [number, number]>>;

export interface Usage { used: number; alloc: number; req: number; lim: number; trend: number[] }
export interface Overview {
  cpu: Usage; mem: Usage; pods: number; podsCap: number; nodes: number; ready: number; cordoned: number; metrics: boolean;
}

export interface IssueAction { label: string; type: string; kind?: string; ns?: string; name?: string; container?: string; node?: string }
export interface Issue {
  id: string; sev: "er" | "wa"; title: string; kind: string; ns: string; obj: string; reason: string; meta: string;
  summary: string; evidence: string[]; fix: string; cmds: string[]; action?: IssueAction; pods?: string[];
}

export interface Ref { kind: string; ns: string; name: string }

export interface KV { k: string; v: string; t?: string }
export interface PortModel { port: number; proto: string; name?: string }
export interface ContainerModel {
  name: string; type: string; image: string; state: string; stateTone: string; running: boolean; last?: string;
  restarts: number; resources: string; ports: PortModel[]; probes: string; mounts: string;
}
export interface PodModel {
  node: string; ip: string; qos: string; sa: string; phase: string; restarts: number; conds: KV[]; containers: ContainerModel[];
}
export interface EventItem { type: string; reason: string; msg: string; count: number; last: number }
export interface Related { rel: string; kind: string; ns: string; name: string }
export interface ObjectDoc {
  kind: string; ns: string; name: string; uid: string; api: string; yaml: string; status: string; tone: string;
  created: number; labels: string[]; annotations: KV[]; summary: KV[]; pod?: PodModel; events: EventItem[];
  related: Related[]; owner?: Ref; managed?: Ref; replicas?: number; unschedulable: boolean; suspended: boolean; helm?: Ref;
}

export interface FixProposal { kind: string; ns: string; name: string; live: string; draft: string }

export interface Forward { id: string; context: string; ns: string; pod: string; remote: number; local: number; status: string; err?: string }

export interface LogOpts { ns: string; pod: string; container: string; previous: boolean; tail: number }
export interface LogChunk { id: string; lines?: string[]; end?: boolean; waiting?: boolean; err?: string }
export interface ExecOpts { ns: string; pod: string; container: string; attach: boolean; cols: number; rows: number }
export interface ExecChunk { id: string; data?: string; end?: boolean; err?: string }

export interface Subject { kind: string; name: string; ns?: string; bindings: number }
export interface RbacResource { name: string; group: string; cluster: boolean }
export interface RbacCell { a: boolean; p?: boolean; na?: boolean }
export interface RbacMatrix { resources: RbacResource[]; verbs: string[]; cells: RbacCell[][]; bindings: string[] }
export interface RbacAnswer { allowed: boolean; partial: boolean; why: string; cmd: string; verified: string }

export interface HelmRelease { name: string; ns: string; chart: string; app: string; rev: number; status: string; tone: string; updated: number }
export interface HelmRevision { rev: number; status: string; tone: string; chart: string; app: string; desc: string; updated: number }
export interface HelmDetail { release: HelmRelease; revisions: HelmRevision[]; values: string; notes: string }

export interface TopoNode { id: string; type: string; kind: string; ns: string; name: string; sub: string; tone: string; col: number; y: number }
export interface TopoEdge { a: string; b: string; t: "o" | "r" | "u" }
export interface Topology { ns: string; cols: string[]; nodes: TopoNode[]; edges: TopoEdge[]; height: number; truncated: boolean }

export interface SavedView { kind: string; query: string }
export interface Settings {
  theme: "dark" | "light"; density: "compact" | "comfortable"; detailLayout: "drawer" | "split" | "page";
  overviewStyle: "metrics" | "nodemap"; collapsed: Record<string, boolean>; savedViews: SavedView[];
  kubeconfigs: string[]; scanKubeDir: boolean; lastContext: string; nsByContext: Record<string, string[]>; protectedPattern: string;
  assistantModel: string; logTail: number; textSize: number; hasKey: boolean;
  mode: "cluster" | "ide"; ideWorkspace: string; ideRecent: string[]; ideHideInspector: boolean; ideLensOff: boolean;
}

export interface Source { path: string; kind: string; contexts: number; err?: string; removable: boolean; hidden?: boolean }
export interface InitState {
  settings: Settings; contexts: ContextInfo[]; sources: Source[]; explicit: boolean; initial: string; platform: string; loadErr: string;
  version: string;
}
export interface TreeState { tree: TreeSection[]; kinds: KindInfo[] }

export interface AiMsg { role: "user" | "assistant"; text: string }
export interface AiChunk { id: string; delta?: string; done?: boolean; err?: string }
export interface AskStart { id: string; cmds: string[] }

// ---- IDE (internal/ide) ----

export interface IdeChange { path: string; st: "M" | "A" | "D" | "R" | "U" }
export interface IdeState {
  root: string; name: string; git: boolean; branch: string; upstream: string; ahead: number; behind: number;
  files: string[]; changes: IdeChange[]; truncated: boolean; err?: string;
}
export interface IdeInit { state: IdeState | null; diags: Record<string, Diag[]>; recent: string[]; err?: string }
export type Sev = "error" | "warning" | "info";
export interface Diag { line: number; col: number; end: number; sev: Sev; code: string; msg: string; live?: boolean; fix?: string }
export interface FileData { text: string; head: string; hasHead: boolean }
export interface IdeRef { apiVersion: string; kind: string; ns: string; name: string }
export interface LiveStatus {
  found: boolean; kind: string; status: string; tone: string; rows: KV[];
  pods: { name: string; status: string; tone: string }[]; event: string; manager: string;
}
export interface Rendered { kind: string; name: string; ns: string; path: string }
export interface FieldInfo { path: string; type: string; doc: string; value: string; live: string; hasLive: boolean; same: boolean }
export interface Inspect {
  kind: string; name: string; apiVersion: string; where: string; doc: number; docs: number; crumbs: string[]; kust: boolean;
  renders: Rendered[] | null; renderErr: string; ref?: IdeRef; live?: LiveStatus; liveMsg: string; drift: number; field?: FieldInfo; schema: string;
}
export interface LiveDiff { text: string; found: number; objects: number; msg: string }
export interface OutLine { t: string; c?: string }
export interface IdeRun { ok: boolean; lines: OutLine[] }
export interface ApplyResult {
  resource: string; ns: string; verb: string; before: string; after: string; err?: string; conflict?: boolean; manager?: string; ref: IdeRef;
}
export interface Blocked { path: string; line: number; msg: string }
export interface Plan { title: string; cmd: string; ctx: string; items: ApplyResult[]; notes: string[]; blocked: Blocked[]; ns: string[] }
export interface IdeSource { path: string; line: number }
