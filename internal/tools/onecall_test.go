package tools

import (
	"strings"
	"testing"
)

// One call should be enough: these tests come from an agent benchmark in
// which agents needed list, then describe, then logs, then get.

// Every row of a list holds the cause of its object on the same line, so
// that grep, head and tail keep it. The worst rows have the deeper cause,
// which quotes the last error of the crashed container.
func TestListRowsHoldTheirCause(t *testing.T) {
	f := diagFixture(t)
	r, _ := newRunner(t, f)
	out := call(t, r, "list", map[string]any{"kind": "pods", "namespace": "pay"}).Text
	mustContain(t, lineWith(t, out, "crash-1"), "CrashLoopBackOff", "container app exits with code 1 (application error), 7 restarts; last error: FATAL config: DATABASE_URL is not set")
	mustContain(t, lineWith(t, out, "img-1"), "ImagePullBackOff", "container pay cannot pull its image: the image or tag registry.example/pay:1.4.2 does not exist in the registry")
	mustContain(t, lineWith(t, out, "job-1"), "container report is OOM killed at its memory limit of 1024Mi")
	mustContain(t, lineWith(t, out, "noconfig-1"), `the configmap "notifier-settings", which does not exist in namespace pay`)
	mustContain(t, lineWith(t, out, "probe-1"), "readiness probe fails (×42")
	mustContain(t, lineWith(t, out, "vol-1"), "Multi-Attach error")
	mustNotContain(t, out, "likely causes", "PROBLEM", "\n- pod/", "crashSecret9")
	// The size of a cause on a row.
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, "  container "); i > 0 && len(l[i+2:]) > deepCauseMax+10 {
			t.Errorf("cause longer than %d bytes: %q", deepCauseMax, l)
		}
	}
	// Only the worst three read logs; the others were diagnosed from the pod,
	// its events and its usage.
	logReads := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "logs ") {
			logReads++
		}
	}
	if logReads > 6 {
		t.Errorf("%d log reads for one list: %v", logReads, f.calls)
	}
	// A healthy list has no cause on any row.
	ok := call(t, r, "list", map[string]any{"kind": "namespaces"}).Text
	mustNotContain(t, ok, "likely causes", "exits with code")
}

func TestListWorkloadsShowsThePodCause(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "deployments", "namespace": "pay"}).Text
	mustContain(t, lineWith(t, out, "ledger"), "pod/crash-1: container app exits with code 1 (application error), 7 restarts; last error: FATAL config: DATABASE_URL is not set")
	mustNotContain(t, out, "likely causes")
}

// describe on a workload shows the last crash of its worst pod.
func TestDescribeWorkloadShowsLastCrash(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "deployment", "ledger", "pay")
	mustContain(t, out,
		"- pod/crash-1: container app exits with code 1 (application error), 7 restarts. Last error before the exit: FATAL config: DATABASE_URL is not set",
		"last crash of container app (previous instance of pod/crash-1):",
		"FATAL config: DATABASE_URL is not set",
	)
}

// The summary of a pod that has a problem shows the command and the
// environment with its values (redacted). A reference shows its source, never
// a value.
func TestSummaryShowsCommandAndEnv(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "crash-1", "pay")
	mustContain(t, out,
		"containers: app (registry.example/ledger:0.9)",
		"- app: command: sh -c run-ledger --mode=prod",
		"  env: LOG_LEVEL=info, DB_PASSWORD (secret ledger-db/password), API_TOKEN=[REDACTED:key-name]",
	)
	mustNotContain(t, out, "summarySecret1")
}

// A missing ConfigMap: name the ConfigMaps that do exist.
func TestMissingConfigMapNamesTheNeighbors(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := describe(t, r, "pod", "noconfig-1", "pay")
	mustContain(t, out, `container notifier cannot start: it refers to the configmap "notifier-settings", which does not exist in namespace pay. ConfigMaps in the namespace: notifier-config.`)
}

// list services shows which service has no ready endpoints, and why.
func TestListServicesShowsHealthAndCause(t *testing.T) {
	r, _ := newRunner(t, diagFixture(t))
	out := call(t, r, "list", map[string]any{"kind": "services", "namespace": "pay"}).Text
	mustContain(t, out, "2 services: 2 unhealthy")
	mustContain(t, lineWith(t, out, "checkout"), "selector app=checkout matches 0 pods in namespace pay; pods with other values for the same label: app=checkout-v2 (2 pods)")
	mustContain(t, lineWith(t, out, "web"), "selector app=web matches 1 pod, but none is ready: pod/probe-1: container web: running but not ready")
	mustNotContain(t, out, "likely causes")
	ok := call(t, r, "list", map[string]any{"kind": "svc"}).Text
	mustContain(t, ok, "1 service: 1 healthy")
	// Agents ask for "endpoints"; the answer is the EndpointSlices.
	ep := call(t, r, "list", map[string]any{"kind": "endpoints", "namespace": "pay"})
	if ep.IsError || !strings.Contains(ep.Text, "list endpointslices") {
		t.Errorf("endpoints alias: %s", ep.Text)
	}
}
