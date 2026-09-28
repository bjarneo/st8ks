package kube

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
}

func (c *Cluster) tailLogs(ns, pod, container string, previous bool, lines int64) string {
	ctx, cancel := context.WithTimeout(c.ctx, 8*time.Second)
	defer cancel()
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &lines}
	b, err := c.cs.CoreV1().Pods(ns).GetLogs(pod, opts).DoRaw(ctx)
	if err != nil {
		return ""
	}
	return string(b)
}

// IssueContext collects the cluster data that explains an issue. It reads
// only. The text goes to the assistant.
func (c *Cluster) IssueContext(id string) (string, []string, error) {
	i := c.FindIssue(id)
	if i == nil {
		return "", nil, fmt.Errorf("the issue is resolved or no longer exists")
	}
	var b strings.Builder
	var cmds []string
	fmt.Fprintf(&b, "Issue: %s\nSeverity: %s\nObject: %s %s/%s\nReason: %s\nDetail: %s\n", i.Title, i.Sev, singular(i.Kind), i.NS, i.Obj, i.Reason, i.Meta)
	if len(i.Pods) > 1 {
		fmt.Fprintf(&b, "Affected pods: %s\n", strings.Join(i.Pods, ", "))
	}
	b.WriteString("Findings from the rule engine:\n")
	for _, e := range i.Evidence {
		b.WriteString("- " + e + "\n")
	}
	b.WriteString("Rule engine suggestion: " + i.Fix + "\n")

	doc, err := c.GetObject(Ref{Kind: i.Kind, NS: i.NS, Name: i.Obj})
	if err == nil {
		cmds = append(cmds, "kubectl get "+kubectlKind(i.Kind)+" "+i.Obj+nsArg(i.NS)+" -o yaml")
		b.WriteString("\n--- " + singular(i.Kind) + " YAML ---\n")
		b.WriteString(clip(doc.YAML, 14000))
		if len(doc.Events) > 0 {
			cmds = append(cmds, "kubectl get events"+nsArg(i.NS)+" --field-selector involvedObject.name="+i.Obj)
			b.WriteString("\n--- Events, newest first ---\n")
			for n, e := range doc.Events {
				if n == 12 {
					break
				}
				fmt.Fprintf(&b, "%s %s x%d (%s ago): %s\n", e.Type, e.Reason, e.Count, ago(time.Unix(e.Last, 0)), e.Msg)
			}
		}
		if doc.Pod != nil {
			for _, ct := range doc.Pod.Containers {
				if ct.Type == "ephemeral" {
					continue
				}
				prev := ct.Restarts > 0
				logs := c.tailLogs(i.NS, i.Obj, ct.Name, prev, 80)
				if logs == "" && prev {
					logs = c.tailLogs(i.NS, i.Obj, ct.Name, false, 80)
					prev = false
				}
				if logs == "" {
					continue
				}
				label := "current"
				flag := ""
				if prev {
					label = "previous"
					flag = " --previous"
				}
				cmds = append(cmds, "kubectl logs "+i.Obj+nsArg(i.NS)+" -c "+ct.Name+flag+" --tail 80")
				fmt.Fprintf(&b, "\n--- Last 80 lines of the %s log of container %s ---\n%s", label, ct.Name, clip(logs, 10000))
			}
		}
		if doc.Managed != nil && doc.Managed.Kind != i.Kind {
			if od, err := c.GetObject(*doc.Managed); err == nil {
				cmds = append(cmds, "kubectl get "+kubectlKind(doc.Managed.Kind)+" "+doc.Managed.Name+nsArg(i.NS)+" -o yaml")
				b.WriteString("\n--- Owner " + singular(doc.Managed.Kind) + " " + doc.Managed.Name + " YAML ---\n")
				b.WriteString(clip(od.YAML, 10000))
			}
		}
	}
	return b.String(), cmds, nil
}

func nsArg(ns string) string {
	if ns == "" {
		return ""
	}
	return " -n " + ns
}

// ClusterContext summarizes the cluster for a general question.
func (c *Cluster) ClusterContext() string {
	var b strings.Builder
	st := c.State()
	ov := c.Overview()
	fmt.Fprintf(&b, "Context: %s\nDistribution: %s %s\nNodes: %d (%d ready, %d cordoned)\nPods running: %d of %d allocatable\n",
		st.Context, st.Dist, st.Version, ov.Nodes, ov.Ready, ov.Cordoned, ov.Pods, ov.PodsCap)
	if ov.CPU.Alloc > 0 {
		fmt.Fprintf(&b, "CPU requests: %s of %s\nMemory requests: %s of %s\n", fmtCPU(ov.CPU.Req), fmtCPU(ov.CPU.Alloc), fmtBytes(ov.Mem.Req), fmtBytes(ov.Mem.Alloc))
	}
	issues := c.Issues()
	if len(issues) == 0 {
		b.WriteString("Open issues: none\n")
	} else {
		b.WriteString("Open issues:\n")
		for _, i := range issues {
			fmt.Fprintf(&b, "- [%s] %s (%s %s/%s): %s. %s\n", i.Sev, i.Title, singular(i.Kind), i.NS, i.Obj, i.Reason, i.Summary)
		}
	}
	return b.String()
}
