package kube

import (
	"regexp"
	"sort"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

// Kind describes one resource type: how to watch it and how to print it.
type Kind struct {
	Name       string
	Label      string
	Kind       string
	Group      string
	Version    string
	Resource   string
	Short      string
	Namespaced bool
	Section    string
	Cols       []Col
	Actions    []string
	Lazy       bool // watch metadata only until the list opens
	Custom     bool
	Local      bool // not a cluster resource

	newObj func() runtime.Object
	client func(c *Cluster) rest.Interface
	row    func(c *Cluster, obj any) *Row
	strip  func(obj any)
	crd    *crdPrinter
	sig    string
}

func (k *Kind) gvr() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
}

func (k *Kind) apiVersion() string {
	if k.Group == "" {
		return k.Version
	}
	return k.Group + "/" + k.Version
}

func (k *Kind) info() KindInfo {
	api := k.apiVersion()
	if k.Short != "" {
		api += " · " + k.Short
	}
	if k.Local {
		api = "helm · repo"
	}
	return KindInfo{Name: k.Name, Label: k.Label, Kind: k.Kind, API: api, Group: k.Group, Version: k.Version,
		Resource: k.Resource, Namespaced: k.Namespaced, Cols: k.Cols, Actions: k.Actions, Custom: k.Custom}
}

var wideCol = regexp.MustCompile(`URL|Hosts|Endpoints|Message|Address|Image|Subjects|Role$|Claim|Target|Default limit|Ports|Holder|Selector|Provisioner|Controller`)

func col(label, typ string) Col {
	w := "minmax(80px,1fr)"
	switch {
	case typ == CName:
		w = "minmax(220px,2.6fr)"
	case typ == CCPU || typ == CMem || typ == CCPUPct || typ == CMemPct:
		w = "minmax(110px,1.1fr)"
	case typ == CAge:
		w = "minmax(56px,.5fr)"
	case label == "Message":
		w = "minmax(260px,3fr)"
	case wideCol.MatchString(label):
		w = "minmax(140px,1.6fr)"
	}
	return Col{L: label, T: typ, W: w}
}

var (
	cName = col("Name", CName)
	cNs   = col("Namespace", CNs)
	cAge  = col("Age", CAge)
)

func nsCols(cols ...Col) []Col {
	return append(append([]Col{cName, cNs}, cols...), cAge)
}

func clusterCols(cols ...Col) []Col {
	return append(append([]Col{cName}, cols...), cAge)
}

// rowBuilder collects cells and tones for one row.
type rowBuilder struct {
	r *Row
	k []byte
}

func begin(o metav1.Object, n int) *rowBuilder {
	return &rowBuilder{
		r: &Row{U: string(o.GetUID()), N: o.GetNamespace(), M: o.GetName(), T: unix(o.GetCreationTimestamp()),
			C: make([]string, 0, n), L: labelString(o.GetLabels())},
		k: make([]byte, 0, n),
	}
}

func (b *rowBuilder) add(s string, t byte) *rowBuilder {
	b.r.C = append(b.r.C, s)
	b.k = append(b.k, t)
	return b
}

func (b *rowBuilder) text(s string) *rowBuilder { return b.add(orDash(s), tNone) }
func (b *rowBuilder) skip() *rowBuilder         { return b.add("", tNone) }

func (b *rowBuilder) status(s string) *rowBuilder { return b.add(orDash(s), statusTone(s)) }

func (b *rowBuilder) done(skipBad int) *Row {
	b.r.K = string(b.k)
	b.r.B = badTones(b.k, skipBad)
	return b.r
}

func clientCore(c *Cluster) rest.Interface      { return c.cs.CoreV1().RESTClient() }
func clientApps(c *Cluster) rest.Interface      { return c.cs.AppsV1().RESTClient() }
func clientBatch(c *Cluster) rest.Interface     { return c.cs.BatchV1().RESTClient() }
func clientNet(c *Cluster) rest.Interface       { return c.cs.NetworkingV1().RESTClient() }
func clientDisc(c *Cluster) rest.Interface      { return c.cs.DiscoveryV1().RESTClient() }
func clientAuto(c *Cluster) rest.Interface      { return c.cs.AutoscalingV2().RESTClient() }
func clientPolicy(c *Cluster) rest.Interface    { return c.cs.PolicyV1().RESTClient() }
func clientSched(c *Cluster) rest.Interface     { return c.cs.SchedulingV1().RESTClient() }
func clientNode(c *Cluster) rest.Interface      { return c.cs.NodeV1().RESTClient() }
func clientCoord(c *Cluster) rest.Interface     { return c.cs.CoordinationV1().RESTClient() }
func clientAdmission(c *Cluster) rest.Interface { return c.cs.AdmissionregistrationV1().RESTClient() }
func clientStorage(c *Cluster) rest.Interface   { return c.cs.StorageV1().RESTClient() }
func clientRBAC(c *Cluster) rest.Interface      { return c.cs.RbacV1().RESTClient() }
func clientExt(c *Cluster) rest.Interface       { return c.ext.ApiextensionsV1().RESTClient() }

var (
	actWorkload = []string{"edit", "delete", "restart", "scale"}
	actDefault  = []string{"edit", "delete"}
)

// builtinKinds returns the registry of built-in kinds in tree order.
func builtinKinds() []*Kind {
	return []*Kind{
		// Cluster
		{Name: "Nodes", Kind: "Node", Version: "v1", Resource: "nodes", Short: "no", Section: "cluster",
			Cols:    clusterCols(col("Status", CStatus), col("Roles", CText), col("Version", CMono), col("Zone", CMono), col("CPU", CCPUPct), col("Memory", CMemPct), col("Pods", CNum)),
			Actions: []string{"edit", "cordon", "delete"},
			newObj:  func() runtime.Object { return &corev1.Node{} }, client: clientCore, row: nodeRow},
		{Name: "Namespaces", Kind: "Namespace", Version: "v1", Resource: "namespaces", Short: "ns", Section: "cluster",
			Cols: clusterCols(col("Status", CStatus)), Actions: actDefault,
			newObj: func() runtime.Object { return &corev1.Namespace{} }, client: clientCore, row: namespaceRow},
		{Name: "Events", Kind: "Event", Version: "v1", Resource: "events", Short: "ev", Namespaced: true, Section: "",
			Cols:   []Col{col("Type", CStatus), col("Reason", CText), col("Object", CMono), col("Message", CText), col("Count", CNum), col("Last", CTime)},
			newObj: func() runtime.Object { return &corev1.Event{} }, client: clientCore, row: eventRow},

		// Workloads
		{Name: "Pods", Kind: "Pod", Version: "v1", Resource: "pods", Short: "po", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Status", CStatus), col("Ready", CReady), col("Restarts", CNum), col("CPU", CCPU), col("Memory", CMem), col("Node", CMono)),
			Actions: []string{"logs", "shell", "edit", "delete"},
			newObj:  func() runtime.Object { return &corev1.Pod{} }, client: clientCore, row: podRow},
		{Name: "Deployments", Kind: "Deployment", Group: "apps", Version: "v1", Resource: "deployments", Short: "deploy", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Ready", CReady), col("Up-to-date", CNum), col("Available", CNum), col("Conditions", CStatus)),
			Actions: actWorkload, newObj: func() runtime.Object { return &appsv1.Deployment{} }, client: clientApps, row: deploymentRow},
		{Name: "StatefulSets", Kind: "StatefulSet", Group: "apps", Version: "v1", Resource: "statefulsets", Short: "sts", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Ready", CReady), col("Service", CMono)),
			Actions: actWorkload, newObj: func() runtime.Object { return &appsv1.StatefulSet{} }, client: clientApps, row: statefulSetRow},
		{Name: "DaemonSets", Kind: "DaemonSet", Group: "apps", Version: "v1", Resource: "daemonsets", Short: "ds", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Desired", CNum), col("Current", CNum), col("Ready", CReady), col("Node selector", CMono)),
			Actions: []string{"edit", "delete", "restart"}, newObj: func() runtime.Object { return &appsv1.DaemonSet{} }, client: clientApps, row: daemonSetRow},
		{Name: "ReplicaSets", Kind: "ReplicaSet", Group: "apps", Version: "v1", Resource: "replicasets", Short: "rs", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Desired", CNum), col("Current", CNum), col("Ready", CReady)),
			Actions: []string{"edit", "delete", "scale"}, newObj: func() runtime.Object { return &appsv1.ReplicaSet{} }, client: clientApps, row: replicaSetRow},
		{Name: "Jobs", Kind: "Job", Group: "batch", Version: "v1", Resource: "jobs", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Completions", CReady), col("Duration", CMono), col("Status", CStatus)),
			Actions: actDefault, newObj: func() runtime.Object { return &batchv1.Job{} }, client: clientBatch, row: jobRow},
		{Name: "CronJobs", Kind: "CronJob", Group: "batch", Version: "v1", Resource: "cronjobs", Short: "cj", Namespaced: true, Section: "workloads",
			Cols:    nsCols(col("Schedule", CMono), col("Suspend", CStatus), col("Active", CNum), col("Last schedule", CTime)),
			Actions: []string{"edit", "delete", "trigger", "suspend"}, newObj: func() runtime.Object { return &batchv1.CronJob{} }, client: clientBatch, row: cronJobRow},

		// Network
		{Name: "Services", Kind: "Service", Version: "v1", Resource: "services", Short: "svc", Namespaced: true, Section: "network",
			Cols:    nsCols(col("Type", CText), col("Cluster IP", CMono), col("Ports", CMono), col("External IP", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.Service{} }, client: clientCore, row: serviceRow},
		{Name: "Endpoints", Kind: "Endpoints", Version: "v1", Resource: "endpoints", Short: "ep", Namespaced: true, Section: "network",
			Cols: nsCols(col("Endpoints", CMono)),
			//lint:ignore SA1019 the design lists v1 Endpoints, and API servers still serve them.
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.Endpoints{} }, client: clientCore, row: endpointsRow},
		{Name: "EndpointSlices", Kind: "EndpointSlice", Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices", Namespaced: true, Section: "network",
			Cols:    nsCols(col("Address type", CText), col("Ports", CMono), col("Endpoints", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &discoveryv1.EndpointSlice{} }, client: clientDisc, row: endpointSliceRow},
		{Name: "Ingresses", Kind: "Ingress", Group: "networking.k8s.io", Version: "v1", Resource: "ingresses", Short: "ing", Namespaced: true, Section: "network",
			Cols:    nsCols(col("Class", CText), col("Hosts", CMono), col("Address", CMono), col("Ports", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &networkingv1.Ingress{} }, client: clientNet, row: ingressRow},
		{Name: "IngressClasses", Kind: "IngressClass", Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses", Section: "network",
			Cols:    clusterCols(col("Controller", CMono), col("Default", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &networkingv1.IngressClass{} }, client: clientNet, row: ingressClassRow},
		{Name: "NetworkPolicies", Kind: "NetworkPolicy", Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies", Short: "netpol", Namespaced: true, Section: "network",
			Cols:    nsCols(col("Pod selector", CMono), col("Policy types", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &networkingv1.NetworkPolicy{} }, client: clientNet, row: networkPolicyRow},

		// Config
		{Name: "ConfigMaps", Kind: "ConfigMap", Version: "v1", Resource: "configmaps", Short: "cm", Namespaced: true, Section: "config", Lazy: true,
			Cols:    nsCols(col("Keys", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.ConfigMap{} }, client: clientCore, row: configMapRow, strip: stripConfigMap},
		{Name: "Secrets", Kind: "Secret", Version: "v1", Resource: "secrets", Namespaced: true, Section: "config", Lazy: true,
			Cols:    nsCols(col("Type", CMono), col("Keys", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.Secret{} }, client: clientCore, row: secretRow, strip: stripSecret},
		{Name: "ResourceQuotas", Kind: "ResourceQuota", Version: "v1", Resource: "resourcequotas", Short: "quota", Namespaced: true, Section: "config",
			Cols:    nsCols(col("CPU (req)", CMono), col("Memory (req)", CMono), col("Pods", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.ResourceQuota{} }, client: clientCore, row: quotaRow},
		{Name: "LimitRanges", Kind: "LimitRange", Version: "v1", Resource: "limitranges", Short: "limits", Namespaced: true, Section: "config",
			Cols:    nsCols(col("Type", CText), col("Default limit", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.LimitRange{} }, client: clientCore, row: limitRangeRow},
		{Name: "HorizontalPodAutoscalers", Kind: "HorizontalPodAutoscaler", Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers", Short: "hpa", Namespaced: true, Section: "config",
			Cols:    nsCols(col("Target", CMono), col("Metrics", CMono), col("Min", CNum), col("Max", CNum), col("Replicas", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &autoscalingv2.HorizontalPodAutoscaler{} }, client: clientAuto, row: hpaRow},
		{Name: "PodDisruptionBudgets", Kind: "PodDisruptionBudget", Group: "policy", Version: "v1", Resource: "poddisruptionbudgets", Short: "pdb", Namespaced: true, Section: "config",
			Cols:    nsCols(col("Min available", CMono), col("Max unavailable", CMono), col("Allowed disruptions", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &policyv1.PodDisruptionBudget{} }, client: clientPolicy, row: pdbRow},
		{Name: "PriorityClasses", Kind: "PriorityClass", Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses", Short: "pc", Section: "config",
			Cols:    clusterCols(col("Value", CMono), col("Global default", CText), col("Preemption", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &schedulingv1.PriorityClass{} }, client: clientSched, row: priorityClassRow},
		{Name: "RuntimeClasses", Kind: "RuntimeClass", Group: "node.k8s.io", Version: "v1", Resource: "runtimeclasses", Section: "config",
			Cols:    clusterCols(col("Handler", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &nodev1.RuntimeClass{} }, client: clientNode, row: runtimeClassRow},
		{Name: "Leases", Kind: "Lease", Group: "coordination.k8s.io", Version: "v1", Resource: "leases", Namespaced: true, Section: "config",
			Cols:    nsCols(col("Holder", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &coordinationv1.Lease{} }, client: clientCoord, row: leaseRow},
		{Name: "MutatingWebhookConfigurations", Kind: "MutatingWebhookConfiguration", Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations", Section: "config",
			Cols:    clusterCols(col("Webhooks", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &admissionv1.MutatingWebhookConfiguration{} }, client: clientAdmission, row: mutatingRow},
		{Name: "ValidatingWebhookConfigurations", Kind: "ValidatingWebhookConfiguration", Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations", Section: "config",
			Cols:    clusterCols(col("Webhooks", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &admissionv1.ValidatingWebhookConfiguration{} }, client: clientAdmission, row: validatingRow},

		// Storage
		{Name: "PersistentVolumeClaims", Kind: "PersistentVolumeClaim", Version: "v1", Resource: "persistentvolumeclaims", Short: "pvc", Namespaced: true, Section: "storage",
			Cols:    nsCols(col("Status", CStatus), col("Volume", CMono), col("Capacity", CMono), col("Access modes", CMono), col("Storage class", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.PersistentVolumeClaim{} }, client: clientCore, row: pvcRow},
		{Name: "PersistentVolumes", Kind: "PersistentVolume", Version: "v1", Resource: "persistentvolumes", Short: "pv", Section: "storage",
			Cols:    clusterCols(col("Capacity", CMono), col("Access modes", CMono), col("Reclaim policy", CText), col("Status", CStatus), col("Claim", CMono), col("Storage class", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.PersistentVolume{} }, client: clientCore, row: pvRow},
		{Name: "StorageClasses", Kind: "StorageClass", Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses", Short: "sc", Section: "storage",
			Cols:    clusterCols(col("Provisioner", CMono), col("Reclaim policy", CText), col("Binding mode", CText), col("Default", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &storagev1.StorageClass{} }, client: clientStorage, row: storageClassRow},
		{Name: "VolumeAttachments", Kind: "VolumeAttachment", Group: "storage.k8s.io", Version: "v1", Resource: "volumeattachments", Section: "storage",
			Cols:    clusterCols(col("Attacher", CMono), col("PV", CMono), col("Node", CMono), col("Attached", CStatus)),
			Actions: actDefault, newObj: func() runtime.Object { return &storagev1.VolumeAttachment{} }, client: clientStorage, row: volumeAttachmentRow},
		{Name: "CSIDrivers", Kind: "CSIDriver", Group: "storage.k8s.io", Version: "v1", Resource: "csidrivers", Section: "storage",
			Cols:    clusterCols(col("Attach required", CText), col("Pod info on mount", CText), col("Modes", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &storagev1.CSIDriver{} }, client: clientStorage, row: csiDriverRow},

		// Access control
		{Name: "ServiceAccounts", Kind: "ServiceAccount", Version: "v1", Resource: "serviceaccounts", Short: "sa", Namespaced: true, Section: "access",
			Cols:    nsCols(col("Secrets", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &corev1.ServiceAccount{} }, client: clientCore, row: serviceAccountRow},
		{Name: "Roles", Kind: "Role", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles", Namespaced: true, Section: "access",
			Cols:    nsCols(col("Rules", CNum)),
			Actions: actDefault, newObj: func() runtime.Object { return &rbacv1.Role{} }, client: clientRBAC, row: roleRow},
		{Name: "RoleBindings", Kind: "RoleBinding", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings", Namespaced: true, Section: "access",
			Cols:    nsCols(col("Role", CMono), col("Subjects", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &rbacv1.RoleBinding{} }, client: clientRBAC, row: roleBindingRow},
		{Name: "ClusterRoles", Kind: "ClusterRole", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles", Section: "access",
			Cols:    clusterCols(col("Rules", CNum), col("Aggregated", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &rbacv1.ClusterRole{} }, client: clientRBAC, row: clusterRoleRow},
		{Name: "ClusterRoleBindings", Kind: "ClusterRoleBinding", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings", Section: "access",
			Cols:    clusterCols(col("Role", CMono), col("Subjects", CMono)),
			Actions: actDefault, newObj: func() runtime.Object { return &rbacv1.ClusterRoleBinding{} }, client: clientRBAC, row: clusterRoleBindingRow},

		// Custom resources
		{Name: "CustomResourceDefinitions", Label: "Definitions", Kind: "CustomResourceDefinition", Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions", Short: "crd", Section: "crd",
			Cols:    clusterCols(col("Group", CMono), col("Version", CMono), col("Scope", CText), col("Kind", CText)),
			Actions: actDefault, newObj: func() runtime.Object { return &apiextv1.CustomResourceDefinition{} }, client: clientExt, row: crdRow},

		// Helm repositories come from the local Helm config, not the cluster.
		{Name: "HelmRepositories", Label: "Repositories", Kind: "HelmRepository", Section: "helm", Local: true,
			Cols: []Col{cName, col("URL", CMono), col("Type", CText)}},
	}
}

var labelOverride = map[string]string{
	"MutatingWebhookConfigurations":   "Mutating Webhooks",
	"ValidatingWebhookConfigurations": "Validating Webhooks",
	"CSIDrivers":                      "CSI Drivers",
}

var camelSplit = regexp.MustCompile(`([a-z])([A-Z])`)

func kindLabel(name string) string {
	if l, ok := labelOverride[name]; ok {
		return l
	}
	return camelSplit.ReplaceAllString(name, "$1 $2")
}

// ---- Row builders ----

type podState struct {
	reason   string
	ready    int64
	total    int64
	restarts int64
}

// podStatusOf follows the logic of kubectl get pods.
func podStatusOf(p *corev1.Pod) podState {
	st := podState{total: int64(len(p.Spec.Containers)), reason: string(p.Status.Phase)}
	if p.Status.Reason != "" {
		st.reason = p.Status.Reason
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Reason == corev1.PodReasonSchedulingGated {
			st.reason = corev1.PodReasonSchedulingGated
		}
	}
	initializing := false
	sidecars := map[string]bool{}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars[c.Name] = true
			st.total++
		}
	}
	for i, c := range p.Status.InitContainerStatuses {
		st.restarts += int64(c.RestartCount)
		if sidecars[c.Name] {
			if c.Started != nil && *c.Started {
				if c.Ready {
					st.ready++
				}
				continue
			}
		}
		switch {
		case c.State.Terminated != nil && c.State.Terminated.ExitCode == 0:
			continue
		case c.State.Terminated != nil:
			if c.State.Terminated.Reason == "" {
				st.reason = "Init:ExitCode:" + itoa(c.State.Terminated.ExitCode)
			} else {
				st.reason = "Init:" + c.State.Terminated.Reason
			}
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "PodInitializing":
			st.reason = "Init:" + c.State.Waiting.Reason
		default:
			st.reason = "Init:" + itoa(i) + "/" + itoa(len(p.Spec.InitContainers))
		}
		initializing = true
		break
	}
	if !initializing {
		hasRunning := false
		for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
			c := p.Status.ContainerStatuses[i]
			st.restarts += int64(c.RestartCount)
			switch {
			case c.State.Waiting != nil && c.State.Waiting.Reason != "":
				st.reason = c.State.Waiting.Reason
			case c.State.Terminated != nil && c.State.Terminated.Reason != "":
				st.reason = c.State.Terminated.Reason
			case c.State.Terminated != nil:
				st.reason = "ExitCode:" + itoa(c.State.Terminated.ExitCode)
			case c.Ready && c.State.Running != nil:
				hasRunning = true
				st.ready++
			}
		}
		if st.reason == "Completed" && hasRunning {
			st.reason = "NotReady"
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					st.reason = "Running"
				}
			}
		}
	}
	if p.DeletionTimestamp != nil {
		if p.Status.Reason == "NodeLost" {
			st.reason = "Unknown"
		} else if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			st.reason = "Terminating"
		}
	}
	return st
}

// podTerminal reports if the pod finished and no longer uses resources.
func podTerminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// podCaps gives the CPU and memory size used for the usage bars. It uses
// the limit, or the request when a container has no limit.
func podCaps(p *corev1.Pod) (cpu, mem int64) {
	for _, c := range p.Spec.Containers {
		if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			cpu += q.MilliValue()
		} else if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpu += q.MilliValue()
		}
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			mem += q.Value()
		} else if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			mem += q.Value()
		}
	}
	return
}

func podRow(c *Cluster, obj any) *Row {
	p := obj.(*corev1.Pod)
	st := podStatusOf(p)
	b := begin(p, 9)
	b.skip().skip()
	tone := statusTone(st.reason)
	if st.reason == "Running" {
		tone = tOK
	}
	b.add(st.reason, tone)
	rt := readyTone(st.ready, st.total)
	if podTerminal(p) {
		rt = tMuted
	}
	b.add(ratio(st.ready, st.total), rt)
	b.add(itoa(st.restarts), restartTone(st.restarts))
	b.skip().skip()
	b.text(p.Spec.NodeName)
	b.skip()
	cpu, mem := podCaps(p)
	b.r.X = []int64{cpu, mem, st.restarts}
	return b.done(4)
}

func nodeReady(n *corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			switch c.Status {
			case corev1.ConditionTrue:
				return "Ready"
			case corev1.ConditionFalse:
				return "NotReady"
			}
			return "Unknown"
		}
	}
	return "Unknown"
}

func nodeStatus(n *corev1.Node) string {
	s := nodeReady(n)
	if n.Spec.Unschedulable {
		s += ",SchedulingDisabled"
	}
	return s
}

func nodeRoles(n *corev1.Node) string {
	var roles []string
	for k, v := range n.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			roles = append(roles, r)
		} else if k == "kubernetes.io/role" && v != "" {
			roles = append(roles, v)
		}
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

func nodeRow(c *Cluster, obj any) *Row {
	n := obj.(*corev1.Node)
	b := begin(n, 9)
	b.skip()
	b.status(nodeStatus(n))
	b.text(nodeRoles(n))
	b.text(n.Status.NodeInfo.KubeletVersion)
	b.text(n.Labels["topology.kubernetes.io/zone"])
	b.skip().skip()
	b.add(itoa(c.podCount(n.Name)), tNone)
	b.skip()
	a := n.Status.Allocatable
	b.r.X = []int64{a.Cpu().MilliValue(), a.Memory().Value(), a.Pods().Value()}
	return b.done(-1)
}

func namespaceRow(_ *Cluster, obj any) *Row {
	n := obj.(*corev1.Namespace)
	b := begin(n, 3)
	b.skip()
	b.status(string(n.Status.Phase))
	b.skip()
	return b.done(-1)
}

// eventTime gives the last time an event was seen.
func eventTime(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func eventCount(e *corev1.Event) int64 {
	if e.Series != nil && e.Series.Count > 0 {
		return int64(e.Series.Count)
	}
	if e.Count > 0 {
		return int64(e.Count)
	}
	return 1
}

func eventRow(_ *Cluster, obj any) *Row {
	e := obj.(*corev1.Event)
	b := begin(e, 6)
	b.r.L = ""
	t := byte(tMuted)
	if e.Type == corev1.EventTypeWarning {
		t = tWarn
	}
	last := eventTime(e)
	cnt := eventCount(e)
	b.add(e.Type, t)
	b.text(e.Reason)
	b.text(e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name)
	b.text(strings.TrimSpace(e.Message))
	b.add(itoa(cnt), tNone)
	b.add(itoa(last.Unix()), tNone)
	b.r.X = []int64{last.Unix(), cnt}
	// Events are not "bad" objects. Warnings are counted separately.
	b.r.K = string(b.k)
	b.r.B = e.Type == corev1.EventTypeWarning
	return b.r
}

func deploymentCondition(d *appsv1.Deployment, want int64) string {
	if want == 0 {
		return "Scaled to 0"
	}
	if d.Spec.Paused {
		return "Paused"
	}
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
			return "Failed"
		}
		if c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue {
			return "Failed"
		}
	}
	s := d.Status
	if int64(s.AvailableReplicas) >= want && int64(s.UpdatedReplicas) >= want && int64(s.ReadyReplicas) >= want {
		return "Available"
	}
	if s.AvailableReplicas == 0 {
		return "Unavailable"
	}
	return "Progressing"
}

func deploymentRow(_ *Cluster, obj any) *Row {
	d := obj.(*appsv1.Deployment)
	want := int64(ptr.Deref(d.Spec.Replicas, 1))
	ready := int64(d.Status.ReadyReplicas)
	b := begin(d, 7)
	b.skip().skip()
	b.add(ratio(ready, want), readyTone(ready, want))
	b.add(itoa(d.Status.UpdatedReplicas), tNone)
	b.add(itoa(d.Status.AvailableReplicas), tNone)
	b.status(deploymentCondition(d, want))
	b.skip()
	b.r.X = []int64{want}
	return b.done(-1)
}

func statefulSetRow(_ *Cluster, obj any) *Row {
	s := obj.(*appsv1.StatefulSet)
	want := int64(ptr.Deref(s.Spec.Replicas, 1))
	ready := int64(s.Status.ReadyReplicas)
	b := begin(s, 5)
	b.skip().skip()
	b.add(ratio(ready, want), readyTone(ready, want))
	b.text(s.Spec.ServiceName)
	b.skip()
	b.r.X = []int64{want}
	return b.done(-1)
}

func daemonSetRow(_ *Cluster, obj any) *Row {
	d := obj.(*appsv1.DaemonSet)
	want := int64(d.Status.DesiredNumberScheduled)
	ready := int64(d.Status.NumberReady)
	b := begin(d, 7)
	b.skip().skip()
	b.add(itoa(want), tNone)
	b.add(itoa(d.Status.CurrentNumberScheduled), tNone)
	b.add(itoa(ready), readyTone(ready, want))
	b.text(labelString(d.Spec.Template.Spec.NodeSelector))
	b.skip()
	return b.done(-1)
}

func replicaSetRow(_ *Cluster, obj any) *Row {
	r := obj.(*appsv1.ReplicaSet)
	want := int64(ptr.Deref(r.Spec.Replicas, 1))
	ready := int64(r.Status.ReadyReplicas)
	b := begin(r, 6)
	b.skip().skip()
	b.add(itoa(want), tNone)
	b.add(itoa(r.Status.Replicas), tNone)
	b.add(itoa(ready), readyTone(ready, want))
	b.skip()
	b.r.X = []int64{want}
	return b.done(-1)
}

func jobStatus(j *batchv1.Job) string {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
			return "Complete"
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			return "Failed"
		case batchv1.JobSuspended:
			return "Suspended"
		}
	}
	if j.Status.Active > 0 {
		return "Running"
	}
	return "Pending"
}

func jobRow(_ *Cluster, obj any) *Row {
	j := obj.(*batchv1.Job)
	want := int64(ptr.Deref(j.Spec.Completions, 1))
	b := begin(j, 6)
	b.skip().skip()
	st := jobStatus(j)
	done := int64(j.Status.Succeeded)
	rt := byte(tNone)
	if st == "Failed" {
		rt = tErr
	}
	b.add(ratio(done, want), rt)
	dur := "—"
	if j.Status.StartTime != nil {
		end := time.Now()
		if j.Status.CompletionTime != nil {
			end = j.Status.CompletionTime.Time
		}
		dur = fmtDuration(end.Sub(j.Status.StartTime.Time))
	}
	b.text(dur)
	b.status(st)
	b.skip()
	return b.done(-1)
}

func cronJobRow(_ *Cluster, obj any) *Row {
	cj := obj.(*batchv1.CronJob)
	b := begin(cj, 7)
	b.skip().skip()
	b.text(cj.Spec.Schedule)
	susp := ptr.Deref(cj.Spec.Suspend, false)
	if susp {
		b.add("True", tWarn)
	} else {
		b.add("False", tNone)
	}
	b.add(itoa(len(cj.Status.Active)), tNone)
	b.add(unixStr(cj.Status.LastScheduleTime), tNone)
	b.skip()
	return b.done(-1)
}

func servicePorts(s *corev1.Service) string {
	var out []string
	for _, p := range s.Spec.Ports {
		v := itoa(p.Port)
		if p.NodePort != 0 {
			v += ":" + itoa(p.NodePort)
		}
		out = append(out, v+"/"+string(p.Protocol))
	}
	return joinMax(out, 4)
}

func serviceExternal(s *corev1.Service) (string, byte) {
	var ips []string
	for _, i := range s.Status.LoadBalancer.Ingress {
		if i.Hostname != "" {
			ips = append(ips, i.Hostname)
		} else if i.IP != "" {
			ips = append(ips, i.IP)
		}
	}
	ips = append(ips, s.Spec.ExternalIPs...)
	if len(ips) == 0 && s.Spec.Type == corev1.ServiceTypeLoadBalancer {
		return "<pending>", tWarn
	}
	if s.Spec.Type == corev1.ServiceTypeExternalName {
		return s.Spec.ExternalName, tNone
	}
	return joinMax(ips, 2), tNone
}

func serviceRow(_ *Cluster, obj any) *Row {
	s := obj.(*corev1.Service)
	b := begin(s, 7)
	b.skip().skip()
	b.text(string(s.Spec.Type))
	b.text(s.Spec.ClusterIP)
	b.text(servicePorts(s))
	ext, t := serviceExternal(s)
	b.add(ext, t)
	b.skip()
	return b.done(-1)
}

func endpointsRow(_ *Cluster, obj any) *Row {
	//lint:ignore SA1019 the design lists v1 Endpoints, and API servers still serve them.
	e := obj.(*corev1.Endpoints)
	var list []string
	for _, s := range e.Subsets {
		for _, a := range s.Addresses {
			if len(s.Ports) == 0 {
				list = append(list, a.IP)
			}
			for _, p := range s.Ports {
				list = append(list, a.IP+":"+itoa(p.Port))
			}
		}
	}
	b := begin(e, 4)
	b.skip().skip()
	b.text(joinMax(list, 3))
	b.skip()
	return b.done(-1)
}

func endpointSliceRow(_ *Cluster, obj any) *Row {
	e := obj.(*discoveryv1.EndpointSlice)
	var ports, addrs []string
	for _, p := range e.Ports {
		if p.Port != nil {
			ports = append(ports, itoa(*p.Port))
		}
	}
	for _, ep := range e.Endpoints {
		addrs = append(addrs, ep.Addresses...)
	}
	b := begin(e, 6)
	b.skip().skip()
	b.text(string(e.AddressType))
	b.text(joinMax(ports, 4))
	b.text(joinMax(addrs, 3))
	b.skip()
	return b.done(-1)
}

func ingressRow(_ *Cluster, obj any) *Row {
	in := obj.(*networkingv1.Ingress)
	class := ptr.Deref(in.Spec.IngressClassName, in.Annotations["kubernetes.io/ingress.class"])
	var hosts, addrs []string
	for _, r := range in.Spec.Rules {
		if r.Host != "" {
			hosts = append(hosts, r.Host)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{"*"}
	}
	for _, i := range in.Status.LoadBalancer.Ingress {
		if i.Hostname != "" {
			addrs = append(addrs, i.Hostname)
		} else if i.IP != "" {
			addrs = append(addrs, i.IP)
		}
	}
	ports := "80"
	if len(in.Spec.TLS) > 0 {
		ports = "80, 443"
	}
	b := begin(in, 7)
	b.skip().skip()
	b.text(class)
	b.text(joinMax(hosts, 3))
	b.text(joinMax(addrs, 2))
	b.text(ports)
	b.skip()
	return b.done(-1)
}

func ingressClassRow(_ *Cluster, obj any) *Row {
	ic := obj.(*networkingv1.IngressClass)
	b := begin(ic, 4)
	b.skip()
	b.text(ic.Spec.Controller)
	b.text(boolStr(ic.Annotations["ingressclass.kubernetes.io/is-default-class"] == "true"))
	b.skip()
	return b.done(-1)
}

func networkPolicyRow(_ *Cluster, obj any) *Row {
	np := obj.(*networkingv1.NetworkPolicy)
	var types []string
	for _, t := range np.Spec.PolicyTypes {
		types = append(types, string(t))
	}
	b := begin(np, 5)
	b.skip().skip()
	b.text(selectorString(&np.Spec.PodSelector))
	b.text(strings.Join(types, ", "))
	b.skip()
	return b.done(-1)
}

// stripConfigMap drops the values and keeps the keys. The detail view loads
// the full object on demand.
func stripConfigMap(obj any) {
	if cm, ok := obj.(*corev1.ConfigMap); ok {
		for k := range cm.Data {
			cm.Data[k] = ""
		}
		for k := range cm.BinaryData {
			cm.BinaryData[k] = nil
		}
	}
}

// stripSecret drops secret values so they are never kept in memory.
func stripSecret(obj any) {
	if s, ok := obj.(*corev1.Secret); ok {
		for k := range s.Data {
			s.Data[k] = nil
		}
		s.StringData = nil
	}
}

func configMapRow(_ *Cluster, obj any) *Row {
	cm := obj.(*corev1.ConfigMap)
	b := begin(cm, 4)
	b.skip().skip()
	b.add(itoa(len(cm.Data)+len(cm.BinaryData)), tNone)
	b.skip()
	return b.done(-1)
}

func secretRow(_ *Cluster, obj any) *Row {
	s := obj.(*corev1.Secret)
	b := begin(s, 5)
	b.skip().skip()
	b.text(string(s.Type))
	b.add(itoa(len(s.Data)), tNone)
	b.skip()
	return b.done(-1)
}

func quotaPair(q *corev1.ResourceQuota, keys ...corev1.ResourceName) string {
	for _, k := range keys {
		if h, ok := q.Status.Hard[k]; ok {
			u := q.Status.Used[k]
			return qty(u) + " / " + qty(h)
		}
	}
	return "—"
}

func quotaRow(_ *Cluster, obj any) *Row {
	q := obj.(*corev1.ResourceQuota)
	b := begin(q, 6)
	b.skip().skip()
	b.text(quotaPair(q, corev1.ResourceRequestsCPU, corev1.ResourceCPU))
	b.text(quotaPair(q, corev1.ResourceRequestsMemory, corev1.ResourceMemory))
	b.text(quotaPair(q, corev1.ResourcePods))
	b.skip()
	return b.done(-1)
}

func limitRangeRow(_ *Cluster, obj any) *Row {
	lr := obj.(*corev1.LimitRange)
	typ, def := "—", "—"
	if len(lr.Spec.Limits) > 0 {
		l := lr.Spec.Limits[0]
		typ = string(l.Type)
		var parts []string
		if q, ok := l.Default[corev1.ResourceCPU]; ok {
			parts = append(parts, "cpu "+q.String())
		}
		if q, ok := l.Default[corev1.ResourceMemory]; ok {
			parts = append(parts, "memory "+q.String())
		}
		if len(parts) > 0 {
			def = strings.Join(parts, ", ")
		}
	}
	b := begin(lr, 5)
	b.skip().skip()
	b.text(typ)
	b.text(def)
	b.skip()
	return b.done(-1)
}

func hpaMetrics(h *autoscalingv2.HorizontalPodAutoscaler) (string, byte) {
	var parts []string
	tone := byte(tNone)
	for i, m := range h.Spec.Metrics {
		if m.Type != autoscalingv2.ResourceMetricSourceType || m.Resource == nil {
			parts = append(parts, string(m.Type))
			continue
		}
		cur := "<unknown>"
		if i < len(h.Status.CurrentMetrics) {
			cm := h.Status.CurrentMetrics[i]
			if cm.Resource != nil && cm.Resource.Current.AverageUtilization != nil {
				cur = itoa(*cm.Resource.Current.AverageUtilization) + "%"
			} else if cm.Resource != nil && cm.Resource.Current.AverageValue != nil {
				cur = cm.Resource.Current.AverageValue.String()
			}
		}
		if cur == "<unknown>" {
			tone = tWarn
		}
		target := ""
		if m.Resource.Target.AverageUtilization != nil {
			target = itoa(*m.Resource.Target.AverageUtilization) + "%"
		} else if m.Resource.Target.AverageValue != nil {
			target = m.Resource.Target.AverageValue.String()
		}
		parts = append(parts, string(m.Resource.Name)+" "+cur+" / "+target)
	}
	return joinMax(parts, 2), tone
}

func hpaRow(_ *Cluster, obj any) *Row {
	h := obj.(*autoscalingv2.HorizontalPodAutoscaler)
	b := begin(h, 8)
	b.skip().skip()
	b.text(h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name)
	m, t := hpaMetrics(h)
	b.add(m, t)
	b.add(itoa(ptr.Deref(h.Spec.MinReplicas, 1)), tNone)
	b.add(itoa(h.Spec.MaxReplicas), tNone)
	b.add(itoa(h.Status.CurrentReplicas), tNone)
	b.skip()
	return b.done(-1)
}

func intOrStr(v interface{ String() string }, isNil bool) string {
	if isNil {
		return "—"
	}
	return v.String()
}

func pdbRow(_ *Cluster, obj any) *Row {
	p := obj.(*policyv1.PodDisruptionBudget)
	b := begin(p, 6)
	b.skip().skip()
	b.text(intOrStr(p.Spec.MinAvailable, p.Spec.MinAvailable == nil))
	b.text(intOrStr(p.Spec.MaxUnavailable, p.Spec.MaxUnavailable == nil))
	b.add(itoa(p.Status.DisruptionsAllowed), tNone)
	b.skip()
	return b.done(-1)
}

func priorityClassRow(_ *Cluster, obj any) *Row {
	p := obj.(*schedulingv1.PriorityClass)
	b := begin(p, 5)
	b.skip()
	b.text(itoa(p.Value))
	b.text(boolStr(p.GlobalDefault))
	pre := "PreemptLowerPriority"
	if p.PreemptionPolicy != nil {
		pre = string(*p.PreemptionPolicy)
	}
	b.text(pre)
	b.skip()
	return b.done(-1)
}

func runtimeClassRow(_ *Cluster, obj any) *Row {
	r := obj.(*nodev1.RuntimeClass)
	b := begin(r, 3)
	b.skip()
	b.text(r.Handler)
	b.skip()
	return b.done(-1)
}

func leaseRow(_ *Cluster, obj any) *Row {
	l := obj.(*coordinationv1.Lease)
	b := begin(l, 4)
	b.skip().skip()
	b.text(ptr.Deref(l.Spec.HolderIdentity, ""))
	b.skip()
	return b.done(-1)
}

func mutatingRow(_ *Cluster, obj any) *Row {
	w := obj.(*admissionv1.MutatingWebhookConfiguration)
	b := begin(w, 3)
	b.skip()
	b.add(itoa(len(w.Webhooks)), tNone)
	b.skip()
	return b.done(-1)
}

func validatingRow(_ *Cluster, obj any) *Row {
	w := obj.(*admissionv1.ValidatingWebhookConfiguration)
	b := begin(w, 3)
	b.skip()
	b.add(itoa(len(w.Webhooks)), tNone)
	b.skip()
	return b.done(-1)
}

func accessModes(m []corev1.PersistentVolumeAccessMode) string {
	short := map[corev1.PersistentVolumeAccessMode]string{
		corev1.ReadWriteOnce: "RWO", corev1.ReadOnlyMany: "ROX", corev1.ReadWriteMany: "RWX", corev1.ReadWriteOncePod: "RWOP",
	}
	var out []string
	for _, a := range m {
		out = append(out, short[a])
	}
	return strings.Join(out, ",")
}

func pvcRow(_ *Cluster, obj any) *Row {
	p := obj.(*corev1.PersistentVolumeClaim)
	b := begin(p, 8)
	b.skip().skip()
	b.status(string(p.Status.Phase))
	b.text(p.Spec.VolumeName)
	capacity := ""
	if q, ok := p.Status.Capacity[corev1.ResourceStorage]; ok {
		capacity = q.String()
	} else if q, ok := p.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		capacity = q.String()
	}
	b.text(capacity)
	b.text(accessModes(p.Spec.AccessModes))
	b.text(ptr.Deref(p.Spec.StorageClassName, ""))
	b.skip()
	return b.done(-1)
}

func pvRow(_ *Cluster, obj any) *Row {
	p := obj.(*corev1.PersistentVolume)
	b := begin(p, 8)
	b.skip()
	capacity := ""
	if q, ok := p.Spec.Capacity[corev1.ResourceStorage]; ok {
		capacity = q.String()
	}
	b.text(capacity)
	b.text(accessModes(p.Spec.AccessModes))
	b.text(string(p.Spec.PersistentVolumeReclaimPolicy))
	b.status(string(p.Status.Phase))
	claim := ""
	if p.Spec.ClaimRef != nil {
		claim = p.Spec.ClaimRef.Namespace + "/" + p.Spec.ClaimRef.Name
	}
	b.text(claim)
	b.text(p.Spec.StorageClassName)
	b.skip()
	return b.done(-1)
}

func storageClassRow(_ *Cluster, obj any) *Row {
	s := obj.(*storagev1.StorageClass)
	b := begin(s, 6)
	b.skip()
	b.text(s.Provisioner)
	rp := "Delete"
	if s.ReclaimPolicy != nil {
		rp = string(*s.ReclaimPolicy)
	}
	b.text(rp)
	bm := "Immediate"
	if s.VolumeBindingMode != nil {
		bm = string(*s.VolumeBindingMode)
	}
	b.text(bm)
	b.text(boolStr(s.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"))
	b.skip()
	return b.done(-1)
}

func volumeAttachmentRow(_ *Cluster, obj any) *Row {
	v := obj.(*storagev1.VolumeAttachment)
	b := begin(v, 6)
	b.skip()
	b.text(v.Spec.Attacher)
	b.text(ptr.Deref(v.Spec.Source.PersistentVolumeName, ""))
	b.text(v.Spec.NodeName)
	if v.Status.Attached {
		b.add("true", tOK)
	} else {
		b.add("false", tWarn)
	}
	b.skip()
	return b.done(-1)
}

func csiDriverRow(_ *Cluster, obj any) *Row {
	d := obj.(*storagev1.CSIDriver)
	var modes []string
	for _, m := range d.Spec.VolumeLifecycleModes {
		modes = append(modes, string(m))
	}
	b := begin(d, 5)
	b.skip()
	b.text(boolStr(ptr.Deref(d.Spec.AttachRequired, true)))
	b.text(boolStr(ptr.Deref(d.Spec.PodInfoOnMount, false)))
	b.text(strings.Join(modes, ", "))
	b.skip()
	return b.done(-1)
}

func serviceAccountRow(_ *Cluster, obj any) *Row {
	s := obj.(*corev1.ServiceAccount)
	b := begin(s, 4)
	b.skip().skip()
	b.add(itoa(len(s.Secrets)), tNone)
	b.skip()
	return b.done(-1)
}

func roleRow(_ *Cluster, obj any) *Row {
	r := obj.(*rbacv1.Role)
	b := begin(r, 4)
	b.skip().skip()
	b.add(itoa(len(r.Rules)), tNone)
	b.skip()
	return b.done(-1)
}

func subjectsString(subs []rbacv1.Subject) string {
	var out []string
	for _, s := range subs {
		if s.Kind == rbacv1.ServiceAccountKind {
			out = append(out, "ServiceAccount/"+s.Namespace+"/"+s.Name)
		} else {
			out = append(out, s.Kind+"/"+s.Name)
		}
	}
	return joinMax(out, 3)
}

func roleBindingRow(_ *Cluster, obj any) *Row {
	r := obj.(*rbacv1.RoleBinding)
	b := begin(r, 5)
	b.skip().skip()
	b.text(r.RoleRef.Kind + "/" + r.RoleRef.Name)
	b.text(subjectsString(r.Subjects))
	b.skip()
	return b.done(-1)
}

func clusterRoleRow(_ *Cluster, obj any) *Row {
	r := obj.(*rbacv1.ClusterRole)
	b := begin(r, 4)
	b.skip()
	b.add(itoa(len(r.Rules)), tNone)
	b.text(boolStr(r.AggregationRule != nil))
	b.skip()
	return b.done(-1)
}

func clusterRoleBindingRow(_ *Cluster, obj any) *Row {
	r := obj.(*rbacv1.ClusterRoleBinding)
	b := begin(r, 4)
	b.skip()
	b.text(r.RoleRef.Kind + "/" + r.RoleRef.Name)
	b.text(subjectsString(r.Subjects))
	b.skip()
	return b.done(-1)
}

func crdVersion(c *apiextv1.CustomResourceDefinition) *apiextv1.CustomResourceDefinitionVersion {
	for i := range c.Spec.Versions {
		if c.Spec.Versions[i].Storage && c.Spec.Versions[i].Served {
			return &c.Spec.Versions[i]
		}
	}
	for i := range c.Spec.Versions {
		if c.Spec.Versions[i].Served {
			return &c.Spec.Versions[i]
		}
	}
	return nil
}

func crdRow(_ *Cluster, obj any) *Row {
	c := obj.(*apiextv1.CustomResourceDefinition)
	v := ""
	if cv := crdVersion(c); cv != nil {
		v = cv.Name
	}
	b := begin(c, 6)
	b.skip()
	b.text(c.Spec.Group)
	b.text(v)
	b.text(string(c.Spec.Scope))
	b.text(c.Spec.Names.Kind)
	b.skip()
	return b.done(-1)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// metaRow is used while only metadata is watched.
func metaRow(k *Kind) func(c *Cluster, obj any) *Row {
	return func(_ *Cluster, obj any) *Row {
		o := obj.(*metav1.PartialObjectMetadata)
		b := begin(o, len(k.Cols))
		for range k.Cols {
			b.skip()
		}
		return b.done(-1)
	}
}
