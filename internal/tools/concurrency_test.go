package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/kube/readonly"
)

// With every read taking the same time, the time of a call is the number of
// reads in its longest chain of dependent reads, not the number of reads.
// The reads run on the fake clock of testing/synctest, so the load of the
// machine does not change the time and the bounds have no slack.
func TestWallTimeIsTheLongestChain(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		args  map[string]any
		chain int
		why   string
	}{
		{
			"list pods in all namespaces", "list",
			map[string]any{"kind": "pods", "allNamespaces": true},
			2,
			"list, usage and events together; then the deeper causes of the 3 worst pods together (previous logs; nodes)",
		},
		{
			"list pods with nothing wrong", "list",
			map[string]any{"kind": "pods", "namespace": "pay", "labelSelector": "app=api"},
			1,
			"list, usage and events together",
		},
		{
			"describe deployment", "describe",
			map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"},
			4,
			"get; then events, pod list and usage together; then the events of the worst pod; then its previous logs",
		},
		{
			"describe pod that crashed", "describe",
			map[string]any{"kind": "pod", "name": "web-5d4f-aaaaa", "namespace": "shop"},
			3,
			"get; then events, usage and the owner chain (ReplicaSet, Deployment) together; then the previous logs",
		},
		{
			"describe pod that is pending", "describe",
			map[string]any{"kind": "pod", "name": "pend-1", "namespace": "pay"},
			3,
			"get; then events and usage together; then the nodes and all pods together",
		},
		{
			"describe service", "describe",
			map[string]any{"kind": "service", "name": "web", "namespace": "shop"},
			2,
			"get; then events, endpoints and the pods behind the selector together (the second endpoint read and the pod read for the cause are the same reads)",
		},
		{
			"describe by name, a deployment", "describe",
			map[string]any{"name": "web"},
			5,
			"the metadata of the six kinds together; then the get of each match (the deployment and the service) together; then the same chain as describe deployment, without its get",
		},
		{
			"describe by name, a pod", "describe",
			map[string]any{"name": "web-5d4f-aaaaa"},
			7,
			"the metadata of the six kinds; then the metadata of all pods; then the get of the pod; then ReplicaSet, then Deployment; then events of the Deployment, events of the worst pod and usage together, with the lists of the related objects (more than six reads: when some wait for a free slot, there is one more step); then the previous logs",
		},
		{"top pods", "top", map[string]any{"resource": "pods", "allNamespaces": true}, 1, "usage and pod list together"},
	}
	// slow is the longest chain when the reads of the related objects wait for
	// a free slot (the bound is maxParallelReads), for the cases where that
	// can happen.
	slow := map[string]int{"describe by name, a pod": 8}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// In the bubble the clock moves only when every goroutine waits, so
			// the time of a call is a whole number of reads, on any machine.
			synctest.Test(t, func(t *testing.T) { wallTimeCase(t, c.tool, c.args, c.chain, slow[c.name], c.why) })
		})
	}
}

// wallTimeCase runs one call with every read taking lat, and checks that it
// took the longest chain of reads, or slow (when set) at most.
func wallTimeCase(t *testing.T, tool string, args map[string]any, chain, slow int, why string) {
	const lat = 100 * time.Millisecond
	f := scenarioFixture(t)
	f.latency = lat
	r, _ := newRunner(t, f)
	start := time.Now()
	res := call(t, r, tool, args)
	got := time.Since(start)
	if res.IsError {
		t.Fatal(res.Text)
	}
	want := time.Duration(chain) * lat
	sum := time.Duration(f.total()) * lat
	t.Logf("%d reads (%v if one after the other), longest chain %d (%v): took %v. %s", f.total(), sum, chain, want, got, why)
	if got < want {
		t.Errorf("took %v, less than the chain of %d reads: the expected chain is out of date", got, chain)
	}
	longest := max(chain, slow)
	if got > time.Duration(longest)*lat {
		t.Errorf("took %v: more than the longest chain of %d reads (%v); the reads are not running together", got, longest, time.Duration(longest)*lat)
	}
	if f.maxInflight > maxParallelReads {
		t.Errorf("%d reads at once, more than %d", f.maxInflight, maxParallelReads)
	}
}

// The same cluster state gives the same text, whichever read finishes first.
func TestOutputDoesNotDependOnTheOrderOfTheReads(t *testing.T) {
	cases := []struct {
		name, tool string
		args       map[string]any
	}{
		{"list pods", "list", map[string]any{"kind": "pods", "allNamespaces": true}},
		{"list deployments", "list", map[string]any{"kind": "deployments", "allNamespaces": true}},
		{"list services", "list", map[string]any{"kind": "services", "allNamespaces": true}},
		{"describe deployment", "describe", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"}},
		{"describe pod", "describe", map[string]any{"kind": "pod", "name": "web-5d4f-aaaaa", "namespace": "shop"}},
		{"describe pending pod", "describe", map[string]any{"kind": "pod", "name": "pend-1", "namespace": "pay"}},
		{"describe service", "describe", map[string]any{"kind": "service", "name": "web", "namespace": "shop"}},
		{"describe by name", "describe", map[string]any{"name": "web"}},
		{"describe by selector", "describe", map[string]any{"kind": "pods", "namespace": "shop", "labelSelector": "app=web"}},
		{"top pods", "top", map[string]any{"resource": "pods", "allNamespaces": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quiet := scenarioFixture(t)
			rq, _ := newRunner(t, quiet)
			want := call(t, rq, c.tool, c.args)
			if want.IsError {
				t.Fatal(want.Text)
			}
			f := scenarioFixture(t)
			f.jitter = 6 * time.Millisecond
			r, _ := newRunner(t, f)
			for i := 0; i < 20; i++ {
				if got := call(t, r, c.tool, c.args); got != want {
					t.Fatalf("run %d differs:\n%s\n--- want\n%s", i, got.Text, want.Text)
				}
			}
		})
	}
}

func TestTimeoutStopsAllReads(t *testing.T) {
	for _, c := range []struct {
		name    string
		timeout time.Duration
		failing bool // the first read does not finish, so the call fails
	}{
		{"before the first read ends", 60 * time.Millisecond, true},
		// After the first read, the reads that fail are optional in describe:
		// the call returns what it has, as it did when the reads ran one
		// after the other. All its reads must still stop.
		{"between two reads", 150 * time.Millisecond, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := scenarioFixture(t)
			f.latency = 100 * time.Millisecond
			r, auditPath := newRunner(t, f)
			r.Timeout = c.timeout
			start := time.Now()
			res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
			if took := time.Since(start); took > c.timeout+100*time.Millisecond {
				t.Errorf("took %v with a timeout of %v", took, c.timeout)
			}
			if c.failing && (!res.IsError || !strings.Contains(res.Text, "did not answer in time")) {
				t.Errorf("got %q", res.Text)
			}
			n := f.total()
			time.Sleep(250 * time.Millisecond)
			if f.total() != n {
				t.Errorf("reads started after the call returned: %d, then %d", n, f.total())
			}
			f.mu.Lock()
			inflight := f.inflight
			f.mu.Unlock()
			if inflight != 0 {
				t.Errorf("%d reads still running after the call returned", inflight)
			}
			if lines := auditLines(t, auditPath); len(lines) != 1 {
				t.Errorf("%d audit lines, want 1: %v", len(lines), lines)
			}
		})
	}
}

func TestCancelledCallStopsAllReads(t *testing.T) {
	for _, after := range []time.Duration{60 * time.Millisecond, 150 * time.Millisecond} {
		f := scenarioFixture(t)
		f.latency = 100 * time.Millisecond
		r, auditPath := newRunner(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(after, cancel)
		start := time.Now()
		res := r.Call(ctx, "list", []byte(`{"kind":"pods","allNamespaces":true}`))
		if took := time.Since(start); took > after+100*time.Millisecond {
			t.Errorf("took %v after a cancel at %v", took, after)
		}
		if after < 100*time.Millisecond && !res.IsError {
			t.Errorf("cancelled before the list ended, but got %q", res.Text)
		}
		n := f.total()
		time.Sleep(250 * time.Millisecond)
		f.mu.Lock()
		inflight := f.inflight
		f.mu.Unlock()
		if f.total() != n || inflight != 0 {
			t.Errorf("reads after the call returned: %d then %d, %d running", n, f.total(), inflight)
		}
		if lines := auditLines(t, auditPath); len(lines) != 1 {
			t.Errorf("audit lines: %v", lines)
		}
	}
}

// A read that fails keeps the effect it had: the events of a list are
// optional, a missing metrics API is a note and not a failure.
func TestFailedParallelReadsKeepTheirMeaning(t *testing.T) {
	base := scenarioFixture(t)
	rb, _ := newRunner(t, base)
	withEvents := call(t, rb, "list", map[string]any{"kind": "pods", "allNamespaces": true})

	f := scenarioFixture(t)
	f.eventsErr = errors.New("events are not readable")
	r, _ := newRunner(t, f)
	res := call(t, r, "list", map[string]any{"kind": "pods", "allNamespaces": true})
	if res.IsError || !strings.Contains(res.Text, "img-1") || !strings.Contains(res.Text, "pend-1") {
		t.Errorf("a list without events must still list the pods:\n%s", res.Text)
	}
	if res == withEvents {
		t.Errorf("the events changed nothing, so the test does not test them")
	}

	f = scenarioFixture(t)
	f.metricsErr = kube.ErrNoMetrics
	r, _ = newRunner(t, f)
	for _, args := range []map[string]any{
		{"kind": "pods", "allNamespaces": true},
	} {
		if res := call(t, r, "list", args); res.IsError {
			t.Errorf("list with no metrics API failed: %s", res.Text)
		}
	}
	res = call(t, r, "describe", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	if res.IsError || !strings.Contains(res.Text, "web-5d4f-aaaaa") {
		t.Errorf("describe with no metrics API failed:\n%s", res.Text)
	}
	res = call(t, r, "top", map[string]any{"resource": "pods", "allNamespaces": true})
	if res.IsError || !strings.Contains(res.Text, "usage: not available") {
		t.Errorf("top with no metrics API must say so, not fail:\n%s", res.Text)
	}
	if f.counts()["list"] == 0 {
		t.Errorf("no list read")
	}
}

// A request that the read-only transport blocks is counted in the audit line
// of the call, also when it was made by a read that runs in the background,
// and the audit line is written once.
func TestBlockedRequestsAreCountedOnceInTheAuditLine(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	target, err := readonly.TargetFor(u)
	if err != nil {
		t.Fatal(err)
	}
	rt := readonly.Wrapper(target)(http.DefaultTransport)
	f := scenarioFixture(t)
	f.eventsHook = func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/namespaces/shop/pods/x/proxy", nil)
		_, err := rt.RoundTrip(req)
		return err // blocked: ErrBlocked
	}
	r, auditPath := newRunner(t, f)
	res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	lines := auditLines(t, auditPath)
	if len(lines) != 1 {
		t.Fatalf("%d audit lines", len(lines))
	}
	var e struct {
		Blocked int64 `json:"blocked_requests"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if want := int64(f.counts()["events"]); e.Blocked != want || want == 0 {
		t.Errorf("blocked_requests %d, want %d (one for each events read)", e.Blocked, want)
	}
}

// --- the per-call reader ---

func podKind(t *testing.T) kinds.Kind {
	t.Helper()
	k, err := kinds.Default().Lookup("pods")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCallReaderBoundsTheReadsInFlight(t *testing.T) {
	f := scenarioFixture(t)
	f.latency = 20 * time.Millisecond
	c := newCallReader(f)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Events(context.Background(), "shop", &kube.ObjectRef{Kind: "Pod", Name: string(rune('a' + i))}, 10)
		}()
	}
	wg.Wait()
	if f.total() != 30 {
		t.Errorf("%d reads, want 30 (each is different)", f.total())
	}
	if f.maxInflight > maxParallelReads || f.maxInflight < 2 {
		t.Errorf("%d reads at once, want 2 to %d", f.maxInflight, maxParallelReads)
	}
}

func TestCallReaderReadsTheSameThingOnce(t *testing.T) {
	f := scenarioFixture(t)
	f.latency = 30 * time.Millisecond
	c := newCallReader(f)
	ctx := context.Background()
	pk := podKind(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := c.List(ctx, pk, "shop", "app=web", 100)
			if err != nil || len(p.Items) != 2 {
				t.Errorf("list: %d items, %v", len(p.Items), err)
			}
			_, _ = c.Get(ctx, pk, "shop", "web-5d4f-aaaaa")
			_, _ = c.Events(ctx, "shop", &kube.ObjectRef{UID: "uid-web-5d4f-aaaaa"}, 200)
			_, _ = c.Metrics(ctx, kube.PodMetrics, "shop", "app=web")
		}()
	}
	wg.Wait()
	if got := fmtCounts(f.counts()); got != "events:1 get:1 list:1 metrics:1" {
		t.Errorf("reads: %s", got)
	}
}

func TestCallReaderDoesNotMemoizeLogs(t *testing.T) {
	f := scenarioFixture(t)
	c := newCallReader(f)
	for i := 0; i < 2; i++ {
		_, _ = c.Logs(context.Background(), "shop", "web-5d4f-aaaaa", kube.LogOptions{Container: "app", Previous: true})
	}
	if f.counts()["logs"] != 2 {
		t.Errorf("%d log reads, want 2 (a log read can be repeated on purpose)", f.counts()["logs"])
	}
}

// A list that returned everything answers the narrower lists of the call: a
// namespace, a label selector, a larger cap. It does not answer a list it
// cannot answer exactly.
func TestCallReaderAnswersNarrowerListsFromAFullList(t *testing.T) {
	f := scenarioFixture(t)
	c := newCallReader(f)
	ctx := context.Background()
	pk := podKind(t)
	all, err := c.List(ctx, pk, "", "", 2000)
	if err != nil || all.More {
		t.Fatal(err)
	}
	f.reset()
	got, err := c.List(ctx, pk, "shop", "app=web", 200)
	if err != nil || len(got.Items) != 2 {
		t.Fatalf("%d items, %v", len(got.Items), err)
	}
	if got.Remaining != -1 || got.More {
		t.Errorf("page %+v", got)
	}
	if again, _ := c.List(ctx, pk, "", "", 5000); len(again.Items) != len(all.Items) {
		t.Errorf("a larger cap: %d items, want %d", len(again.Items), len(all.Items))
	}
	if _, err := c.List(ctx, pk, "", "not a selector !!", 200); err == nil {
		t.Errorf("a bad selector must still fail")
	}
	if f.total() != 1 { // only the bad selector reached the reader
		t.Errorf("reads: %v", f.calls)
	}
	// A cap smaller than the answer is a real read.
	f.reset()
	small, _ := c.List(ctx, pk, "pay", "", 3)
	if !small.More || len(small.Items) != 3 || f.total() != 1 {
		t.Errorf("cap of 3: %d items, more=%v, %d reads", len(small.Items), small.More, f.total())
	}
	// The items of an answer are the caller's: sorting them does not change the next answer.
	a, _ := c.List(ctx, pk, "shop", "app=web", 200)
	a.Items[0], a.Items[1] = a.Items[1], a.Items[0]
	b, _ := c.List(ctx, pk, "shop", "app=web", 200)
	if b.Items[0].GetName() != "web-5d4f-aaaaa" {
		t.Errorf("an answer changed after a caller sorted its copy: %s first", b.Items[0].GetName())
	}
}

// A read that was cancelled on purpose is not kept: the same read, asked
// again with a live context, is made.
func TestCallReaderDoesNotKeepACancelledRead(t *testing.T) {
	f := scenarioFixture(t)
	f.latency = 50 * time.Millisecond
	c := newCallReader(f)
	pk := podKind(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	if _, err := c.List(ctx, pk, "shop", "", 10); err == nil {
		t.Fatal("the cancelled read returned no error")
	}
	p, err := c.List(context.Background(), pk, "shop", "", 10)
	if err != nil || len(p.Items) == 0 {
		t.Errorf("the second read: %d items, %v", len(p.Items), err)
	}
}

func TestFutureAndParallelRaiseAPanicInTheCaller(t *testing.T) {
	recovered := func(fn func()) (p any) {
		defer func() { p = recover() }()
		fn()
		return nil
	}
	if p := recovered(func() { parallel(func() {}, func() { panic("boom") }) }); p != "boom" {
		t.Errorf("parallel: %v", p)
	}
	if p := recovered(func() {
		f := start(context.Background(), func(context.Context) int { panic("bang") })
		f.wait()
	}); p != "bang" {
		t.Errorf("future: %v", p)
	}
	// A panic in a read that runs in the background is an internal error of the call, not a crash.
	f := scenarioFixture(t)
	f.eventsHook = func(context.Context) error { panic("secret value in a panic") }
	r, _ := newRunner(t, f)
	res := call(t, r, "describe", map[string]any{"kind": "deployment", "name": "web", "namespace": "shop"})
	if !res.IsError || strings.Contains(res.Text, "secret value") {
		t.Errorf("got %q", res.Text)
	}
}
