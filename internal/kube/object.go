package kube

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// KV is one labeled value with an optional tone.
type KV struct {
	K string `json:"k"`
	V string `json:"v"`
	T string `json:"t,omitempty"`
}

// PortModel is one container port.
type PortModel struct {
	Port  int32  `json:"port"`
	Proto string `json:"proto"`
	Name  string `json:"name,omitempty"`
}

// ContainerModel describes one container of a pod.
type ContainerModel struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	Image     string      `json:"image"`
	State     string      `json:"state"`
	StateTone string      `json:"stateTone"`
	Running   bool        `json:"running"`
	Last      string      `json:"last,omitempty"`
	Restarts  int32       `json:"restarts"`
	Resources string      `json:"resources"`
	Ports     []PortModel `json:"ports"`
	Probes    string      `json:"probes"`
	Mounts    string      `json:"mounts"`
}

// PodModel holds the pod fields that the overview tab shows.
type PodModel struct {
	Node       string           `json:"node"`
	IP         string           `json:"ip"`
	QoS        string           `json:"qos"`
	SA         string           `json:"sa"`
	Phase      string           `json:"phase"`
	Restarts   int64            `json:"restarts"`
	Conds      []KV             `json:"conds"`
	Containers []ContainerModel `json:"containers"`
}

// EventItem is one event of an object.
type EventItem struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Msg    string `json:"msg"`
	Count  int64  `json:"count"`
	Last   int64  `json:"last"`
}

// Related is one relationship to another object.
type Related struct {
	Rel  string `json:"rel"`
	Kind string `json:"kind"`
	NS   string `json:"ns"`
	Name string `json:"name"`
}

// ObjectDoc is everything the detail panel shows for one object.
type ObjectDoc struct {
	Kind          string      `json:"kind"`
	NS            string      `json:"ns"`
	Name          string      `json:"name"`
	UID           string      `json:"uid"`
	API           string      `json:"api"`
	YAML          string      `json:"yaml"`
	Status        string      `json:"status"`
	Tone          string      `json:"tone"`
	Created       int64       `json:"created"`
	Labels        []string    `json:"labels"`
	Annotations   []KV        `json:"annotations"`
	Summary       []KV        `json:"summary"`
	Pod           *PodModel   `json:"pod,omitempty"`
	Events        []EventItem `json:"events"`
	Related       []Related   `json:"related"`
	Owner         *Ref        `json:"owner,omitempty"`
	Managed       *Ref        `json:"managed,omitempty"`
	Replicas      *int64      `json:"replicas,omitempty"`
	Unschedulable bool        `json:"unschedulable"`
	Suspended     bool        `json:"suspended"`
	Helm          *Ref        `json:"helm,omitempty"`
}

// mapper maps kinds to resources. It lives on the cluster, so it goes away
// with the cluster after a context switch.
func (c *Cluster) mapper() meta.RESTMapper {
	c.mapperOnce.Do(func() {
		c.restMapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(c.cs.Discovery()))
	})
	return c.restMapper
}

func (c *Cluster) ri(k *Kind, ns string) dynamic.ResourceInterface {
	r := c.dyn.Resource(k.gvr())
	if k.Namespaced {
		return r.Namespace(ns)
	}
	return r
}

func toYAML(obj map[string]any) string {
	b, err := yaml.Marshal(obj)
	if err != nil {
		return "# " + err.Error()
	}
	return string(b)
}

func cleanForYAML(u *unstructured.Unstructured) {
	u.SetManagedFields(nil)
	if a := u.GetAnnotations(); a != nil {
		if _, ok := a[lastApplied]; ok {
			delete(a, lastApplied)
			u.SetAnnotations(a)
		}
	}
}

// GetObject loads one object and everything around it.
func (c *Cluster) GetObject(ref Ref) (*ObjectDoc, error) {
	k := c.kind(ref.Kind)
	if k == nil || k.Local {
		return nil, fmt.Errorf("%s is not a cluster resource", ref.Kind)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	u, err := c.ri(k, ref.NS).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, wrapf(err, "cannot read %s %s", singular(ref.Kind), ref.Name)
	}
	cleanForYAML(u)
	doc := &ObjectDoc{Kind: k.Name, NS: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()), API: k.apiVersion(),
		YAML: toYAML(u.Object), Created: u.GetCreationTimestamp().Unix(), Labels: []string{}, Annotations: []KV{},
		Summary: []KV{}, Events: []EventItem{}, Related: []Related{}}
	if l := labelString(u.GetLabels()); l != "" {
		doc.Labels = strings.Split(l, " ")
	}
	annKeys := make([]string, 0, len(u.GetAnnotations()))
	for key := range u.GetAnnotations() {
		annKeys = append(annKeys, key)
	}
	sort.Strings(annKeys)
	for _, key := range annKeys {
		doc.Annotations = append(doc.Annotations, KV{K: key, V: u.GetAnnotations()[key]})
	}
	if t := c.table(k.Name); t != nil {
		if row := t.get(doc.UID); row != nil {
			for i, col := range k.Cols {
				if col.T == CName || col.T == CNs || col.T == CAge || i >= len(row.C) {
					continue
				}
				v := row.C[i]
				if col.T == CTime && v != "" {
					v = "@" + v
				}
				doc.Summary = append(doc.Summary, KV{K: col.L, V: v, T: string(row.K[i])})
			}
			// The header shows a status column, or the ready count when the
			// kind has no status column.
			for _, want := range []string{CStatus, CReady} {
				for i, col := range k.Cols {
					if doc.Status == "" && col.T == want && i < len(row.C) {
						doc.Status = row.C[i]
						doc.Tone = string(row.K[i])
					}
				}
			}
		}
	}
	if a := u.GetAnnotations(); a["meta.helm.sh/release-name"] != "" {
		doc.Helm = &Ref{Kind: "HelmRelease", NS: a["meta.helm.sh/release-namespace"], Name: a["meta.helm.sh/release-name"]}
	}
	if ok, on := c.directOwner(u.GetOwnerReferences()); on != "" {
		doc.Owner = &Ref{Kind: ok, NS: u.GetNamespace(), Name: on}
		if tk, tn := c.owner(u.GetNamespace(), u.GetOwnerReferences()); tn != "" {
			doc.Managed = &Ref{Kind: tk, NS: u.GetNamespace(), Name: tn}
		}
	}
	switch k.Name {
	case "Pods":
		var p corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p); err == nil {
			doc.Pod = podModel(&p)
			st := podStatusOf(&p)
			doc.Status = st.reason
			doc.Tone = string(statusTone(st.reason))
			if st.reason == "Running" {
				doc.Tone = string(tOK)
			}
		}
	case "Deployments", "StatefulSets", "ReplicaSets":
		if r, ok, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); ok {
			doc.Replicas = &r
		} else {
			one := int64(1)
			doc.Replicas = &one
		}
	case "Nodes":
		doc.Unschedulable, _, _ = unstructured.NestedBool(u.Object, "spec", "unschedulable")
	case "CronJobs":
		doc.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	}
	doc.Events = c.objectEvents(doc.UID)
	doc.Related = c.related(k, u)
	return doc, nil
}

func (c *Cluster) objectEvents(uid string) []EventItem {
	objs := c.byIndex("Events", "involved", uid)
	out := make([]EventItem, 0, len(objs))
	for _, o := range objs {
		e := o.(*corev1.Event)
		out = append(out, EventItem{Type: e.Type, Reason: e.Reason, Msg: strings.TrimSpace(e.Message), Count: eventCount(e), Last: eventTime(e).Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out
}

func stateString(s corev1.ContainerState, age string) (string, string, bool) {
	switch {
	case s.Running != nil:
		return "Running · started " + ago(s.Running.StartedAt.Time) + " ago", string(tOK), true
	case s.Terminated != nil:
		t := s.Terminated
		v := fmt.Sprintf("Terminated · %s · exit %d", orDash(t.Reason), t.ExitCode)
		return v, string(statusTone(t.Reason)), false
	case s.Waiting != nil:
		return "Waiting · " + orDash(s.Waiting.Reason), string(statusTone(s.Waiting.Reason)), false
	}
	return "Waiting · " + age, string(tWarn), false
}

func resString(r corev1.ResourceRequirements) string {
	q := func(l corev1.ResourceList, n corev1.ResourceName) string {
		if v, ok := l[n]; ok {
			return v.String()
		}
		return "—"
	}
	return "cpu " + q(r.Requests, corev1.ResourceCPU) + " / " + q(r.Limits, corev1.ResourceCPU) +
		" · memory " + q(r.Requests, corev1.ResourceMemory) + " / " + q(r.Limits, corev1.ResourceMemory)
}

func probeString(name string, p *corev1.Probe) string {
	if p == nil {
		return ""
	}
	switch {
	case p.HTTPGet != nil:
		return name + " GET " + p.HTTPGet.Path + " :" + p.HTTPGet.Port.String()
	case p.TCPSocket != nil:
		return name + " TCP :" + p.TCPSocket.Port.String()
	case p.GRPC != nil:
		return name + " gRPC :" + itoa(p.GRPC.Port)
	case p.Exec != nil:
		return name + " exec " + strings.Join(p.Exec.Command, " ")
	}
	return name
}

func containerModel(ct corev1.Container, st *corev1.ContainerStatus, typ string) ContainerModel {
	m := ContainerModel{Name: ct.Name, Type: typ, Image: ct.Image, Resources: resString(ct.Resources), Ports: []PortModel{}}
	for _, p := range ct.Ports {
		m.Ports = append(m.Ports, PortModel{Port: p.ContainerPort, Proto: string(p.Protocol), Name: p.Name})
	}
	var probes []string
	for _, p := range []string{probeString("liveness", ct.LivenessProbe), probeString("readiness", ct.ReadinessProbe), probeString("startup", ct.StartupProbe)} {
		if p != "" {
			probes = append(probes, p)
		}
	}
	m.Probes = orDash(strings.Join(probes, " · "))
	var mounts []string
	for _, v := range ct.VolumeMounts {
		mounts = append(mounts, v.Name+" → "+v.MountPath)
	}
	m.Mounts = orDash(strings.Join(mounts, " · "))
	if st != nil {
		m.State, m.StateTone, m.Running = stateString(st.State, "")
		m.Restarts = st.RestartCount
		if st.Image != "" {
			m.Image = st.Image
		}
		if t := st.LastTerminationState.Terminated; t != nil {
			m.Last = fmt.Sprintf("Terminated · %s · exit %d · %s ago", orDash(t.Reason), t.ExitCode, ago(t.FinishedAt.Time))
		}
	} else {
		m.State, m.StateTone = "Waiting · pod not started", string(tWarn)
	}
	return m
}

func podModel(p *corev1.Pod) *PodModel {
	m := &PodModel{Node: orDash(p.Spec.NodeName), IP: orDash(p.Status.PodIP), QoS: orDash(string(p.Status.QOSClass)),
		SA: orDash(p.Spec.ServiceAccountName), Phase: string(p.Status.Phase), Conds: []KV{}, Containers: []ContainerModel{}}
	for _, t := range []corev1.PodConditionType{corev1.PodScheduled, corev1.PodInitialized, corev1.ContainersReady, corev1.PodReady} {
		v := "False"
		for _, c := range p.Status.Conditions {
			if c.Type == t {
				v = string(c.Status)
			}
		}
		m.Conds = append(m.Conds, KV{K: string(t), V: v, T: string(condTone(v))})
	}
	status := func(list []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
		for i := range list {
			if list[i].Name == name {
				return &list[i]
			}
		}
		return nil
	}
	for _, ct := range p.Spec.InitContainers {
		typ := "init"
		if ct.RestartPolicy != nil && *ct.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			typ = "sidecar"
		}
		m.Containers = append(m.Containers, containerModel(ct, status(p.Status.InitContainerStatuses, ct.Name), typ))
	}
	for _, ct := range p.Spec.Containers {
		cm := containerModel(ct, status(p.Status.ContainerStatuses, ct.Name), "")
		m.Restarts += int64(cm.Restarts)
		m.Containers = append(m.Containers, cm)
	}
	for _, ec := range p.Spec.EphemeralContainers {
		ct := corev1.Container(ec.EphemeralContainerCommon)
		m.Containers = append(m.Containers, containerModel(ct, status(p.Status.EphemeralContainerStatuses, ec.Name), "ephemeral"))
	}
	return m
}

// related finds owners, children and references of an object.
func (c *Cluster) related(k *Kind, u *unstructured.Unstructured) []Related {
	ns, name, uid := u.GetNamespace(), u.GetName(), string(u.GetUID())
	var out []Related
	add := func(rel, kind, ns, name string) {
		if name == "" || kind == "" {
			return
		}
		for _, r := range out {
			if r.Rel == rel && r.Kind == kind && r.NS == ns && r.Name == name {
				return
			}
		}
		out = append(out, Related{Rel: rel, Kind: kind, NS: ns, Name: name})
	}
	for _, ref := range u.GetOwnerReferences() {
		if kk := c.kindByKind(ref.Kind, ref.APIVersion); kk != nil {
			rel := "Owned by"
			if ptr.Deref(ref.Controller, false) {
				rel = "Controlled by"
			}
			add(rel, kk.Name, ns, ref.Name)
		}
	}
	children := func(kinds ...string) {
		for _, ck := range kinds {
			for _, o := range c.byIndex(ck, "owner", uid) {
				if m := objMeta(o); m != nil {
					add("Owns", ck, m.GetNamespace(), m.GetName())
				}
			}
		}
	}
	podsBySelector := func(sel labels.Selector, rel string) {
		if sel == nil || sel.Empty() {
			return
		}
		n := 0
		for _, o := range c.byIndex("Pods", "namespace", ns) {
			p := o.(*corev1.Pod)
			if sel.Matches(labels.Set(p.Labels)) {
				add(rel, "Pods", p.Namespace, p.Name)
				if n++; n >= 50 {
					return
				}
			}
		}
	}
	servicesSelecting := func(podLabels map[string]string) {
		for _, o := range c.byIndex("Services", "namespace", ns) {
			s := o.(*corev1.Service)
			if len(s.Spec.Selector) > 0 && labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(podLabels)) {
				add("Exposed by", "Services", s.Namespace, s.Name)
			}
		}
	}
	scaledBy := func(kind string) {
		for _, o := range c.byIndex("HorizontalPodAutoscalers", "namespace", ns) {
			h := o.(*autoscalingv2.HorizontalPodAutoscaler)
			if h.Spec.ScaleTargetRef.Kind == kind && h.Spec.ScaleTargetRef.Name == name {
				add("Scaled by", "HorizontalPodAutoscalers", h.Namespace, h.Name)
			}
		}
	}

	switch k.Name {
	case "Pods":
		var p corev1.Pod
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p) != nil {
			break
		}
		if tk, tn := c.owner(ns, p.OwnerReferences); tn != "" {
			add("Managed by", tk, ns, tn)
		}
		for _, o := range c.byIndex("Services", "namespace", ns) {
			s := o.(*corev1.Service)
			if len(s.Spec.Selector) > 0 && labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(p.Labels)) {
				add("Selected by", "Services", s.Namespace, s.Name)
			}
		}
		for _, v := range p.Spec.Volumes {
			switch {
			case v.ConfigMap != nil:
				add("Mounts", "ConfigMaps", ns, v.ConfigMap.Name)
			case v.Secret != nil:
				add("Mounts", "Secrets", ns, v.Secret.SecretName)
			case v.PersistentVolumeClaim != nil:
				add("Mounts", "PersistentVolumeClaims", ns, v.PersistentVolumeClaim.ClaimName)
			case v.Projected != nil:
				for _, s := range v.Projected.Sources {
					if s.ConfigMap != nil && s.ConfigMap.Name != "kube-root-ca.crt" {
						add("Mounts", "ConfigMaps", ns, s.ConfigMap.Name)
					}
					if s.Secret != nil {
						add("Mounts", "Secrets", ns, s.Secret.Name)
					}
				}
			}
		}
		for _, ct := range p.Spec.Containers {
			for _, ef := range ct.EnvFrom {
				if ef.ConfigMapRef != nil {
					add("Reads env from", "ConfigMaps", ns, ef.ConfigMapRef.Name)
				}
				if ef.SecretRef != nil {
					add("Reads env from", "Secrets", ns, ef.SecretRef.Name)
				}
			}
		}
		add("Runs as", "ServiceAccounts", ns, p.Spec.ServiceAccountName)
		add("Scheduled on", "Nodes", "", p.Spec.NodeName)
	case "Deployments", "StatefulSets", "DaemonSets", "ReplicaSets":
		children("ReplicaSets", "Pods")
		if tl, ok, _ := unstructured.NestedStringMap(u.Object, "spec", "template", "metadata", "labels"); ok {
			servicesSelecting(tl)
			for _, o := range c.byIndex("PodDisruptionBudgets", "namespace", ns) {
				if m := objMeta(o); m != nil {
					add("Protected by", "PodDisruptionBudgets", ns, m.GetName())
				}
			}
		}
		scaledBy(singular(k.Name))
	case "Jobs":
		children("Pods")
	case "CronJobs":
		children("Jobs")
	case "Services":
		if sel, ok, _ := unstructured.NestedStringMap(u.Object, "spec", "selector"); ok && len(sel) > 0 {
			podsBySelector(labels.SelectorFromSet(sel), "Selects")
		}
		add("Endpoints", "Endpoints", ns, name)
		for _, o := range c.byIndex("EndpointSlices", "namespace", ns) {
			if m := objMeta(o); m != nil && m.GetLabels()["kubernetes.io/service-name"] == name {
				add("Endpoints", "EndpointSlices", ns, m.GetName())
			}
		}
		for _, o := range c.byIndex("Ingresses", "namespace", ns) {
			in := o.(*networkingv1.Ingress)
			if ingressUses(in, name) {
				add("Routed by", "Ingresses", ns, in.Name)
			}
		}
	case "Ingresses":
		var in networkingv1.Ingress
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &in) == nil {
			for _, s := range ingressServices(&in) {
				add("Routes to", "Services", ns, s)
			}
			for _, t := range in.Spec.TLS {
				add("TLS from", "Secrets", ns, t.SecretName)
			}
			if in.Spec.IngressClassName != nil {
				add("Class", "IngressClasses", "", *in.Spec.IngressClassName)
			}
		}
	case "PersistentVolumeClaims":
		vol, _, _ := unstructured.NestedString(u.Object, "spec", "volumeName")
		add("Bound to", "PersistentVolumes", "", vol)
		sc, _, _ := unstructured.NestedString(u.Object, "spec", "storageClassName")
		add("Class", "StorageClasses", "", sc)
		for _, o := range c.byIndex("Pods", "namespace", ns) {
			p := o.(*corev1.Pod)
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == name {
					add("Used by", "Pods", ns, p.Name)
				}
			}
		}
	case "PersistentVolumes":
		cns, _, _ := unstructured.NestedString(u.Object, "spec", "claimRef", "namespace")
		cn, _, _ := unstructured.NestedString(u.Object, "spec", "claimRef", "name")
		add("Claimed by", "PersistentVolumeClaims", cns, cn)
		sc, _, _ := unstructured.NestedString(u.Object, "spec", "storageClassName")
		add("Class", "StorageClasses", "", sc)
	case "Nodes":
		n := 0
		for _, o := range c.byIndex("Pods", "node", name) {
			p := o.(*corev1.Pod)
			if podTerminal(p) {
				continue
			}
			add("Runs", "Pods", p.Namespace, p.Name)
			if n++; n >= 100 {
				break
			}
		}
	case "ConfigMaps", "Secrets":
		for _, o := range c.byIndex("Pods", "namespace", ns) {
			p := o.(*corev1.Pod)
			if podUses(p, k.Name, name) {
				add("Used by", "Pods", ns, p.Name)
			}
		}
	case "ServiceAccounts":
		for _, o := range c.byIndex("Pods", "namespace", ns) {
			p := o.(*corev1.Pod)
			if p.Spec.ServiceAccountName == name {
				add("Used by", "Pods", ns, p.Name)
			}
		}
		for _, o := range c.list("RoleBindings") {
			rb := o.(*rbacv1.RoleBinding)
			if hasSA(rb.Subjects, ns, name) {
				add("Bound by", "RoleBindings", rb.Namespace, rb.Name)
			}
		}
		for _, o := range c.list("ClusterRoleBindings") {
			rb := o.(*rbacv1.ClusterRoleBinding)
			if hasSA(rb.Subjects, ns, name) {
				add("Bound by", "ClusterRoleBindings", "", rb.Name)
			}
		}
	case "RoleBindings":
		rk, _, _ := unstructured.NestedString(u.Object, "roleRef", "kind")
		rn, _, _ := unstructured.NestedString(u.Object, "roleRef", "name")
		if rk == "Role" {
			add("Grants", "Roles", ns, rn)
		} else {
			add("Grants", "ClusterRoles", "", rn)
		}
	case "ClusterRoleBindings":
		rn, _, _ := unstructured.NestedString(u.Object, "roleRef", "name")
		add("Grants", "ClusterRoles", "", rn)
	case "HorizontalPodAutoscalers":
		tk, _, _ := unstructured.NestedString(u.Object, "spec", "scaleTargetRef", "kind")
		tn, _, _ := unstructured.NestedString(u.Object, "spec", "scaleTargetRef", "name")
		if kk := c.kindByKind(tk, ""); kk != nil {
			add("Scales", kk.Name, ns, tn)
		}
	}
	if ns != "" && k.Name != "Namespaces" {
		add("In namespace", "Namespaces", "", ns)
	}
	if out == nil {
		return []Related{}
	}
	return out
}

func ingressServices(in *networkingv1.Ingress) []string {
	var out []string
	if in.Spec.DefaultBackend != nil && in.Spec.DefaultBackend.Service != nil {
		out = append(out, in.Spec.DefaultBackend.Service.Name)
	}
	for _, r := range in.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			if p.Backend.Service != nil {
				out = append(out, p.Backend.Service.Name)
			}
		}
	}
	return out
}

func ingressUses(in *networkingv1.Ingress, svc string) bool {
	for _, s := range ingressServices(in) {
		if s == svc {
			return true
		}
	}
	return false
}

func podUses(p *corev1.Pod, kind, name string) bool {
	for _, v := range p.Spec.Volumes {
		if kind == "ConfigMaps" && v.ConfigMap != nil && v.ConfigMap.Name == name {
			return true
		}
		if kind == "Secrets" && v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
		if v.Projected != nil {
			for _, s := range v.Projected.Sources {
				if kind == "ConfigMaps" && s.ConfigMap != nil && s.ConfigMap.Name == name {
					return true
				}
				if kind == "Secrets" && s.Secret != nil && s.Secret.Name == name {
					return true
				}
			}
		}
	}
	for _, ct := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
		for _, ef := range ct.EnvFrom {
			if kind == "ConfigMaps" && ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == name {
				return true
			}
			if kind == "Secrets" && ef.SecretRef != nil && ef.SecretRef.Name == name {
				return true
			}
		}
		for _, e := range ct.Env {
			if e.ValueFrom == nil {
				continue
			}
			if kind == "ConfigMaps" && e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name == name {
				return true
			}
			if kind == "Secrets" && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
				return true
			}
		}
	}
	for _, s := range p.Spec.ImagePullSecrets {
		if kind == "Secrets" && s.Name == name {
			return true
		}
	}
	return false
}

func hasSA(subs []rbacv1.Subject, ns, name string) bool {
	for _, s := range subs {
		if s.Kind == rbacv1.ServiceAccountKind && s.Namespace == ns && s.Name == name {
			return true
		}
	}
	return false
}

// ---- Writes ----

func dryOpts(dry bool) []string {
	if dry {
		return []string{metav1.DryRunAll}
	}
	return nil
}

func resourceString(k *Kind) string {
	s := strings.ToLower(k.Kind)
	if k.Group != "" {
		s += "." + k.Group
	}
	return s
}

func suffix(dry bool) string {
	if dry {
		return " (server dry run)"
	}
	return ""
}

func decodeDocs(text string) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewBufferString(text), 4096)
	var out []*unstructured.Unstructured
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("the YAML is not valid: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: obj})
	}
	if len(out) == 0 {
		return nil, errors.New("the YAML has no objects")
	}
	return out, nil
}

func (c *Cluster) resolve(u *unstructured.Unstructured, defNS string) (dynamic.ResourceInterface, string, error) {
	gvk := u.GroupVersionKind()
	if gvk.Kind == "" || gvk.Version == "" {
		return nil, "", errors.New("the object needs apiVersion and kind")
	}
	m, err := c.mapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil && meta.IsNoMatchError(err) {
		// A CRD can be newer than the cached discovery data.
		if rm, ok := c.mapper().(meta.ResettableRESTMapper); ok {
			rm.Reset()
			m, err = c.mapper().RESTMapping(gvk.GroupKind(), gvk.Version)
		}
	}
	if err != nil {
		return nil, "", fmt.Errorf("the cluster does not serve %s: %w", gvk.String(), err)
	}
	res := strings.ToLower(gvk.Kind)
	if gvk.Group != "" {
		res += "." + gvk.Group
	}
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		if u.GetNamespace() == "" {
			u.SetNamespace(defNS)
		}
		return c.dyn.Resource(m.Resource).Namespace(u.GetNamespace()), res, nil
	}
	return c.dyn.Resource(m.Resource), res, nil
}

// Apply replaces an object with the edited YAML. The resourceVersion in the
// YAML makes the update fail if the object changed in the meantime.
func (c *Cluster) Apply(text string, dry bool) (string, error) {
	docs, err := decodeDocs(text)
	if err != nil {
		return "", err
	}
	if len(docs) != 1 {
		return "", errors.New("edit one object at a time")
	}
	u := docs[0]
	ri, res, err := c.resolve(u, "default")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	// The YAML view hides the last-applied annotation. Put it back, so a
	// later kubectl apply can still compute its three-way merge.
	if live, err := ri.Get(ctx, u.GetName(), metav1.GetOptions{}); err == nil {
		if v, ok := live.GetAnnotations()[lastApplied]; ok {
			a := u.GetAnnotations()
			if _, has := a[lastApplied]; !has {
				if a == nil {
					a = map[string]string{}
				}
				a[lastApplied] = v
				u.SetAnnotations(a)
			}
		}
	}
	before := u.GetResourceVersion()
	out, err := ri.Update(ctx, u, metav1.UpdateOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)})
	if err != nil {
		return "", wrapf(err, "%s/%s was not applied", res, u.GetName())
	}
	verb := "configured"
	if !dry && out.GetResourceVersion() == before {
		verb = "unchanged"
	}
	return res + "/" + u.GetName() + " " + verb + suffix(dry), nil
}

// Create creates every object in the YAML.
func (c *Cluster) Create(text, defNS string, dry bool) (string, error) {
	docs, err := decodeDocs(text)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	var msgs []string
	for _, u := range docs {
		ri, res, err := c.resolve(u, defNS)
		if err != nil {
			return strings.Join(msgs, "\n"), err
		}
		if _, err := ri.Create(ctx, u, metav1.CreateOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)}); err != nil {
			return strings.Join(msgs, "\n"), wrapf(err, "%s/%s was not created", res, u.GetName())
		}
		msgs = append(msgs, res+"/"+u.GetName()+" created"+suffix(dry))
	}
	return strings.Join(msgs, "\n"), nil
}

func (c *Cluster) each(items []Ref, fn func(ctx context.Context, k *Kind, r Ref) error) (int, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
	defer cancel()
	var errs []string
	n := 0
	for _, r := range items {
		k := c.kind(r.Kind)
		if k == nil || k.Local {
			errs = append(errs, r.Kind+" is not a cluster resource")
			continue
		}
		if err := fn(ctx, k, r); err != nil {
			errs = append(errs, r.Name+": "+errString(err))
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, errors.New(strings.Join(errs, "\n"))
	}
	return n, nil
}

// Delete deletes objects in the background.
func (c *Cluster) Delete(items []Ref, dry bool) (string, error) {
	pol := metav1.DeletePropagationBackground
	n, err := c.each(items, func(ctx context.Context, k *Kind, r Ref) error {
		return c.ri(k, r.NS).Delete(ctx, r.Name, metav1.DeleteOptions{PropagationPolicy: &pol, DryRun: dryOpts(dry)})
	})
	msg := fmt.Sprintf("%d deleted%s (propagationPolicy=Background)", n, suffix(dry))
	if dry && err == nil {
		msg = fmt.Sprintf("Server dry-run: %d objects would be deleted (propagationPolicy=Background). No admission webhook rejected the request.", n)
	}
	return msg, err
}

// Restart triggers a rolling restart like kubectl rollout restart.
func (c *Cluster) Restart(items []Ref, dry bool) (string, error) {
	now := time.Now().Format(time.RFC3339)
	patch := []byte(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"` + now + `"}}}}}`)
	n, err := c.each(items, func(ctx context.Context, k *Kind, r Ref) error {
		_, err := c.ri(k, r.NS).Patch(ctx, r.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)})
		return err
	})
	return fmt.Sprintf("%d restarted%s (kubectl.kubernetes.io/restartedAt patched)", n, suffix(dry)), err
}

// Scale sets the replica count.
func (c *Cluster) Scale(r Ref, replicas int64, dry bool) (string, error) {
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err := c.each([]Ref{r}, func(ctx context.Context, k *Kind, r Ref) error {
		_, err := c.ri(k, r.NS).Patch(ctx, r.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)}, "scale")
		return err
	})
	k := c.kind(r.Kind)
	if k == nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s scaled to %d%s", resourceString(k), r.Name, replicas, suffix(dry)), err
}

// Cordon marks a node as schedulable or unschedulable.
func (c *Cluster) Cordon(node string, on, dry bool) (string, error) {
	patch := []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, on))
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	_, err := c.cs.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)})
	if err != nil {
		return "", wrapf(err, "node/%s was not changed", node)
	}
	verb := "uncordoned"
	if on {
		verb = "cordoned"
	}
	return "node/" + node + " " + verb + suffix(dry), nil
}

// TriggerCronJob creates a Job from a CronJob, like kubectl create job --from.
func (c *Cluster) TriggerCronJob(ns, name string, dry bool) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	cj, err := c.cs.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", wrapf(err, "cannot read cronjob %s", name)
	}
	base := cj.Name
	if len(base) > 45 {
		base = base[:45]
	}
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	ann := map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	for k, v := range cj.Spec.JobTemplate.Annotations {
		ann[k] = v
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: base + "-manual-" + hex.EncodeToString(buf), Namespace: ns,
			Labels: cj.Spec.JobTemplate.Labels, Annotations: ann,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cj, batchv1.SchemeGroupVersion.WithKind("CronJob"))}},
		Spec: cj.Spec.JobTemplate.Spec,
	}
	out, err := c.cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)})
	if err != nil {
		return "", wrapf(err, "job was not created")
	}
	return "job.batch/" + out.Name + " created" + suffix(dry), nil
}

// SuspendCronJob suspends or resumes a CronJob.
func (c *Cluster) SuspendCronJob(ns, name string, suspend, dry bool) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	patch := []byte(fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend))
	if _, err := c.cs.BatchV1().CronJobs(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "st8ks", DryRun: dryOpts(dry)}); err != nil {
		return "", wrapf(err, "cronjob/%s was not changed", name)
	}
	if suspend {
		return "cronjob.batch/" + name + " suspended" + suffix(dry), nil
	}
	return "cronjob.batch/" + name + " resumed" + suffix(dry), nil
}

// FixProposal is a suggested edit that the user reviews as a diff.
type FixProposal struct {
	Kind  string `json:"kind"`
	NS    string `json:"ns"`
	Name  string `json:"name"`
	Live  string `json:"live"`
	Draft string `json:"draft"`
}

// ProposeMemoryFix raises the memory limit of one container in a workload.
func (c *Cluster) ProposeMemoryFix(a IssueAction) (*FixProposal, error) {
	k := c.kind(a.Kind)
	if k == nil {
		return nil, fmt.Errorf("unknown kind %s", a.Kind)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	u, err := c.ri(k, a.NS).Get(ctx, a.Name, metav1.GetOptions{})
	if err != nil {
		return nil, wrapf(err, "cannot read %s %s", singular(a.Kind), a.Name)
	}
	cleanForYAML(u)
	live := toYAML(u.Object)
	cts, ok, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	if !ok {
		return nil, errors.New("the workload has no pod template")
	}
	found := false
	for i, ci := range cts {
		ct, _ := ci.(map[string]any)
		if ct["name"] != a.Container {
			continue
		}
		found = true
		limStr, _, _ := unstructured.NestedString(ct, "resources", "limits", "memory")
		reqStr, _, _ := unstructured.NestedString(ct, "resources", "requests", "memory")
		var cur int64
		if q, err := resource.ParseQuantity(limStr); err == nil {
			cur = q.Value()
		}
		next := proposedMemory(cur)
		_ = unstructured.SetNestedField(ct, fmtBytes(next), "resources", "limits", "memory")
		if q, err := resource.ParseQuantity(reqStr); err == nil && q.Value() < next/2 {
			_ = unstructured.SetNestedField(ct, fmtBytes(next/2), "resources", "requests", "memory")
		}
		cts[i] = ct
	}
	if !found {
		return nil, fmt.Errorf("container %s not found in %s %s", a.Container, singular(a.Kind), a.Name)
	}
	_ = unstructured.SetNestedSlice(u.Object, cts, "spec", "template", "spec", "containers")
	return &FixProposal{Kind: a.Kind, NS: a.NS, Name: a.Name, Live: live, Draft: toYAML(u.Object)}, nil
}

// NewYAML returns a template for a new object of a kind.
func (c *Cluster) NewYAML(kind, ns string) string {
	k := c.kind(kind)
	if k == nil {
		return ""
	}
	nsLine := ""
	if k.Namespaced {
		nsLine = "\n  namespace: " + ns
	}
	switch kind {
	case "Pods":
		return "apiVersion: v1\nkind: Pod\nmetadata:\n  name: my-pod" + nsLine + "\nspec:\n  containers:\n  - name: app\n    image: nginx:1.27\n    ports:\n    - containerPort: 80\n"
	case "Deployments":
		return "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: my-app" + nsLine + "\nspec:\n  replicas: 2\n  selector:\n    matchLabels:\n      app: my-app\n  template:\n    metadata:\n      labels:\n        app: my-app\n    spec:\n      containers:\n      - name: app\n        image: nginx:1.27\n        ports:\n        - containerPort: 80\n"
	case "Services":
		return "apiVersion: v1\nkind: Service\nmetadata:\n  name: my-service" + nsLine + "\nspec:\n  selector:\n    app: my-app\n  ports:\n  - port: 80\n    targetPort: 80\n"
	case "ConfigMaps":
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: my-config" + nsLine + "\ndata:\n  key: value\n"
	case "Secrets":
		return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: my-secret" + nsLine + "\ntype: Opaque\nstringData:\n  key: value\n"
	case "Namespaces":
		return "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: my-namespace\n"
	case "Jobs":
		return "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: my-job" + nsLine + "\nspec:\n  template:\n    spec:\n      restartPolicy: Never\n      containers:\n      - name: job\n        image: busybox:1.36\n        command: [\"sh\", \"-c\", \"echo hello\"]\n"
	case "CronJobs":
		return "apiVersion: batch/v1\nkind: CronJob\nmetadata:\n  name: my-cronjob" + nsLine + "\nspec:\n  schedule: \"*/15 * * * *\"\n  jobTemplate:\n    spec:\n      template:\n        spec:\n          restartPolicy: OnFailure\n          containers:\n          - name: job\n            image: busybox:1.36\n            command: [\"sh\", \"-c\", \"date\"]\n"
	}
	return "apiVersion: " + k.apiVersion() + "\nkind: " + k.Kind + "\nmetadata:\n  name: my-" + strings.ToLower(k.Kind) + nsLine + "\nspec: {}\n"
}
