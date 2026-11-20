package kube

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// accessTTL is how long st8ks trusts the namespaces and the rules of
	// the user before it reads them again.
	accessTTL = 55 * time.Second
	// accessEvery is how often st8ks checks if the namespaces of a
	// fallback watch changed.
	accessEvery = 60 * time.Second
	// maxFallbackNamespaces caps the namespaces that one kind watches when
	// the credentials cannot list it in all namespaces.
	maxFallbackNamespaces = 50
	accessWorkers         = 8
)

// nsAccess caches the namespaces of the cluster and the rules of the user
// in each of them. st8ks uses it when the credentials cannot list a kind
// in all namespaces.
type nsAccess struct {
	mu    sync.Mutex
	at    time.Time
	names []string // the namespaces to check, the namespace of the context first
	rules map[string]nsRules

	reviewMu sync.Mutex
	reviews  map[string]accessReview
}

type nsRules struct {
	rules      []authv1.ResourceRule
	incomplete bool
	failed     bool
}

type accessReview struct {
	at      time.Time
	allowed bool
}

// accessSnapshot returns the namespaces to check and the rules in each.
// It reads them again when they are older than accessTTL. Callers that
// arrive during a refresh wait for it, so a burst of forbidden watches
// at startup costs one round of requests.
func (c *Cluster) accessSnapshot(ctx context.Context) ([]string, map[string]nsRules) {
	a := &c.access
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rules != nil && time.Since(a.at) < accessTTL {
		return a.names, a.rules
	}

	var names []string
	if l, err := c.cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			names = append(names, l.Items[i].Name)
		}
	} else if a.rules != nil && !apierrors.IsForbidden(err) {
		// A network error. Keep the last answer rather than lose the
		// namespaces until the next check.
		return a.names, a.rules
	}
	sort.Strings(names)
	if c.defaultNS != "" {
		names = slices.DeleteFunc(names, func(n string) bool { return n == c.defaultNS })
		names = append([]string{c.defaultNS}, names...)
	}

	rules := make(map[string]nsRules, len(names))
	var mu sync.Mutex
	work := make(chan string)
	var wg sync.WaitGroup
	for range min(accessWorkers, len(names)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ns := range work {
				r := c.reviewRules(ctx, ns)
				mu.Lock()
				rules[ns] = r
				mu.Unlock()
			}
		}()
	}
	for _, ns := range names {
		work <- ns
	}
	close(work)
	wg.Wait()

	a.names, a.rules, a.at = names, rules, time.Now()
	return names, rules
}

func (c *Cluster) reviewRules(ctx context.Context, ns string) nsRules {
	r, err := c.cs.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, &authv1.SelfSubjectRulesReview{
		Spec: authv1.SelfSubjectRulesReviewSpec{Namespace: ns},
	}, metav1.CreateOptions{})
	if err != nil {
		return nsRules{failed: true}
	}
	return nsRules{rules: r.Status.ResourceRules, incomplete: r.Status.Incomplete}
}

// rulesAllow reports if the rules allow every verb on a resource in all
// of its objects. Rules with resource names allow only those objects, so
// they cannot list.
func rulesAllow(rules []authv1.ResourceRule, group, resource string, verbs []string) bool {
	for _, verb := range verbs {
		ok := false
		for _, r := range rules {
			if len(r.ResourceNames) > 0 {
				continue
			}
			if matchRule(r.Verbs, verb) && matchRule(r.APIGroups, group) && matchRule(r.Resources, resource) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func matchRule(values []string, want string) bool {
	for _, v := range values {
		if v == "*" || v == want {
			return true
		}
	}
	return false
}

// accessAllowed asks the API server if the user may do every verb on a
// resource in a namespace. st8ks asks only when the rules review is
// incomplete, for example with a webhook authorizer.
func (c *Cluster) accessAllowed(ctx context.Context, ns, group, resource string, verbs []string) bool {
	key := ns + "\x00" + group + "\x00" + resource + "\x00" + strings.Join(verbs, ",")
	a := &c.access
	a.reviewMu.Lock()
	if r, ok := a.reviews[key]; ok && time.Since(r.at) < accessTTL {
		a.reviewMu.Unlock()
		return r.allowed
	}
	a.reviewMu.Unlock()

	allowed := true
	for _, verb := range verbs {
		r, err := c.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: ns, Verb: verb, Group: group, Resource: resource,
			}},
		}, metav1.CreateOptions{})
		if err != nil || !r.Status.Allowed {
			allowed = false
			break
		}
	}
	a.reviewMu.Lock()
	if a.reviews == nil {
		a.reviews = map[string]accessReview{}
	}
	a.reviews[key] = accessReview{at: time.Now(), allowed: allowed}
	a.reviewMu.Unlock()
	return allowed
}

// permittedNamespaces returns the namespaces where the user may do every
// verb on a resource, the namespace of the context first and then by
// name. checked holds the namespaces that st8ks looked at. It is empty
// when the user cannot list namespaces and the context sets none.
func (c *Cluster) permittedNamespaces(ctx context.Context, group, resource string, verbs ...string) (out, checked []string) {
	names, rules := c.accessSnapshot(ctx)
	for _, ns := range names {
		r := rules[ns]
		ok := !r.failed && rulesAllow(r.rules, group, resource, verbs)
		if !ok && (r.failed || r.incomplete) {
			ok = c.accessAllowed(ctx, ns, group, resource, verbs)
		}
		if ok {
			out = append(out, ns)
		}
	}
	return out, names
}

// fallbackNote is the notice of a list whose kind st8ks watches only in
// some namespaces. live holds the namespaces that st8ks watches,
// permitted counts the namespaces that allow the kind, and checked holds
// the namespaces that st8ks looked at.
func fallbackNote(k *Kind, live []string, permitted int, checked []string) string {
	msg := "Your credentials cannot list " + k.Label + " in all namespaces."
	switch {
	case len(checked) == 0:
		return msg + " Set a namespace on the context to show one."
	case len(live) == 0 && len(checked) == 1:
		return "Your credentials cannot list " + k.Label + " in namespace " + checked[0] + "."
	case len(live) == 0:
		return "Your credentials cannot list " + k.Label + " in any namespace."
	case len(live) == 1 && permitted <= 1:
		return msg + " Showing namespace " + live[0] + " only."
	}
	count := fmt.Sprintf("%d namespaces", len(live))
	if permitted > len(live) {
		count = fmt.Sprintf("%d of %d namespaces", len(live), permitted)
	}
	return msg + " Showing " + count + ": " + nsList(live) + "."
}

// nsList names up to four namespaces and counts the rest.
func nsList(names []string) string {
	if len(names) <= 4 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:4], ", ") + fmt.Sprintf(" and %d more", len(names)-4)
}
