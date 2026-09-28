package kube

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

// IssueAction is the fix that the assistant offers for an issue.
type IssueAction struct {
	Label     string `json:"label"`
	Type      string `json:"type"` // diff, uncordon, helm-rollback, logs
	Kind      string `json:"kind,omitempty"`
	NS        string `json:"ns,omitempty"`
	Name      string `json:"name,omitempty"`
	Container string `json:"container,omitempty"`
	Node      string `json:"node,omitempty"`
}

// Issue is a problem that needs attention.
type Issue struct {
	ID       string       `json:"id"`
	Sev      string       `json:"sev"`
	Title    string       `json:"title"`
	Kind     string       `json:"kind"`
	NS       string       `json:"ns"`
	Obj      string       `json:"obj"`
	Reason   string       `json:"reason"`
	Meta     string       `json:"meta"`
	Summary  string       `json:"summary"`
	Evidence []string     `json:"evidence"`
	Fix      string       `json:"fix"`
	Cmds     []string     `json:"cmds"`
	Action   *IssueAction `json:"action,omitempty"`
	Pods     []string     `json:"pods,omitempty"`
}

type derivedState struct {
	mu        sync.Mutex
	ovKey     [3]uint64
	issueKey  uint64
	overview  Overview
	issues    []Issue
	issueJSON []byte
}

var issueKinds = []string{"Pods", "Nodes", "Deployments", "StatefulSets", "DaemonSets", "Jobs", "PersistentVolumeClaims", "Events", "certificates.cert-manager.io"}

func (c *Cluster) derivedLoop() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tk.C:
			c.refreshDerived()
		}
	}
}

func (c *Cluster) tableVer(name string) uint64 {
	if t := c.table(name); t != nil {
		return t.ver()
	}
	return 0
}

func (c *Cluster) refreshDerived() {
	d := &c.derived
	ovKey := [3]uint64{c.tableVer("Pods"), c.tableVer("Nodes"), c.met.ver()}
	var ik uint64
	for _, k := range issueKinds {
		ik = ik*31 + c.tableVer(k)
	}
	d.mu.Lock()
	ovChanged := ovKey != d.ovKey
	issuesChanged := ik != d.issueKey
	d.ovKey = ovKey
	d.issueKey = ik
	d.mu.Unlock()

	if ovChanged {
		ov := c.computeOverview()
		d.mu.Lock()
		d.overview = ov
		d.mu.Unlock()
		c.emit("overview", ov)
	}
	if issuesChanged {
		issues := c.computeIssues()
		b, _ := json.Marshal(issues)
		d.mu.Lock()
		same := bytes.Equal(b, d.issueJSON)
		d.issues = issues
		d.issueJSON = b
		d.mu.Unlock()
		if !same {
			c.emit("issues", issues)
		}
	}
}

// Overview returns the last computed overview.
func (c *Cluster) Overview() Overview {
	c.derived.mu.Lock()
	defer c.derived.mu.Unlock()
	return c.derived.overview
}

// Issues returns the last computed issues.
func (c *Cluster) Issues() []Issue {
	c.derived.mu.Lock()
	defer c.derived.mu.Unlock()
	if c.derived.issues == nil {
		return []Issue{}
	}
	return c.derived.issues
}

// FindIssue returns one issue by ID.
func (c *Cluster) FindIssue(id string) *Issue {
	for _, i := range c.Issues() {
		if i.ID == id {
			ii := i
			return &ii
		}
	}
	return nil
}

// owner resolves the managing workload of a pod: the Deployment behind a
// ReplicaSet, the CronJob behind a Job, or the direct controller.
func (c *Cluster) owner(ns string, refs []metav1.OwnerReference) (kind, name string) {
	ref := metav1.GetControllerOfNoCopy(&metav1.ObjectMeta{OwnerReferences: refs})
	if ref == nil {
		return "", ""
	}
	switch ref.Kind {
	case "ReplicaSet":
		if o := c.getObj("ReplicaSets", ns, ref.Name); o != nil {
			if k, n := c.owner(ns, o.(*appsv1.ReplicaSet).OwnerReferences); k != "" {
				return k, n
			}
		}
		return "ReplicaSets", ref.Name
	case "Job":
		if o := c.getObj("Jobs", ns, ref.Name); o != nil {
			if k, n := c.owner(ns, o.(*batchv1.Job).OwnerReferences); k != "" {
				return k, n
			}
		}
		return "Jobs", ref.Name
	}
	if k := c.kindByKind(ref.Kind, ref.APIVersion); k != nil {
		return k.Name, ref.Name
	}
	return "", ""
}

// directOwner returns the controller reference as a table kind name.
func (c *Cluster) directOwner(refs []metav1.OwnerReference) (kind, name string) {
	ref := metav1.GetControllerOfNoCopy(&metav1.ObjectMeta{OwnerReferences: refs})
	if ref == nil {
		return "", ""
	}
	if k := c.kindByKind(ref.Kind, ref.APIVersion); k != nil {
		return k.Name, ref.Name
	}
	return ref.Kind, ref.Name
}

func singular(kind string) string {
	switch {
	case kind == "Endpoints":
		return "Endpoints"
	case strings.HasSuffix(kind, "Classes"):
		return strings.TrimSuffix(kind, "es")
	case strings.HasSuffix(kind, "Ingresses"):
		return strings.TrimSuffix(kind, "es")
	case strings.HasSuffix(kind, "Policies"):
		return strings.TrimSuffix(kind, "ies") + "y"
	case strings.HasSuffix(kind, "ies"):
		return strings.TrimSuffix(kind, "ies") + "y"
	}
	return strings.TrimSuffix(kind, "s")
}

func kubectlKind(kind string) string {
	switch kind {
	case "Deployments":
		return "deploy"
	case "StatefulSets":
		return "sts"
	case "DaemonSets":
		return "ds"
	case "ReplicaSets":
		return "rs"
	case "Jobs":
		return "job"
	case "CronJobs":
		return "cronjob"
	}
	return strings.ToLower(singular(kind))
}

// warningEvents returns the recent warning messages for an object.
func (c *Cluster) warningEvents(uid string, max int) []*corev1.Event {
	var out []*corev1.Event
	for _, o := range c.byIndex("Events", "involved", uid) {
		e := o.(*corev1.Event)
		if e.Type == corev1.EventTypeWarning {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return eventTime(out[i]).After(eventTime(out[j])) })
	seen := map[string]bool{}
	var uniq []*corev1.Event
	for _, e := range out {
		if !seen[e.Reason] {
			seen[e.Reason] = true
			uniq = append(uniq, e)
		}
		if len(uniq) == max {
			break
		}
	}
	return uniq
}

func appName(p *corev1.Pod, ownerName string) string {
	if v := p.Labels["app.kubernetes.io/name"]; v != "" {
		return v
	}
	if v := p.Labels["app"]; v != "" {
		return v
	}
	if ownerName != "" {
		return ownerName
	}
	return p.Name
}

func exitCodeHint(code int32) string {
	switch code {
	case 137:
		return "Exit code 137 means the process got SIGKILL. A failing liveness probe or a node memory limit often sends it."
	case 139:
		return "Exit code 139 means the process crashed with a segmentation fault."
	case 143:
		return "Exit code 143 means the process got SIGTERM and stopped."
	case 1:
		return "Exit code 1 is a generic application error. The previous logs usually show the cause."
	case 127:
		return "Exit code 127 means the container command was not found in the image."
	case 126:
		return "Exit code 126 means the container command is not executable."
	}
	return fmt.Sprintf("The process exited with code %d.", code)
}

func containerLimit(p *corev1.Pod, name string, r corev1.ResourceName) (resource.Quantity, bool) {
	for _, ct := range p.Spec.Containers {
		if ct.Name == name {
			q, ok := ct.Resources.Limits[r]
			return q, ok
		}
	}
	return resource.Quantity{}, false
}

type issueGroup struct {
	issue    *Issue
	restarts int32
	pods     []string
}

// computeIssues runs every detection rule.
func (c *Cluster) computeIssues() []Issue {
	groups := map[string]*issueGroup{}
	var order []string
	add := func(id string, mk func() *Issue, pod string, restarts int32) {
		g := groups[id]
		if g == nil {
			i := mk()
			if i == nil {
				return
			}
			i.ID = id
			g = &issueGroup{issue: i}
			groups[id] = g
			order = append(order, id)
		}
		if pod != "" {
			g.pods = append(g.pods, pod)
		}
		g.restarts += restarts
	}
	ownersWithPodIssues := map[string]bool{}
	now := time.Now()

	for _, o := range c.list("Pods") {
		p := o.(*corev1.Pod)
		if podTerminal(p) || p.DeletionTimestamp != nil {
			continue
		}
		ok, on := c.owner(p.Namespace, p.OwnerReferences)
		app := appName(p, on)
		ownerKey := p.Namespace + "/" + ok + "/" + on
		if on == "" {
			ownerKey = p.Namespace + "/Pods/" + p.Name
		}
		for _, cs := range p.Status.ContainerStatuses {
			w := cs.State.Waiting
			if w == nil {
				continue
			}
			switch w.Reason {
			case "CrashLoopBackOff":
				ownersWithPodIssues[ownerKey] = true
				pp, cs := p, cs
				add("crash:"+ownerKey+"/"+cs.Name, func() *Issue { return c.crashIssue(pp, cs, ok, on, app) }, p.Name, cs.RestartCount)
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull":
				ownersWithPodIssues[ownerKey] = true
				pp, cs, w := p, cs, w
				add("image:"+ownerKey+"/"+cs.Name, func() *Issue { return c.imageIssue(pp, cs, w, ok, on, app) }, p.Name, 0)
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				ownersWithPodIssues[ownerKey] = true
				pp, cs, w := p, cs, w
				add("config:"+ownerKey+"/"+cs.Name, func() *Issue {
					return &Issue{Sev: "er", Title: app + " cannot start its container", Kind: "Pods", NS: pp.Namespace, Obj: pp.Name,
						Reason: w.Reason, Summary: "Kubernetes cannot create container " + cs.Name + ": " + w.Message,
						Evidence: []string{w.Message}, Fix: "Create the missing ConfigMap or Secret, or fix the reference in the pod template.",
						Cmds: []string{"kubectl describe pod " + pp.Name + " -n " + pp.Namespace}}
				}, p.Name, 0)
			}
		}
		if p.Status.Phase == corev1.PodPending && p.Spec.NodeName == "" {
			for _, cond := range p.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable &&
					now.Sub(p.CreationTimestamp.Time) > 30*time.Second {
					ownersWithPodIssues[ownerKey] = true
					pp, cond := p, cond
					add("sched:"+ownerKey, func() *Issue { return c.schedulingIssue(pp, cond, ok, on) }, p.Name, 0)
				}
			}
		}
	}

	for _, o := range c.list("Nodes") {
		n := o.(*corev1.Node)
		for _, cond := range n.Status.Conditions {
			switch {
			case cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue:
				nn, cond := n, cond
				add("node:"+n.Name, func() *Issue {
					return &Issue{Sev: "er", Title: "Node " + nn.Name + " is not ready", Kind: "Nodes", Obj: nn.Name,
						Reason: "NotReady · " + cond.Reason, Meta: "since " + ago(cond.LastTransitionTime.Time),
						Summary:  "The kubelet on " + nn.Name + " stopped reporting as ready. Pods on this node can be evicted.",
						Evidence: []string{"Ready condition: " + string(cond.Status) + ", reason " + cond.Reason, cond.Message},
						Fix:      "Check the node in your cloud console. If the node is gone, drain and delete it.",
						Cmds:     []string{"kubectl describe node " + nn.Name}}
				}, "", 0)
			case (cond.Type == corev1.NodeMemoryPressure || cond.Type == corev1.NodeDiskPressure || cond.Type == corev1.NodePIDPressure) && cond.Status == corev1.ConditionTrue:
				nn, cond := n, cond
				add("pressure:"+n.Name+"/"+string(cond.Type), func() *Issue {
					return &Issue{Sev: "wa", Title: "Node " + nn.Name + " has " + strings.TrimPrefix(string(cond.Type), "Node"), Kind: "Nodes", Obj: nn.Name,
						Reason: string(cond.Type), Meta: "since " + ago(cond.LastTransitionTime.Time),
						Summary: cond.Message, Evidence: []string{cond.Message},
						Fix:  "Move workloads off the node or add capacity. The kubelet evicts pods while the pressure lasts.",
						Cmds: []string{"kubectl describe node " + nn.Name}}
				}, "", 0)
			}
		}
	}

	for _, o := range c.list("Deployments") {
		d := o.(*appsv1.Deployment)
		if ownersWithPodIssues[d.Namespace+"/Deployments/"+d.Name] {
			continue
		}
		for _, cond := range d.Status.Conditions {
			if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse {
				dd, cond := d, cond
				add("rollout:"+d.Namespace+"/"+d.Name, func() *Issue {
					return &Issue{Sev: "er", Title: dd.Name + " rollout is stuck", Kind: "Deployments", NS: dd.Namespace, Obj: dd.Name,
						Reason: cond.Reason, Meta: fmt.Sprintf("%d/%d ready", dd.Status.ReadyReplicas, ptr.Deref(dd.Spec.Replicas, 1)),
						Summary: cond.Message, Evidence: []string{cond.Message},
						Fix:  "Check the new ReplicaSet and its pods. Roll back with kubectl rollout undo if the new revision is broken.",
						Cmds: []string{"kubectl rollout status deploy/" + dd.Name + " -n " + dd.Namespace, "kubectl rollout history deploy/" + dd.Name + " -n " + dd.Namespace}}
				}, "", 0)
			}
		}
	}
	for _, o := range c.list("StatefulSets") {
		s := o.(*appsv1.StatefulSet)
		want := ptr.Deref(s.Spec.Replicas, 1)
		if ownersWithPodIssues[s.Namespace+"/StatefulSets/"+s.Name] || s.Status.ReadyReplicas >= want || now.Sub(s.CreationTimestamp.Time) < 5*time.Minute {
			continue
		}
		ss := s
		add("sts:"+s.Namespace+"/"+s.Name, func() *Issue {
			return &Issue{Sev: "wa", Title: ss.Name + " is not fully ready", Kind: "StatefulSets", NS: ss.Namespace, Obj: ss.Name,
				Reason: "Unavailable", Meta: fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, want),
				Summary:  fmt.Sprintf("%d of %d replicas are ready.", ss.Status.ReadyReplicas, want),
				Evidence: []string{fmt.Sprintf("Current revision %s, update revision %s", ss.Status.CurrentRevision, ss.Status.UpdateRevision)},
				Fix:      "Open the pods of this StatefulSet and check their events.",
				Cmds:     []string{"kubectl rollout status sts/" + ss.Name + " -n " + ss.Namespace}}
		}, "", 0)
	}
	for _, o := range c.list("Jobs") {
		j := o.(*batchv1.Job)
		for _, cond := range j.Status.Conditions {
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue && now.Sub(cond.LastTransitionTime.Time) < 24*time.Hour {
				jj, cond := j, cond
				add("job:"+j.Namespace+"/"+j.Name, func() *Issue {
					return &Issue{Sev: "wa", Title: jj.Name + " failed", Kind: "Jobs", NS: jj.Namespace, Obj: jj.Name,
						Reason: cond.Reason, Meta: "failed " + ago(cond.LastTransitionTime.Time) + " ago",
						Summary: cond.Message, Evidence: []string{cond.Reason + ": " + cond.Message, fmt.Sprintf("%d failed pods", jj.Status.Failed)},
						Fix:  "Read the logs of the failed pods, fix the cause, and run the job again.",
						Cmds: []string{"kubectl describe job " + jj.Name + " -n " + jj.Namespace, "kubectl logs job/" + jj.Name + " -n " + jj.Namespace}}
				}, "", 0)
			}
		}
	}
	for _, o := range c.list("PersistentVolumeClaims") {
		pvc := o.(*corev1.PersistentVolumeClaim)
		if pvc.Status.Phase != corev1.ClaimPending || now.Sub(pvc.CreationTimestamp.Time) < 2*time.Minute {
			continue
		}
		pp := pvc
		add("pvc:"+pvc.Namespace+"/"+pvc.Name, func() *Issue {
			ev := []string{"Status Pending for " + ago(pp.CreationTimestamp.Time)}
			for _, e := range c.warningEvents(string(pp.UID), 2) {
				ev = append(ev, e.Reason+": "+e.Message)
			}
			return &Issue{Sev: "wa", Title: pp.Name + " is not bound", Kind: "PersistentVolumeClaims", NS: pp.Namespace, Obj: pp.Name,
				Reason: "Pending", Meta: "Pending " + ago(pp.CreationTimestamp.Time),
				Summary: "No PersistentVolume is bound to this claim.", Evidence: ev,
				Fix:  "Check the storage class and its provisioner. A WaitForFirstConsumer class binds only after a pod uses the claim.",
				Cmds: []string{"kubectl describe pvc " + pp.Name + " -n " + pp.Namespace}}
		}, "", 0)
	}
	for _, o := range c.list("certificates.cert-manager.io") {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, ci := range conds {
			cm, _ := ci.(map[string]any)
			if cm["type"] == "Ready" && cm["status"] == "False" {
				uu := u
				msg, _ := cm["message"].(string)
				reason, _ := cm["reason"].(string)
				add("cert:"+u.GetNamespace()+"/"+u.GetName(), func() *Issue {
					return &Issue{Sev: "wa", Title: uu.GetName() + " certificate is not ready", Kind: "certificates.cert-manager.io", NS: uu.GetNamespace(), Obj: uu.GetName(),
						Reason: reason, Summary: msg, Evidence: []string{msg},
						Fix:  "Check the CertificateRequest and the Issuer status.",
						Cmds: []string{"kubectl describe certificate " + uu.GetName() + " -n " + uu.GetNamespace()}}
				}, "", 0)
			}
		}
	}

	out := make([]Issue, 0, len(order))
	for _, id := range order {
		g := groups[id]
		i := *g.issue
		if len(g.pods) > 1 {
			sort.Strings(g.pods)
			i.Pods = g.pods
			i.Meta = fmt.Sprintf("%d pods", len(g.pods))
			if g.restarts > 0 {
				i.Meta += fmt.Sprintf(" · %d restarts", g.restarts)
			}
		}
		if i.Evidence == nil {
			i.Evidence = []string{}
		}
		if i.Cmds == nil {
			i.Cmds = []string{}
		}
		out = append(out, i)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Sev != out[b].Sev {
			return out[a].Sev == "er"
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func (c *Cluster) crashIssue(p *corev1.Pod, cs corev1.ContainerStatus, ownerKind, ownerName, app string) *Issue {
	i := &Issue{Sev: "er", Title: app + " keeps restarting", Kind: "Pods", NS: p.Namespace, Obj: p.Name, Reason: "CrashLoopBackOff",
		Meta: fmt.Sprintf("%d restarts in %s", cs.RestartCount, ago(p.CreationTimestamp.Time))}
	last := cs.LastTerminationState.Terminated
	logCmd := "kubectl logs " + p.Name + " -n " + p.Namespace + " --previous"
	if len(p.Spec.Containers) > 1 {
		logCmd += " -c " + cs.Name
	}
	i.Cmds = []string{"kubectl describe pod " + p.Name + " -n " + p.Namespace, logCmd}
	if ownerKind != "" && ownerKind != "ReplicaSets" {
		i.Cmds = append(i.Cmds, "kubectl rollout history "+kubectlKind(ownerKind)+"/"+ownerName+" -n "+p.Namespace)
	}
	oom := last != nil && last.Reason == "OOMKilled"
	if last != nil {
		i.Reason += " · " + orDash(last.Reason)
		i.Evidence = append(i.Evidence, fmt.Sprintf("Last state: Terminated, reason %s, exit code %d", orDash(last.Reason), last.ExitCode))
	}
	lim, hasLim := containerLimit(p, cs.Name, corev1.ResourceMemory)
	if oom {
		ev := "Memory limit " + lim.String()
		if !hasLim {
			ev = "The container has no memory limit, so the node ran out of memory"
		}
		if _, mem, ok := c.PodUsage(string(p.UID)); ok && mem > 0 {
			ev += "; current pod usage " + fmtBytes(mem)
		}
		i.Evidence = append(i.Evidence, ev)
		i.Summary = fmt.Sprintf("Container %s is killed because it uses more memory than its limit of %s.", cs.Name, lim.String())
		if !hasLim {
			i.Summary = fmt.Sprintf("Container %s is killed for using too much memory on the node.", cs.Name)
		}
	} else if last != nil {
		i.Summary = fmt.Sprintf("Container %s exits shortly after it starts. %s", cs.Name, exitCodeHint(last.ExitCode))
	} else {
		i.Summary = fmt.Sprintf("Container %s restarts again and again.", cs.Name)
	}
	for _, e := range c.warningEvents(string(p.UID), 2) {
		i.Evidence = append(i.Evidence, e.Reason+": "+e.Message)
	}
	if ownerKind == "Deployments" {
		if o := c.getObj("Deployments", p.Namespace, ownerName); o != nil {
			if rev := o.(*appsv1.Deployment).Annotations["deployment.kubernetes.io/revision"]; rev != "" {
				i.Evidence = append(i.Evidence, "Deployment "+ownerName+" is at revision "+rev)
			}
		}
	}
	switch {
	case oom && ownerKind != "" && ownerKind != "ReplicaSets" && ownerKind != "Jobs":
		cur := lim.Value()
		next := proposedMemory(cur)
		i.Fix = fmt.Sprintf("Raise the memory limit on %s %s (%s → %s).", singular(ownerKind), ownerName, fmtBytes(cur), fmtBytes(next))
		if ownerKind == "Deployments" {
			i.Fix += " The Pod is owned by a ReplicaSet, so the change belongs on the Deployment."
		}
		i.Action = &IssueAction{Label: "Review fix as diff", Type: "diff", Kind: ownerKind, NS: p.Namespace, Name: ownerName, Container: cs.Name}
	case oom:
		i.Fix = "Raise the memory limit of container " + cs.Name + " or lower its memory use."
		i.Action = &IssueAction{Label: "Open previous logs", Type: "logs", Kind: "Pods", NS: p.Namespace, Name: p.Name, Container: cs.Name}
	default:
		i.Fix = "Read the logs of the previous container to find the error, then fix the application or its configuration."
		i.Action = &IssueAction{Label: "Open previous logs", Type: "logs", Kind: "Pods", NS: p.Namespace, Name: p.Name, Container: cs.Name}
	}
	return i
}

// proposedMemory doubles a limit, with at least 256Mi more, rounded to Mi.
func proposedMemory(cur int64) int64 {
	const mi = 1 << 20
	if cur <= 0 {
		return 512 * mi
	}
	next := cur * 2
	if next-cur < 256*mi {
		next = cur + 256*mi
	}
	return (next + mi - 1) / mi * mi
}

func (c *Cluster) imageIssue(p *corev1.Pod, cs corev1.ContainerStatus, w *corev1.ContainerStateWaiting, ownerKind, ownerName, app string) *Issue {
	i := &Issue{Sev: "er", Title: app + " image cannot be pulled", Kind: "Pods", NS: p.Namespace, Obj: p.Name, Reason: w.Reason,
		Summary: "Kubernetes cannot pull image " + cs.Image + ".",
		Cmds:    []string{"kubectl describe pod " + p.Name + " -n " + p.Namespace}}
	var pulls int64
	for _, e := range c.warningEvents(string(p.UID), 3) {
		i.Evidence = append(i.Evidence, e.Message)
		pulls += eventCount(e)
	}
	if len(i.Evidence) == 0 && w.Message != "" {
		i.Evidence = append(i.Evidence, w.Message)
	}
	i.Meta = "waiting " + ago(p.CreationTimestamp.Time)
	if pulls > 0 {
		i.Meta = fmt.Sprintf("%d pull attempts", pulls)
	}
	switch {
	case strings.Contains(w.Message, "not found") || strings.Contains(w.Message, "manifest unknown"):
		i.Summary += " The tag does not exist in the registry."
	case strings.Contains(w.Message, "unauthorized") || strings.Contains(w.Message, "authentication") || strings.Contains(w.Message, "denied"):
		i.Summary += " The registry rejected the credentials. Check imagePullSecrets."
	}
	i.Fix = "Fix the image reference on " + orDash(singular(ownerKind)) + " " + ownerName + ", or push the missing image to the registry."
	var annotations map[string]string
	if ownerKind != "" {
		if o := c.getObj(ownerKind, p.Namespace, ownerName); o != nil {
			if m := objMeta(o); m != nil {
				annotations = m.GetAnnotations()
			}
		}
	}
	if rel := annotations["meta.helm.sh/release-name"]; rel != "" {
		relNS := annotations["meta.helm.sh/release-namespace"]
		if relNS == "" {
			relNS = p.Namespace
		}
		i.Evidence = append(i.Evidence, "Helm release "+relNS+"/"+rel+" manages "+singular(ownerKind)+" "+ownerName)
		i.Fix = "Roll back Helm release " + rel + " to the last revision that deployed, or push the missing tag to the registry."
		i.Cmds = append(i.Cmds, "helm history "+rel+" -n "+relNS)
		i.Action = &IssueAction{Label: "Roll back Helm release", Type: "helm-rollback", NS: relNS, Name: rel}
	}
	return i
}

func (c *Cluster) schedulingIssue(p *corev1.Pod, cond corev1.PodCondition, ownerKind, ownerName string) *Issue {
	i := &Issue{Sev: "wa", Title: p.Name + " cannot be scheduled", Kind: "Pods", NS: p.Namespace, Obj: p.Name,
		Reason: "FailedScheduling", Meta: "Pending " + ago(p.CreationTimestamp.Time),
		Summary: cond.Message, Evidence: []string{cond.Message},
		Cmds: []string{"kubectl describe pod " + p.Name + " -n " + p.Namespace}}
	var cpu, mem int64
	for _, ct := range p.Spec.Containers {
		cpu += ct.Resources.Requests.Cpu().MilliValue()
		mem += ct.Resources.Requests.Memory().Value()
	}
	if cpu > 0 || mem > 0 {
		i.Evidence = append(i.Evidence, "Pod requests cpu "+fmtCPU(cpu)+", memory "+fmtBytes(mem))
	}
	var cordoned []string
	for _, o := range c.list("Nodes") {
		n := o.(*corev1.Node)
		if n.Spec.Unschedulable && nodeReady(n) == "Ready" {
			cordoned = append(cordoned, n.Name)
		}
	}
	sort.Strings(cordoned)
	if len(cordoned) > 0 {
		i.Evidence = append(i.Evidence, "Cordoned nodes: "+joinMax(cordoned, 3))
		i.Fix = "Uncordon " + cordoned[0] + " if its maintenance is finished, or add capacity."
		i.Action = &IssueAction{Label: "Uncordon " + cordoned[0], Type: "uncordon", Node: cordoned[0]}
		i.Cmds = append(i.Cmds, "kubectl get nodes --field-selector spec.unschedulable=true")
	} else {
		i.Fix = "Add capacity, or lower the requests"
		if ownerName != "" {
			i.Fix += " on " + singular(ownerKind) + " " + ownerName
		}
		i.Fix += "."
	}
	return i
}
