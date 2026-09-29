package ide

// Cluster is the access to the target cluster that the IDE needs. The kube
// package implements it for the connected context.
type Cluster interface {
	Name() string
	DefaultNamespace() string
	// Minor is the minor Kubernetes version, or 0 when it is not known.
	Minor() int
	// Version changes when the live objects that the checks read change.
	Version() uint64
	// Resource finds the REST resource of a kind. ok is false when the
	// cluster does not serve the kind.
	Resource(apiVersion, kind string) (r Resource, ok bool)
	// OpenAPI returns the OpenAPI v3 document of a group version, such as
	// apps/v1 or v1.
	OpenAPI(groupVersion string) ([]byte, error)
	// Live returns the live object without status and managed fields, or
	// nil when it does not exist.
	Live(ref Ref) (map[string]any, error)
	Workload(ref Ref) *Workload
	PullFailures() []PullFailure
	Status(ref Ref) *LiveStatus
	// Apply runs a server-side apply for each object.
	Apply(objs []map[string]any, dry, force bool) []ApplyResult
	// Rollout follows a workload rollout and reports progress lines.
	Rollout(ref Ref, log func(text, tone string))
}

// Resource is a served kind.
type Resource struct {
	Name       string // for example deployment.apps
	Namespaced bool
}

// Ref names one object.
type Ref struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	NS         string `json:"ns"`
	Name       string `json:"name"`
}

// Workload holds live facts about the pods of a workload.
type Workload struct {
	Found      bool
	Containers map[string]*ContainerFacts
}

// ContainerFacts are the live facts of one container across all pods.
type ContainerFacts struct {
	Image     string // the image of the current pod template
	Ready     int    // pods where the container is ready
	Pods      int
	OOMKills  int32  // restarts that ended with OOMKilled
	MemLimit  string // the live memory limit
	CrashExit int32  // the last exit code of a crash loop, or -1
	Crash     bool
	PullImage string // an image that the pods cannot pull
	PullMsg   string
	PrevImage string // the image of the previous revision
	PrevReady bool   // the previous revision had ready pods
}

// PullFailure is an image that pods cannot pull.
type PullFailure struct {
	NS        string
	Image     string
	Msg       string
	Pods      int
	Owner     Ref
	Container string
	PrevImage string
}

// KV is a labeled value with an optional tone.
type KV struct {
	K string `json:"k"`
	V string `json:"v"`
	T string `json:"t,omitempty"`
}

// PodRow is one pod of a workload.
type PodRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Tone   string `json:"tone"`
}

// LiveStatus is what the inspector shows about a live object.
type LiveStatus struct {
	Found   bool     `json:"found"`
	Kind    string   `json:"kind"` // the table name, such as Deployments
	Status  string   `json:"status"`
	Tone    string   `json:"tone"`
	Rows    []KV     `json:"rows"`
	Pods    []PodRow `json:"pods"`
	Event   string   `json:"event"`
	Manager string   `json:"manager"`
}

// ApplyResult is the outcome of an apply of one object.
type ApplyResult struct {
	Resource string `json:"resource"` // for example deployment.apps/checkout
	NS       string `json:"ns"`
	Verb     string `json:"verb"`   // created, configured, unchanged or failed
	Before   string `json:"before"` // the live object as YAML
	After    string `json:"after"`  // the result as YAML
	Err      string `json:"err,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
	Manager  string `json:"manager,omitempty"`
	Ref      Ref    `json:"ref"`
}
