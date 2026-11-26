package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/groundcover-com/kray/internal/audit"
	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
)

// fakeReader serves objects from memory and records every call. It is safe
// for the concurrent reads of one tool call. With latency set, each read
// takes that long (and stops early when its context ends), so that a test can
// tell reads that run together from reads that run one after the other.
type fakeReader struct {
	objs       []unstructured.Unstructured
	logs       map[string]string // ns/pod/container -> text
	metrics    map[kube.MetricsTarget][]unstructured.Unstructured
	metricsErr error
	latency    time.Duration
	jitter     time.Duration                   // each read also waits a random time up to this, so that reads finish in changing orders
	eventsErr  error                           // when set, every events read fails with it
	listErrs   map[string]error                // plural kind name -> error: every list of that kind fails with it
	eventsHook func(ctx context.Context) error // when set, every events read runs it first; an error fails the read

	mu          sync.Mutex
	calls       []string // one line for each read: "get ...", "list ...", "listmeta ...", "events ...", "logs ...", "metrics ..."
	inflight    int
	maxInflight int
}

// begin records a read and, when latency is set, waits for it. The returned
// function ends the read. It returns an error when ctx ends first.
func (f *fakeReader) begin(ctx context.Context, call string) (func(), error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.inflight++
	f.maxInflight = max(f.maxInflight, f.inflight)
	f.mu.Unlock()
	end := func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}
	delay := f.latency
	if f.jitter > 0 {
		delay += time.Duration(rand.Int64N(int64(f.jitter)))
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			end()
			return nil, ctx.Err()
		}
	}
	return end, nil
}

// counts is the number of reads by method: get, list, listmeta, events, logs,
// metrics.
func (f *fakeReader) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, c := range f.calls {
		m, _, _ := strings.Cut(c, " ")
		out[m]++
	}
	return out
}

// total is the number of reads.
func (f *fakeReader) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// reset forgets the recorded reads.
func (f *fakeReader) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.maxInflight = nil, 0
}

func (f *fakeReader) match(k kinds.Kind, u unstructured.Unstructured) bool {
	gv, _ := schema.ParseGroupVersion(u.GetAPIVersion())
	return u.GetKind() == k.Kind() && gv.Group == k.Group()
}

func (f *fakeReader) Get(ctx context.Context, k kinds.Kind, ns, name string) (*unstructured.Unstructured, error) {
	end, err := f.begin(ctx, fmt.Sprintf("get %s %s/%s", k.Name(), ns, name))
	if err != nil {
		return nil, err
	}
	defer end()
	for i := range f.objs {
		u := f.objs[i]
		if f.match(k, u) && u.GetName() == name && (!k.Namespaced() || u.GetNamespace() == ns) {
			return u.DeepCopy(), nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: k.Group(), Resource: k.Resource()}, name)
}

func (f *fakeReader) List(ctx context.Context, k kinds.Kind, ns, sel string, max int) (kube.Page, error) {
	end, err := f.begin(ctx, fmt.Sprintf("list %s %s %s", k.Name(), ns, sel))
	if err != nil {
		return kube.Page{}, err
	}
	defer end()
	if e := f.listErrs[k.Name()]; e != nil {
		return kube.Page{}, e
	}
	s, err := labels.Parse(sel)
	if err != nil {
		return kube.Page{}, err
	}
	p := kube.Page{Remaining: -1}
	for _, u := range f.objs {
		if f.match(k, u) && (ns == "" || u.GetNamespace() == ns) && s.Matches(labels.Set(u.GetLabels())) {
			if len(p.Items) == max {
				p.More = true
				break
			}
			p.Items = append(p.Items, *u.DeepCopy())
		}
	}
	return p, nil
}

// ListMeta serves the metadata of the objects only, as the API server does
// for a metadata list: no spec, no status, no data.
func (f *fakeReader) ListMeta(ctx context.Context, k kinds.Kind, ns string, max int) (kube.MetaPage, error) {
	end, err := f.begin(ctx, fmt.Sprintf("listmeta %s %s", k.Name(), ns))
	if err != nil {
		return kube.MetaPage{}, err
	}
	defer end()
	if e := f.listErrs[k.Name()]; e != nil {
		return kube.MetaPage{}, e
	}
	p := kube.MetaPage{Remaining: -1}
	for i := range f.objs {
		u := &f.objs[i]
		if !f.match(k, *u) || (ns != "" && u.GetNamespace() != ns) {
			continue
		}
		if len(p.Items) == max {
			p.More = true
			break
		}
		p.Items = append(p.Items, metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"},
			ObjectMeta: metav1.ObjectMeta{
				Name: u.GetName(), Namespace: u.GetNamespace(), UID: u.GetUID(),
				Labels: u.GetLabels(), Annotations: u.GetAnnotations(),
				OwnerReferences: u.GetOwnerReferences(), CreationTimestamp: u.GetCreationTimestamp(),
			},
		})
	}
	return p, nil
}

func (f *fakeReader) Events(ctx context.Context, ns string, about *kube.ObjectRef, max int) (kube.Page, error) {
	end, err := f.begin(ctx, "events "+ns)
	if err != nil {
		return kube.Page{}, err
	}
	defer end()
	if f.eventsHook != nil {
		if err := f.eventsHook(ctx); err != nil {
			return kube.Page{}, err
		}
	}
	if f.eventsErr != nil {
		return kube.Page{}, f.eventsErr
	}
	p := kube.Page{Remaining: -1}
	for _, u := range f.objs {
		if u.GetKind() != "Event" || (ns != "" && u.GetNamespace() != ns) {
			continue
		}
		if about != nil {
			uid, _, _ := unstructured.NestedString(u.Object, "involvedObject", "uid")
			kind, _, _ := unstructured.NestedString(u.Object, "involvedObject", "kind")
			name, _, _ := unstructured.NestedString(u.Object, "involvedObject", "name")
			if about.UID != "" && uid != about.UID || about.UID == "" && (kind != about.Kind || name != about.Name) {
				continue
			}
		}
		p.Items = append(p.Items, *u.DeepCopy())
	}
	return p, nil
}

func (f *fakeReader) Logs(ctx context.Context, ns, pod string, o kube.LogOptions) (string, error) {
	end, err := f.begin(ctx, fmt.Sprintf("logs %s/%s %s previous=%v", ns, pod, o.Container, o.Previous))
	if err != nil {
		return "", err
	}
	defer end()
	key := ns + "/" + pod + "/" + o.Container
	if o.Previous {
		if l, ok := f.logs[key+"#previous"]; ok {
			return l, nil
		}
	}
	return f.logs[key], nil
}

func (f *fakeReader) Metrics(ctx context.Context, t kube.MetricsTarget, ns, sel string) ([]unstructured.Unstructured, error) {
	end, err := f.begin(ctx, fmt.Sprintf("metrics %s %s %s", t, ns, sel))
	if err != nil {
		return nil, err
	}
	defer end()
	if f.metricsErr != nil {
		return nil, f.metricsErr
	}
	var out []unstructured.Unstructured
	for _, m := range f.metrics[t] {
		if ns == "" || m.GetNamespace() == ns {
			out = append(out, m)
		}
	}
	return out, nil
}

func loadObjects(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cluster.json"))
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	out := make([]unstructured.Unstructured, len(items))
	for i, m := range items {
		out[i] = unstructured.Unstructured{Object: m}
	}
	return out
}

var fixedNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func newRunner(t *testing.T, f *fakeReader) (*Runner, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := audit.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return &Runner{
		Reader: f, Kinds: kinds.Default(), Context: "kind-test", Namespace: "shop",
		Budget: 16 << 10, FullBudget: 256 << 10, Audit: l, Surface: "mcp", Session: "s1",
		Timeout: 5 * time.Second, Now: func() time.Time { return fixedNow }, Nonce: func() string { return "0123456789abcdef" },
	}, p
}

func call(t *testing.T, r *Runner, tool string, args any) Result {
	t.Helper()
	b, _ := json.Marshal(args)
	return r.Call(context.Background(), tool, b)
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output does not contain %q:\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, bads ...string) {
	t.Helper()
	for _, b := range bads {
		if strings.Contains(got, b) {
			t.Errorf("output contains %q:\n%s", b, got)
		}
	}
}

// lineWith returns the one output line that holds s. A row of a list must
// hold its cause on that same line: that is what a text filter keeps.
func lineWith(t *testing.T, out, s string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	t.Fatalf("no line holds %q:\n%s", s, out)
	return ""
}
