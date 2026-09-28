package kube

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Tone codes, one byte per cell.
const (
	tNone  = '-'
	tOK    = 'o'
	tWarn  = 'w'
	tErr   = 'e'
	tMuted = 'm'
)

var errWords = []string{"CrashLoop", "Error", "Failed", "failed", "ImagePull", "ErrImage", "OOM", "Unavailable",
	"Degraded", "Unreachable", "Lost", "NotReady", "Evicted", "expired", "Invalid", "BackOff", "ContainerCannotRun",
	"CreateContainer", "RunContainerError", "DeadlineExceeded"}
var warnWords = []string{"Pending", "Progressing", "Terminating", "SchedulingDisabled", "OutOfSync", "Released",
	"ContainerCreating", "PodInitializing", "Init:", "Unknown", "unknown", "Suspended", "Missing", "SchedulingGated", "Waiting"}
var mutedWords = []string{"Completed", "Complete", "Succeeded", "superseded", "Scaled to 0"}

// statusTone gives the tone of a status word.
func statusTone(s string) byte {
	if s == "" || s == "—" {
		return tNone
	}
	if s == "False" {
		return tErr
	}
	for _, w := range errWords {
		if strings.Contains(s, w) {
			return tErr
		}
	}
	for _, w := range warnWords {
		if strings.Contains(s, w) {
			return tWarn
		}
	}
	for _, w := range mutedWords {
		if strings.Contains(s, w) {
			return tMuted
		}
	}
	return tOK
}

// readyTone gives the tone of "a/b" counts.
func readyTone(a, b int64) byte {
	if a == b {
		return tNone
	}
	if a == 0 {
		return tErr
	}
	return tWarn
}

func condTone(s string) byte {
	switch s {
	case "True":
		return tOK
	case "False":
		return tErr
	case "":
		return tNone
	}
	return tWarn
}

func restartTone(n int64) byte {
	if n > 5 {
		return tErr
	}
	if n > 0 {
		return tWarn
	}
	return tNone
}

// badTones reports if any tone marks a problem.
func badTones(k []byte, skip int) bool {
	for i, t := range k {
		if i != skip && (t == tErr || t == tWarn) {
			return true
		}
	}
	return false
}

func ratio(a, b int64) string { return strconv.FormatInt(a, 10) + "/" + strconv.FormatInt(b, 10) }

func itoa[T ~int | ~int32 | ~int64](n T) string { return strconv.FormatInt(int64(n), 10) }

func unix(t metav1.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixStr(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.Unix(), 10)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// labelString gives sorted "k=v" pairs separated by spaces.
func labelString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
	}
	return b.String()
}

// selectorString formats a label selector.
func selectorString(s *metav1.LabelSelector) string {
	if s == nil {
		return "—"
	}
	if len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0 {
		return "{}"
	}
	sel, err := metav1.LabelSelectorAsSelector(s)
	if err != nil {
		return "—"
	}
	return sel.String()
}

// joinMax joins at most n items and adds "+k more".
func joinMax(items []string, n int) string {
	if len(items) == 0 {
		return "—"
	}
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + " +" + strconv.Itoa(len(items)-n) + " more"
}

// fmtCPU formats millicores like "250m" or "2.5".
func fmtCPU(m int64) string {
	if m < 1000 {
		return strconv.FormatInt(m, 10) + "m"
	}
	return strconv.FormatFloat(float64(m)/1000, 'f', -1, 64)
}

// fmtBytes formats bytes with binary units.
func fmtBytes(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k*k:
		return trimFloat(float64(b)/(k*k*k*k)) + "Ti"
	case b >= k*k*k:
		return trimFloat(float64(b)/(k*k*k)) + "Gi"
	case b >= k*k:
		return strconv.FormatInt(b/(k*k), 10) + "Mi"
	case b >= k:
		return strconv.FormatInt(b/k, 10) + "Ki"
	}
	return strconv.FormatInt(b, 10)
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

func qty(q resource.Quantity) string {
	if q.IsZero() {
		return "0"
	}
	return q.String()
}

// fmtDuration formats a duration like kubectl: 4m12s, 3h, 2d.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 600:
		return fmt.Sprintf("%dm%ds", s/60, s%60)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 8*3600:
		if s%3600/60 == 0 {
			return fmt.Sprintf("%dh", s/3600)
		}
		return fmt.Sprintf("%dh%dm", s/3600, s%3600/60)
	case s < 48*3600:
		return fmt.Sprintf("%dh", s/3600)
	}
	return fmt.Sprintf("%dd", s/86400)
}

// ago formats the time since t.
func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return fmtDuration(time.Since(t))
}
