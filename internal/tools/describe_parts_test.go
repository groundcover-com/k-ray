package tools

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/shape"
)

func describeTestTarget(t *testing.T, kind, ns, name string, pods int) target {
	t.Helper()
	k, err := kinds.Default().Lookup(kind)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetKind(k.Kind())
	u.SetNamespace(ns)
	u.SetName(name)
	return target{k: k, u: u, pods: pods}
}

func TestDescribeCrashHeading(t *testing.T) {
	cases := []struct{ container, pod, note, want string }{
		{"api", "", "", "last crash of container api (previous instance):"},
		{"api", "web-1", "", "last crash of container api (previous instance of pod/web-1):"},
		{"api", "web-1", "custom heading:", "custom heading:"},
	}
	for _, c := range cases {
		if got := describeCrashHeading(c.container, c.pod, c.note); got != c.want {
			t.Errorf("describeCrashHeading(%q, %q, %q) = %q, want %q", c.container, c.pod, c.note, got, c.want)
		}
	}
}

func TestDescribeCauseSentences(t *testing.T) {
	if got := describeWorstPodCause("web-1", "container api crashed"); got != "pod/web-1: container api crashed" {
		t.Errorf("describeWorstPodCause = %q", got)
	}
	if got := describeFailedLogHeading("job", "batch-x"); got != "last log lines of container job in the failed pod/batch-x:" {
		t.Errorf("describeFailedLogHeading = %q", got)
	}
	if got := describeNotReadyLogHeading("api"); got != "recent log of container api (running, not ready):" {
		t.Errorf("describeNotReadyLogHeading = %q", got)
	}
	if got := describeStateLine(shape.Unhealthy); got != "state: unhealthy" {
		t.Errorf("describeStateLine = %q", got)
	}
}

func TestDescribeNoEventsLine(t *testing.T) {
	if got := describeNoEventsLine(nil); got != "recent events: none" {
		t.Errorf("no error: %q", got)
	}
	if got := describeNoEventsLine(errors.New("boom")); !strings.HasPrefix(got, "recent events: could not be read (") {
		t.Errorf("a failed read must not read as no events: %q", got)
	}
}

func TestDescribeMatchesSummaryAndClosing(t *testing.T) {
	if got := describeMatchesSummary(3, "web", 1, 3, 0); got != "found by name: 3 objects match web; all are described below (problems first)" {
		t.Errorf("all shown: %q", got)
	}
	if got := describeMatchesSummary(6, "web", 2, 4, 2); got != "found by name: 6 objects match web in 2 namespaces; 4 are described below, and the other 2 get one line each (problems first)" {
		t.Errorf("some shown: %q", got)
	}
	if got := describeMatchesClosing(2); !strings.Contains(got, "the namespace decides") {
		t.Errorf("two namespaces: %q", got)
	}
	if got := describeMatchesClosing(1); !strings.Contains(got, "the kind decides") {
		t.Errorf("one namespace: %q", got)
	}
}

func TestDescribeKeepExact(t *testing.T) {
	a := describeFoundObj{exact: false}
	b := describeFoundObj{exact: true}
	if got := describeKeepExact([]describeFoundObj{a, b, a}); len(got) != 1 || !got[0].exact {
		t.Errorf("exact matches must win: %+v", got)
	}
	if got := describeKeepExact([]describeFoundObj{a, a}); len(got) != 2 {
		t.Errorf("prefix matches must stay when none is exact: %+v", got)
	}
}

func TestDescribeBestTarget(t *testing.T) {
	dep := describeTestTarget(t, "deployments", "shop", "web", 0)
	dep2 := describeTestTarget(t, "statefulsets", "shop", "web", 0)
	svc := describeTestTarget(t, "services", "shop", "web", 0)
	svc2 := describeTestTarget(t, "services", "other", "web", 0)

	if got := describeBestTarget([]target{dep}, []target{svc}); got == nil || got.key() != dep.key() {
		t.Errorf("one workload and its service: want the workload, got %v", got)
	}
	if got := describeBestTarget(nil, []target{svc}); got == nil || got.key() != svc.key() {
		t.Errorf("one service alone: want the service, got %v", got)
	}
	if got := describeBestTarget([]target{dep, dep2}, nil); got != nil {
		t.Errorf("two workloads are ambiguous, got %v", got.key())
	}
	if got := describeBestTarget(nil, []target{svc, svc2}); got != nil {
		t.Errorf("two services are ambiguous, got %v", got.key())
	}
}

func TestDescribeSplitAndSortTargets(t *testing.T) {
	ts := []target{
		describeTestTarget(t, "services", "shop", "web", 0),
		describeTestTarget(t, "deployments", "shop", "web", 2),
		describeTestTarget(t, "deployments", "dev", "web", 0),
	}
	primary, services := describeSplitServices(ts)
	if len(primary) != 2 || len(services) != 1 {
		t.Fatalf("primary %d, services %d", len(primary), len(services))
	}
	describeSortTargets(ts)
	var got []string
	for _, x := range ts {
		got = append(got, x.key())
	}
	want := "Deployment/dev/web Deployment/shop/web Service/shop/web"
	if strings.Join(got, " ") != want {
		t.Errorf("sort order = %v, want %s", got, want)
	}
}

func TestDescribeFoundSummary(t *testing.T) {
	if got := describeFoundSummary(describeTestTarget(t, "deployments", "shop", "web", 2)); got != "found by name: deployment/web · namespace shop (the workload of 2 pods)" {
		t.Errorf("workload of pods: %q", got)
	}
	if got := describeFoundSummary(describeTestTarget(t, "services", "shop", "web", 0)); got != "found by name: service/web · namespace shop" {
		t.Errorf("object itself: %q", got)
	}
}

func TestDescribeFoundNamespace(t *testing.T) {
	if ns, err := describeFoundNamespace(DescribeInput{Name: "web", Namespace: "shop"}); err != nil || ns != "shop" {
		t.Errorf("namespace: %q, %v", ns, err)
	}
	if ns, err := describeFoundNamespace(DescribeInput{Name: "web", Namespace: "shop", AllNamespaces: true}); err != nil || ns != "" {
		t.Errorf("allNamespaces wins: %q, %v", ns, err)
	}
	if _, err := describeFoundNamespace(DescribeInput{Name: "web", Namespace: "Bad Namespace"}); err == nil {
		t.Error("a bad namespace must be refused")
	}
	if _, err := describeFoundNamespace(DescribeInput{Name: "../web"}); err == nil {
		t.Error("a bad name must be refused")
	}
}

func TestDescribeMatchRankAndStateOf(t *testing.T) {
	order := []shape.State{shape.Unhealthy, shape.Warning, shape.Healthy, shape.NoHealth}
	for i := 1; i < len(order); i++ {
		if describeMatchRank(order[i-1]) >= describeMatchRank(order[i]) {
			t.Errorf("%s must rank before %s", order[i-1], order[i])
		}
	}
	if got := describeStateOf(output{summary: []string{"x", "state: warning"}}); got != "warning" {
		t.Errorf("describeStateOf = %q", got)
	}
	if got := describeStateOf(output{}); got != "-" {
		t.Errorf("describeStateOf with no state line = %q", got)
	}
}

func TestDescribeWriteCrash(t *testing.T) {
	var b strings.Builder
	describeWriteCrash(&b, &describeParts{
		crash: []string{"panic: x"}, crashOf: "api", crashPod: "web-1",
		logLines: []string{"waiting"}, logOf: "side",
	})
	want := "last crash of container api (previous instance of pod/web-1):\n  panic: x\n" +
		"recent log of container side (running, not ready):\n  waiting\n"
	if b.String() != want {
		t.Errorf("describeWriteCrash =\n%s\nwant\n%s", b.String(), want)
	}
}
