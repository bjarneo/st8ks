package ide

// fieldDoc is a type and a description for a field.
type fieldDoc struct{ typ, doc string }

// kustDocs describes kustomization fields. The cluster schema does not have
// them, because kustomize runs on the client.
var kustDocs = map[string]fieldDoc{
	"apiVersion":            {"string", "kustomize.config.k8s.io/v1beta1 for a kustomization, v1alpha1 for a component."},
	"kind":                  {"string", "Kustomization or Component. Build it with kustomize build or kubectl apply -k."},
	"resources":             {"[]string", "Files, folders with a kustomization, or remote URLs to include in the build."},
	"bases":                 {"[]string", "Deprecated. Use resources."},
	"components":            {"[]string", "Components to add. A component is a reusable kustomization of kind Component."},
	"namespace":             {"string", "Sets the namespace of every namespaced object in the build."},
	"namePrefix":            {"string", "Prepended to the name of every object, and to the references to it."},
	"nameSuffix":            {"string", "Appended to the name of every object, and to the references to it."},
	"labels":                {"[]Label", "Labels to add. includeSelectors also adds them to selectors."},
	"commonLabels":          {"map[string]string", "Deprecated. Adds labels to objects and selectors. Use labels."},
	"commonAnnotations":     {"map[string]string", "Annotations to add to every object."},
	"images":                {"[]Image", "Rewrites image names, tags or digests without a change to the base files."},
	"name":                  {"string", "The image name to match, without a tag."},
	"newName":               {"string", "The image name to use instead."},
	"newTag":                {"string", "The tag to use for images that match name."},
	"digest":                {"string", "The digest to use instead of a tag."},
	"patches":               {"[]Patch", "Strategic merge or JSON 6902 patches, from a file or inline, with an optional target."},
	"patchesStrategicMerge": {"[]string", "Deprecated. Use patches."},
	"patchesJson6902":       {"[]Patch", "Deprecated. Use patches."},
	"target":                {"Selector", "The objects that a patch applies to: group, version, kind, name, namespace or labels."},
	"path":                  {"string", "A file relative to this kustomization."},
	"configMapGenerator":    {"[]ConfigMapArgs", "Generates ConfigMaps. A hash suffix on the name rolls out pods when the data changes."},
	"secretGenerator":       {"[]SecretArgs", "Generates Secrets, with a hash suffix on the name."},
	"generatorOptions":      {"GeneratorOptions", "Options for all generators, such as disableNameSuffixHash."},
	"replicas":              {"[]Replica", "Sets the replicas of workloads by name."},
	"replacements":          {"[]Replacement", "Copies a field value from one object to fields of other objects."},
	"helmCharts":            {"[]HelmChart", "Inflates Helm charts. st8ks builds without Helm, so these fail in the IDE."},
	"crds":                  {"[]string", "CRD files, so that kustomize knows the merge keys of custom resources."},
	"buildMetadata":         {"[]string", "originAnnotations, transformerAnnotations or managedByLabel."},
	"sortOptions":           {"SortOptions", "The order of the objects in the output."},
}

// objDocs are short descriptions for common fields, for when the schema of
// the cluster is not loaded.
var objDocs = map[string]fieldDoc{
	"apiVersion":         {"string", "The versioned schema of this object. The API server rejects versions that it no longer serves."},
	"kind":               {"string", "The REST resource that this object represents."},
	"metadata":           {"ObjectMeta", "Standard object metadata: name, namespace, labels and annotations."},
	"name":               {"string", "Unique within the namespace for this kind."},
	"namespace":          {"string", "The namespace of the object. Kustomize overlays often set it."},
	"labels":             {"map[string]string", "Key and value pairs for selectors, Services and NetworkPolicies."},
	"annotations":        {"map[string]string", "Metadata for tools. Selectors do not use annotations."},
	"spec":               {"object", "The desired state. The controller changes the status to match it."},
	"replicas":           {"integer", "The number of pods. An HPA that targets the workload changes it."},
	"selector":           {"LabelSelector", "The pods that this workload owns. The selector cannot change after creation."},
	"template":           {"PodTemplateSpec", "The pod for each replica. A change starts a rollout."},
	"containers":         {"[]Container", "The containers of the pod. Names must be unique."},
	"image":              {"string", "The container image. Pin a tag or a digest, so rollouts and rollbacks repeat."},
	"resources":          {"ResourceRequirements", "Requests for the scheduler, and limits that the kernel enforces."},
	"requests":           {"map[string]Quantity", "The resources that the scheduler reserves for the container."},
	"limits":             {"map[string]Quantity", "Hard limits. CPU is throttled at the limit. Memory above it kills the container with exit code 137."},
	"memory":             {"Quantity", "Bytes, for example 256Mi or 1Gi."},
	"cpu":                {"Quantity", "Cores, for example 500m for half a core."},
	"env":                {"[]EnvVar", "Environment variables. A change starts a rollout."},
	"value":              {"string", "A literal value. Quote numbers and booleans."},
	"ports":              {"[]ContainerPort", "The ports that the container exposes."},
	"containerPort":      {"integer", "The port number on the pod IP, from 1 to 65535."},
	"livenessProbe":      {"Probe", "Restarts the container when it fails."},
	"readinessProbe":     {"Probe", "Removes the pod from Service endpoints while it fails."},
	"serviceAccountName": {"string", "The ServiceAccount that the pod runs as."},
}

// builtinDoc returns a description for a field when the schema is not
// loaded.
func builtinDoc(kust bool, key string) (fieldDoc, bool) {
	if kust {
		d, ok := kustDocs[key]
		return d, ok
	}
	d, ok := objDocs[key]
	return d, ok
}
