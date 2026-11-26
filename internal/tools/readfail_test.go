package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/groundcover-com/kray/internal/kube"
)

// errMarker is planted in the text of every error that these tests inject. It
// must never reach the answer outside the fence.
const errMarker = "ERRTEXT-do-this-now"

func forbidden(resource string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "x", errors.New(errMarker))
}

// outsideFence is the text of a result without the fenced cluster data.
func outsideFence(out string) string {
	i := strings.Index(out, "<<<untrusted")
	j := strings.Index(out, "<<<end")
	if i < 0 || j < i {
		return out
	}
	k := strings.Index(out[j:], "\n")
	if k < 0 {
		return out[:i]
	}
	return out[:i] + out[j+k:]
}

func webFixture(t *testing.T) *fakeReader {
	t.Helper()
	f := &fakeReader{logs: map[string]string{}}
	workload(f, "shop", "web", 2, map[string]any{"app": "web"})
	return f
}

// 1a: a kind that could not be read is not "looked at".
func TestDescribeByNameNamesTheKindsItCouldNotRead(t *testing.T) {
	f := webFixture(t)
	f.listErrs = map[string]error{"cronjobs": forbidden("cronjobs"), "jobs": context.DeadlineExceeded}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"name": "nothing-like-it"})
	mustContain(t, out, "no object named nothing-like-it",
		"looked at deployments, statefulsets, daemonsets, services and pods in all namespaces",
		"could not read: jobs (timed out), cronjobs (not allowed)")
	mustNotContain(t, out, "looked at deployments, statefulsets, daemonsets, jobs")
	mustNotContain(t, outsideFence(out), errMarker)
}

func TestDescribeByNameFoundStillNotesTheKindsItCouldNotRead(t *testing.T) {
	f := webFixture(t)
	f.listErrs = map[string]error{"cronjobs": forbidden("cronjobs")}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"name": "web"})
	mustContain(t, out, "describe deployment web", "could not read: cronjobs (not allowed)")
	mustNotContain(t, outsideFence(out), errMarker)
}

func TestDescribeByNameWithNoFailureSaysNothingAboutReads(t *testing.T) {
	r, _ := newRunner(t, webFixture(t))
	out := describeText(t, r, map[string]any{"name": "nothing-like-it"})
	mustContain(t, out, "looked at deployments, statefulsets, daemonsets, jobs, cronjobs, services and pods")
	mustNotContain(t, out, "could not read")
}

// 1b: a failed read of the events is not "no events".
func TestDescribeSaysWhenTheEventsCouldNotBeRead(t *testing.T) {
	for err, reason := range map[error]string{
		forbidden("events"):      "not allowed",
		context.DeadlineExceeded: "timed out",
		errors.New(errMarker):    "the read failed",
	} {
		f := webFixture(t)
		f.eventsErr = err
		r, _ := newRunner(t, f)
		out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web"})
		mustContain(t, out, "recent events: could not be read ("+reason+")")
		mustNotContain(t, out, "recent events: none")
		mustNotContain(t, outsideFence(out), errMarker)
	}
	// No failure and no events is still "none".
	r, _ := newRunner(t, webFixture(t))
	mustContain(t, describeText(t, r, map[string]any{"kind": "deployment", "name": "web"}), "recent events: none")
}

// 1c: a failed pod list is said, and no cause that depends on the pods is given.
func TestDescribeWorkloadSaysWhenThePodsCouldNotBeRead(t *testing.T) {
	labels := map[string]any{"app": "web"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, brokenDeployment("shop", "web", labels),
		ownedRS("shop", "web-7f", "web", 1, 1, labels, nil),
		mkPod("shop", "web-7f-abc", "ReplicaSet", "web-7f", labels, "Crash"))
	f.objs = append(f.objs, quietObjects("shop")...)
	r, _ := newRunner(t, f)
	base := describeText(t, r, map[string]any{"kind": "deployment", "name": "web"})
	mustContain(t, base, "pod/web-7f-abc") // the pods give a cause when they can be read

	f.listErrs = map[string]error{"pods": forbidden("pods")}
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web"})
	mustContain(t, out, "pods: could not be read (not allowed)")
	mustNotContain(t, out, "pod/web-7f-abc", "pods (selector")
	mustNotContain(t, outsideFence(out), errMarker)
}

// 1d: a related read that failed is not "no problem".
func TestRelatedSaysWhichReadsFailed(t *testing.T) {
	labels := map[string]any{"app": "web"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, appDeployment("shop", "web", 1, 1, labels), ownedRS("shop", "web-7f", "web", 1, 1, labels, nil))
	f.objs = append(f.objs, quietObjects("shop")...)
	f.listErrs = map[string]error{"persistentvolumeclaims": forbidden("persistentvolumeclaims"), "poddisruptionbudgets": context.DeadlineExceeded}
	r, _ := newRunner(t, f)
	out := describeText(t, r, map[string]any{"kind": "deployment", "name": "web"})
	mustContain(t, out, "related, not read: persistentvolumeclaims (not allowed); poddisruptionbudgets (timed out)")
	mustNotContain(t, outsideFence(out), errMarker)

	f.listErrs = nil
	r, _ = newRunner(t, f)
	mustNotContain(t, describeText(t, r, map[string]any{"kind": "deployment", "name": "web"}), "related, not read")
}

// 1e: the three cases of the metrics API.
func TestDescribePodDistinguishesMetricsFailures(t *testing.T) {
	for err, reason := range map[error]string{
		kube.ErrNoMetrics:     "no metrics API",
		kube.ErrMetricsDown:   "no metrics API",
		forbidden("pods"):     "not allowed",
		errors.New(errMarker): "the read failed",
	} {
		f := &fakeReader{logs: map[string]string{}, metricsErr: err}
		f.objs = append(f.objs, mkPod("shop", "web-7f-abc", "ReplicaSet", "web-7f", map[string]any{"app": "web"}, "Running"))
		r, _ := newRunner(t, f)
		out := describeText(t, r, map[string]any{"kind": "pod", "name": "web-7f-abc"})
		mustContain(t, out, "resources: usage not available ("+reason+")")
		mustNotContain(t, outsideFence(out), errMarker)
	}
}

// 1f: a pod list that was cut is said in top.
func TestTopSaysWhenThePodListWasCut(t *testing.T) {
	f := &fakeReader{logs: map[string]string{}}
	labels := map[string]any{"app": "web"}
	for i := 0; i < topListCap+1; i++ {
		f.objs = append(f.objs, mkPod("shop", fmt.Sprintf("web-%05d", i), "ReplicaSet", "web-7f", labels, "Running"))
	}
	f.metrics = map[kube.MetricsTarget][]unstructured.Unstructured{
		kube.PodMetrics: {podMetric("shop", "web-00001", map[string][2]string{"app": {"10m", "20Mi"}})},
	}
	r, _ := newRunner(t, f)
	res := call(t, r, "top", map[string]any{"resource": "pods"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	mustContain(t, res.Text, fmt.Sprintf("the pod list was cut at %d; verdicts cover the pods read", topListCap))

	f.objs = f.objs[:3]
	res = call(t, r, "top", map[string]any{"resource": "pods"})
	mustNotContain(t, res.Text, "the pod list was cut")
}

// The text of an API error stays out of every path above, outside the fence.
func TestReadFailureTextNeverLeavesTheFence(t *testing.T) {
	labels := map[string]any{"app": "web"}
	f := &fakeReader{logs: map[string]string{}}
	f.objs = append(f.objs, appDeployment("shop", "web", 1, 1, labels), ownedRS("shop", "web-7f", "web", 1, 1, labels, nil),
		mkPod("shop", "web-7f-abc", "ReplicaSet", "web-7f", labels, "Running"))
	f.objs = append(f.objs, quietObjects("shop")...)
	bad := errors.New(errMarker)
	f.listErrs = map[string]error{"cronjobs": bad, "jobs": forbidden("jobs"), "pods": bad, "persistentvolumeclaims": bad, "services": forbidden("services")}
	f.eventsErr = bad
	f.metricsErr = forbidden("pods")
	r, _ := newRunner(t, f)
	for _, args := range []map[string]any{
		{"name": "web"}, {"name": "nothing-like-it"}, {"kind": "deployment", "name": "web"}, {"kind": "pod", "name": "web-7f-abc"},
	} {
		res := call(t, r, "describe", args)
		mustNotContain(t, outsideFence(res.Text), errMarker)
	}
}
