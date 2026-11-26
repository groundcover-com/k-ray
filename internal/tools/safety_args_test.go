package tools

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kube"
)

// Item 4: command and args are redacted one element at a time, as the object
// path does, before they are joined. (The init container line of a short
// block is tested in safety_brief_test.go.)
const argsPod = `[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1","command":["/app","--password","correct horse battery"],"args":["--client-secret=alpha beta gamma","--db-password","one two three","sh","-c","sleep 3600"]}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}]`

func TestDescribeContainerCommandAndArgsAreRedactedPerElement(t *testing.T) {
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, argsPod), logs: map[string]string{}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := call(t, r, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"}).Text
	mustNotContain(t, out, "correct", "horse", "battery", "alpha", "beta", "gamma", "one two", "two three")
	mustContain(t, out, "command: /app --password [REDACTED:key-name]", "--client-secret=[REDACTED:key-name]", "--db-password [REDACTED:key-name]", "sh -c sleep 3600")
}

func TestDescribeContainerOrdinaryCommandIsUnchanged(t *testing.T) {
	raw := `[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-13T10:00:00Z"},
"spec":{"nodeName":"n","containers":[{"name":"app","image":"x:1","command":["sh","-c","sleep 3600"]}]},
"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}]`
	r, _ := newRunner(t, &fakeReader{objs: objsFrom(t, raw), logs: map[string]string{}, metrics: map[kube.MetricsTarget][]unstructured.Unstructured{}})
	out := call(t, r, "describe", map[string]any{"kind": "pod", "name": "web-1", "namespace": "shop"}).Text
	mustContain(t, out, "command: sh -c sleep 3600")
	mustNotContain(t, out, "REDACTED")
}
