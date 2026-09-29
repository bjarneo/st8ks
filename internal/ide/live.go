package ide

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// clusterScoped lists built-in kinds without a namespace, for when no
// cluster is connected.
var clusterScoped = map[string]bool{
	"Namespace": true, "Node": true, "PersistentVolume": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"CustomResourceDefinition": true, "StorageClass": true, "PriorityClass": true, "IngressClass": true,
	"MutatingWebhookConfiguration": true, "ValidatingWebhookConfiguration": true, "APIService": true,
	"RuntimeClass": true, "CSIDriver": true, "CSINode": true, "VolumeAttachment": true,
}

// namespaced reports if a kind lives in a namespace.
func namespaced(cl Cluster, apiVersion, kind string) bool {
	if cl != nil {
		if r, ok := cl.Resource(apiVersion, kind); ok {
			return r.Namespaced
		}
	}
	return !clusterScoped[kind]
}

// refs lists the objects that a document can become on the cluster: one
// for each build that includes the file, and the plain file.
func refs(ix *Index, cl Cluster, file string, d *Doc) []Ref {
	ns := namespaced(cl, d.APIVersion, d.Kind)
	def := ""
	if cl != nil {
		def = cl.DefaultNamespace()
	}
	if def == "" {
		def = "default"
	}
	var out []Ref
	add := func(n, name string) {
		if !ns {
			n = ""
		}
		r := Ref{APIVersion: d.APIVersion, Kind: d.Kind, NS: n, Name: name}
		for _, x := range out {
			if x == r {
				return
			}
		}
		out = append(out, r)
	}
	if ix != nil {
		for _, t := range ix.Targets[file] {
			n := t.NS
			if n == "" {
				n = d.Namespace
			}
			if n == "" {
				n = def
			}
			add(n, t.Prefix+d.Name+t.Suffix)
		}
	}
	n := d.Namespace
	if n == "" {
		n = def
	}
	add(n, d.Name)
	return out
}

func (e *lintEnv) findWorkload(d *Doc) *Workload {
	for _, r := range refs(e.ix, e.cl, e.path, d) {
		if w := e.cl.Workload(r); w != nil && w.Found {
			return w
		}
	}
	return nil
}

// proposedMemory doubles a memory limit, by at least 256 MiB.
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

func fmtMem(b int64) string {
	const mi = 1 << 20
	if b%(1<<30) == 0 {
		return fmt.Sprintf("%dGi", b>>30)
	}
	return fmt.Sprintf("%dMi", (b+mi-1)/mi)
}

// pinned reports if an image has a fixed tag or a digest.
func pinned(img string) bool {
	t := imageTag(img)
	return t != "" && t != "latest"
}

// shortImage returns the tag of an image, or the image when it has none.
func shortImage(img string) string {
	if t := imageTag(img); t != "" && t != "@" {
		return t
	}
	return img
}

func (e *lintEnv) live(d *Doc, prior []Diag) []Diag {
	if e.cl == nil {
		return nil
	}
	ps := podSpec(d)
	if ps == nil {
		return nil
	}
	w := e.findWorkload(d)
	if w == nil {
		return nil
	}
	ctx := e.cl.Name()
	var out []Diag
	add := func(dg Diag) {
		dg.Live = true
		out = append(out, dg)
	}
	for _, c := range containers(ps) {
		f := w.Containers[c.name]
		if f == nil {
			continue
		}
		var at *yaml.Node = c.node
		if k, _ := pair(c.node, "name"); k != nil {
			at = k
		}
		img := get(c.node, "image")
		switch {
		case f.OOMKills > 0:
			add(e.oom(c.node, at, f, ctx))
		case f.Crash:
			msg := fmt.Sprintf("Pods on %s restart in a crash loop", ctx)
			if f.CrashExit >= 0 {
				msg += fmt.Sprintf(". The last exit code was %d", f.CrashExit)
			}
			add(keyDiag(at, SevWarning, "live-crash", msg))
		}
		if f.PullImage != "" && img != nil && img.Kind == yaml.ScalarNode {
			if img.Value == f.PullImage {
				dg := e.valueDiag(img, SevWarning, "live-image", fmt.Sprintf("%s cannot pull this image. %s", ctx, f.PullMsg))
				if f.PrevImage != "" && f.PrevImage != img.Value {
					dg.Fix = "Revert to " + shortImage(f.PrevImage) + " (previous revision)"
					dg.run = replaceValue(img, f.PrevImage)
				}
				add(dg)
			} else if !e.ix.imageSet(e.path, imageName(img.Value)) {
				add(e.valueDiag(img, SevInfo, "live-image", fmt.Sprintf("Pods on %s cannot pull %s. This file sets %s, so an apply replaces it", ctx, f.PullImage, img.Value)))
			}
		}
		if img != nil {
			for i := range prior {
				if prior[i].Code != "image-tag" || prior[i].Line != img.Line || prior[i].run != nil {
					continue
				}
				pin := ""
				switch {
				case f.PullImage == "" && f.Ready > 0 && pinned(f.Image):
					pin = f.Image
				case f.PrevImage != "" && f.PrevReady && pinned(f.PrevImage):
					pin = f.PrevImage
				}
				if pin != "" && imageName(pin) == imageName(img.Value) {
					prior[i].Fix = "Pin to " + shortImage(pin) + " (runs on " + ctx + ")"
					prior[i].run = replaceValue(img, pin)
				}
			}
		}
	}
	return out
}

func (e *lintEnv) oom(c, at *yaml.Node, f *ContainerFacts, ctx string) Diag {
	lm := dig(c, "resources", "limits", "memory")
	rq := dig(c, "resources", "requests", "memory")
	if lm == nil || lm.Kind != yaml.ScalarNode {
		return keyDiag(at, SevWarning, "live-oom", fmt.Sprintf("Pods on %s were OOMKilled %d times. Set a memory limit that fits the workload", ctx, f.OOMKills))
	}
	fileQ, err := resource.ParseQuantity(lm.Value)
	if err != nil {
		return e.valueDiag(lm, SevWarning, "live-oom", fmt.Sprintf("Pods on %s were OOMKilled %d times at this limit", ctx, f.OOMKills))
	}
	if liveQ, err := resource.ParseQuantity(f.MemLimit); err == nil && fileQ.Cmp(liveQ) > 0 {
		return e.valueDiag(lm, SevInfo, "live-oom", fmt.Sprintf("Pods on %s were OOMKilled at %s. This file sets %s, so an apply raises the limit", ctx, f.MemLimit, lm.Value))
	}
	next := proposedMemory(fileQ.Value())
	dg := e.valueDiag(lm, SevWarning, "live-oom", fmt.Sprintf("Pods on %s were OOMKilled %d times at this limit. Raise the limit or lower the memory use", ctx, f.OOMKills))
	dg.Fix = "Set the limit to " + fmtMem(next)
	fixLimit := replaceValue(lm, fmtMem(next))
	var fixReq func([]string) []string
	if rq != nil && rq.Kind == yaml.ScalarNode {
		if q, err := resource.ParseQuantity(rq.Value); err == nil && q.Value() < next/2 {
			dg.Fix += ", the request to " + fmtMem(next/2)
			fixReq = replaceValue(rq, fmtMem(next/2))
		}
	}
	dg.run = func(ls []string) []string {
		ls = fixLimit(ls)
		if fixReq != nil {
			ls = fixReq(ls)
		}
		return ls
	}
	return dg
}

// liveImages checks the images list of a kustomization against pods that
// cannot pull an image.
func (e *lintEnv) liveImages(k *Kust) []Diag {
	if e.cl == nil || len(k.Images) == 0 {
		return nil
	}
	fails := e.cl.PullFailures()
	if len(fails) == 0 {
		return nil
	}
	var nss []string
	for _, t := range e.ix.Targets[k.Path] {
		if t.NS != "" {
			nss = append(nss, t.NS)
		}
	}
	var out []Diag
	for _, im := range k.Images {
		if im.Tag == nil || im.NewTag == "" {
			continue
		}
		name := im.NewName
		if name == "" {
			name = im.Name
		}
		want := name + ":" + im.NewTag
		for _, f := range fails {
			if f.Image != want || (len(nss) > 0 && !contains(nss, f.NS)) {
				continue
			}
			wait := fmt.Sprintf("%d pods in %s wait for it", f.Pods, f.NS)
			if f.Pods == 1 {
				wait = "1 pod in " + f.NS + " waits for it"
			}
			dg := e.valueDiag(im.Tag, SevWarning, "live-image", fmt.Sprintf("%s cannot pull %s. %s. %s", e.cl.Name(), want, wait, f.Msg))
			dg.Live = true
			if f.PrevImage != "" && imageName(f.PrevImage) == name && pinned(f.PrevImage) {
				if tag := imageTag(f.PrevImage); tag != "@" && tag != im.NewTag {
					dg.Fix = "Revert to " + tag + " (previous revision)"
					dg.run = replaceValue(im.Tag, tag)
				}
			}
			out = append(out, dg)
			break
		}
	}
	return out
}

// firstLine returns the first line of a message.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
