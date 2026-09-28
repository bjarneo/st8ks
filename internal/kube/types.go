package kube

import (
	"slices"
)

// Emitter sends an event to the frontend.
type Emitter func(name string, data any)

// Column types. The frontend picks the cell renderer from the type.
const (
	CName   = "name"   // uses Row.M
	CNs     = "ns"     // uses Row.N
	CAge    = "age"    // uses Row.T
	CText   = "text"   // proportional font
	CMono   = "mono"   // monospace font
	CStatus = "status" // colored dot and text
	CReady  = "ready"  // "a/b" counts
	CNum    = "num"    // right-aligned number
	CCPU    = "cpu"    // pod CPU usage from metrics. X[0] is the limit in millicores
	CMem    = "mem"    // pod memory usage from metrics. X[1] is the limit in bytes
	CCPUPct = "cpupct" // node CPU usage in percent. X[0] is allocatable millicores
	CMemPct = "mempct" // node memory usage in percent. X[1] is allocatable bytes
	CTime   = "time"   // unix seconds in the cell, shown as a relative time
)

// Col describes one table column.
type Col struct {
	L string `json:"l"`
	T string `json:"t"`
	W string `json:"w"`
}

// Row is one object in a resource table. Cells for name, namespace and age
// columns are empty because the frontend reads M, N and T.
type Row struct {
	U string   `json:"u"`
	N string   `json:"n,omitempty"`
	M string   `json:"m"`
	T int64    `json:"t"`
	C []string `json:"c"`
	K string   `json:"k"`
	B bool     `json:"b,omitempty"`
	X []int64  `json:"x,omitempty"`
	L string   `json:"l,omitempty"`
}

func (r *Row) equal(o *Row) bool {
	return r.U == o.U && r.N == o.N && r.M == o.M && r.T == o.T && r.K == o.K &&
		r.B == o.B && r.L == o.L && slices.Equal(r.C, o.C) && slices.Equal(r.X, o.X)
}

// KindInfo describes a resource kind to the frontend.
type KindInfo struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Kind       string   `json:"kind"`
	API        string   `json:"api"`
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Resource   string   `json:"resource"`
	Namespaced bool     `json:"namespaced"`
	Cols       []Col    `json:"cols"`
	Actions    []string `json:"actions"`
	Custom     bool     `json:"custom"`
}

// TreeItem is one entry in the navigation tree. Kind is set for resource
// lists and View is set for special views.
type TreeItem struct {
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
	View  string `json:"view,omitempty"`
}

// TreeSection groups tree items.
type TreeSection struct {
	ID    string     `json:"id"`
	Label string     `json:"label"`
	Items []TreeItem `json:"items"`
}

// ContextInfo describes one kubeconfig context.
type ContextInfo struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Server    string `json:"server"`
	User      string `json:"user"`
	Namespace string `json:"namespace"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Dist      string `json:"dist"`
	Version   string `json:"version"`
	Region    string `json:"region"`
	Nodes     int    `json:"nodes"`
	Pods      int    `json:"pods"`
	Current   bool   `json:"current"`
}

// Context status values.
const (
	StatusUnknown     = "Unknown"
	StatusConnecting  = "Connecting"
	StatusConnected   = "Connected"
	StatusUnreachable = "Unreachable"
)

// ClusterState is the connection state of the current context.
type ClusterState struct {
	Context string `json:"context"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Server  string `json:"server"`
	Dist    string `json:"dist"`
	Version string `json:"version"`
	Region  string `json:"region"`
	Metrics bool   `json:"metrics"`
}

// Ref points to one object.
type Ref struct {
	Kind string `json:"kind"`
	NS   string `json:"ns"`
	Name string `json:"name"`
}

// Snapshot is the full content of one table.
type Snapshot struct {
	Kind      string              `json:"kind"`
	V         uint64              `json:"v"`
	Rows      []*Row              `json:"rows"`
	Synced    bool                `json:"synced"`
	Err       string              `json:"err,omitempty"`
	Metrics   map[string][2]int64 `json:"metrics,omitempty"`
	Available bool                `json:"available"`
}

// Delta holds the rows of one table that changed since the last flush.
type Delta struct {
	Kind   string   `json:"kind"`
	V      uint64   `json:"v"`
	Up     []*Row   `json:"up,omitempty"`
	Del    []string `json:"del,omitempty"`
	Synced bool     `json:"synced"`
	Err    string   `json:"err,omitempty"`
}
