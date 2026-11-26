package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
)

// maxParallelReads is how many reads of one tool call are in flight at once.
// The client-go rate limiter (10 requests per second, burst 20) still applies to all of them, so this
// bound limits the burst that one call makes, not the rate.
const maxParallelReads = 6

// maxLowReads is how many of those slots the reads of lower priority may hold
// at once. The reads that look at the objects around the described object
// (related.go) are of lower priority: the diagnosis of the object itself is
// a chain of dependent reads, and it must not wait behind a list of policies.
const maxLowReads = maxParallelReads / 2

type lowKey struct{}

// lowPriority marks the reads made with the returned context as lower
// priority. They still go through the same bound and the same memo.
func lowPriority(ctx context.Context) context.Context { return context.WithValue(ctx, lowKey{}, true) }

// callReader is the kube.Reader that the tools of one call see. It wraps the
// real reader and adds two things, for the length of that call only:
//
//   - a bound on the reads in flight (maxParallelReads), so that tools can run
//     independent reads at the same time without opening a flood of requests;
//   - a memo, so that the same read is made once: an identical read that is
//     asked again (also at the same moment) gets the first answer, and a list
//     that a read already returned in full answers the narrower lists of the
//     same call.
//
// The memo lives in the call and dies with it. Nothing is kept across calls
// and nothing is written to disk. Errors that come from a cancelled context
// are not kept, so that a read that was cancelled on purpose does not poison
// the same read later. Log reads are not memoized: a log read can be repeated
// on purpose (a container between two instances).
type callReader struct {
	inner kube.Reader
	sem   chan struct{}
	low   chan struct{} // held, besides a slot, by a read of lower priority

	mu    sync.Mutex
	memo  map[string]*readEntry
	lists []doneList // lists that were read in full, in the order they finished
}

// doneList is a list that returned every object in its scope.
type doneList struct {
	kind, ns, sel string
	items         []unstructured.Unstructured
}

func newCallReader(inner kube.Reader) *callReader {
	return &callReader{inner: inner, sem: make(chan struct{}, maxParallelReads), low: make(chan struct{}, maxLowReads), memo: map[string]*readEntry{}}
}

// readEntry is one read: in flight until done is closed.
type readEntry struct {
	done chan struct{}
	err  error

	obj   *unstructured.Unstructured
	page  kube.Page
	meta  kube.MetaPage
	items []unstructured.Unstructured // metrics

	// For a list in flight: what it reads. Set when the entry is made, under
	// the lock of the reader.
	list *listScope
}

type listScope struct{ kind, ns, sel string }

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// slot waits for a free read slot. It returns false when ctx ends first.
func (c *callReader) slot(ctx context.Context) bool {
	if ctx.Value(lowKey{}) != nil {
		select {
		case c.low <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		select {
		case c.sem <- struct{}{}:
			return true
		case <-ctx.Done():
			<-c.low
			return false
		}
	}
	select {
	case c.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release frees the slot that slot gave to a read made with the same ctx.
func (c *callReader) release(ctx context.Context) {
	<-c.sem
	if ctx.Value(lowKey{}) != nil {
		<-c.low
	}
}

// do runs fill once for key. Callers of the same key wait for that run.
func (c *callReader) do(ctx context.Context, key string, scope *listScope, fill func(e *readEntry)) *readEntry {
	for {
		c.mu.Lock()
		if e, ok := c.memo[key]; ok {
			c.mu.Unlock()
			select {
			case <-e.done:
			case <-ctx.Done():
				return &readEntry{err: ctx.Err()}
			}
			if isCtxErr(e.err) && ctx.Err() == nil {
				continue // the first caller was cancelled, this one was not: read again
			}
			return e
		}
		e := &readEntry{done: make(chan struct{}), list: scope}
		c.memo[key] = e
		c.mu.Unlock()
		c.run(ctx, key, e, fill)
		return e
	}
}

func (c *callReader) run(ctx context.Context, key string, e *readEntry, fill func(e *readEntry)) {
	defer func() {
		p := recover()
		if p != nil {
			e.err = errInternal
		}
		if e.err != nil && isCtxErr(e.err) {
			c.mu.Lock()
			if c.memo[key] == e {
				delete(c.memo, key)
			}
			c.mu.Unlock()
		}
		close(e.done)
		if p != nil {
			panic(p)
		}
	}()
	if !c.slot(ctx) {
		e.err = ctx.Err()
		return
	}
	defer c.release(ctx)
	fill(e)
}

func kindKey(k kinds.Kind) string { return k.GVR().String() }

func cloneItems(in []unstructured.Unstructured) []unstructured.Unstructured {
	if in == nil {
		return nil
	}
	return append([]unstructured.Unstructured(nil), in...)
}

// Get reads one object.
func (c *callReader) Get(ctx context.Context, k kinds.Kind, ns, name string) (*unstructured.Unstructured, error) {
	key := fmt.Sprintf("get|%s|%s|%s", kindKey(k), ns, name)
	e := c.do(ctx, key, nil, func(e *readEntry) {
		e.obj, e.err = c.inner.Get(ctx, k, ns, name)
	})
	if e.err != nil {
		return nil, e.err
	}
	if e.obj == nil {
		return nil, nil
	}
	return e.obj.DeepCopy(), nil
}

// List reads objects. A list that an earlier read of this call returned in
// full answers it without a request.
func (c *callReader) List(ctx context.Context, k kinds.Kind, ns, sel string, max int) (kube.Page, error) {
	if p, ok := c.fromEarlierList(k, ns, sel, max); ok {
		return p, nil
	}
	key := fmt.Sprintf("list|%s|%s|%s|%d", kindKey(k), ns, sel, max)
	// The same list with another cap may be in flight (for example the pods
	// behind a selector, with a cap of 50 and with a cap of 200). Wait for it:
	// it answers this one when it returns everything. Which of two reads at the
	// same moment ends first must not decide how many reads a call makes.
	c.waitForSameList(ctx, key, k, ns, sel)
	if p, ok := c.fromEarlierList(k, ns, sel, max); ok { // also when it ended while we looked
		return p, nil
	}
	e := c.do(ctx, key, &listScope{kindKey(k), ns, sel}, func(e *readEntry) {
		e.page, e.err = c.inner.List(ctx, k, ns, sel, max)
		// Keep a copy: a caller may sort the items it gets in place.
		e.page.Items = cloneItems(e.page.Items)
		if e.err == nil && !e.page.More {
			c.mu.Lock()
			c.lists = append(c.lists, doneList{kindKey(k), ns, sel, e.page.Items})
			c.mu.Unlock()
		}
	})
	if e.err != nil {
		return kube.Page{}, e.err
	}
	p := e.page
	p.Items = cloneItems(p.Items)
	return p, nil
}

// waitForSameList waits for a list of the same kind, namespace and selector
// that is in flight with another cap.
func (c *callReader) waitForSameList(ctx context.Context, ownKey string, k kinds.Kind, ns, sel string) {
	want := listScope{kindKey(k), ns, sel}
	c.mu.Lock()
	var wait []*readEntry
	for key, e := range c.memo {
		if key == ownKey || e.list == nil || *e.list != want {
			continue
		}
		select {
		case <-e.done:
		default:
			wait = append(wait, e)
		}
	}
	c.mu.Unlock()
	for _, e := range wait {
		select {
		case <-e.done:
		case <-ctx.Done():
			return
		}
	}
}

// fromEarlierList answers a list from a finished list of the same kind that
// had no more objects: the same selector or no selector, in the same
// namespace or in all namespaces. The answer is the same as the cluster would
// give, in the same order. It is used only when the answer fits in max.
func (c *callReader) fromEarlierList(k kinds.Kind, ns, sel string, max int) (kube.Page, bool) {
	want, err := labels.Parse(sel)
	if err != nil {
		return kube.Page{}, false
	}
	kk := kindKey(k)
	c.mu.Lock()
	lists := append([]doneList(nil), c.lists...)
	c.mu.Unlock()
	for _, l := range lists {
		if l.kind != kk || (l.ns != "" && l.ns != ns) || (l.sel != "" && l.sel != sel) {
			continue
		}
		var items []unstructured.Unstructured
		for _, u := range l.items {
			if ns != "" && u.GetNamespace() != ns {
				continue
			}
			if !want.Matches(labels.Set(u.GetLabels())) {
				continue
			}
			items = append(items, u)
		}
		if len(items) > max {
			continue
		}
		return kube.Page{Items: items, Remaining: -1}, true
	}
	return kube.Page{}, false
}

// ListMeta reads the metadata of the objects of a kind. The same read in one
// call is made once. A metadata list never answers a full list.
func (c *callReader) ListMeta(ctx context.Context, k kinds.Kind, ns string, max int) (kube.MetaPage, error) {
	key := fmt.Sprintf("listmeta|%s|%s|%d", kindKey(k), ns, max)
	e := c.do(ctx, key, nil, func(e *readEntry) {
		e.meta, e.err = c.inner.ListMeta(ctx, k, ns, max)
	})
	if e.err != nil {
		return kube.MetaPage{}, e.err
	}
	p := e.meta
	p.Items = append([]metav1.PartialObjectMetadata(nil), p.Items...)
	return p, nil
}

// Events reads events.
func (c *callReader) Events(ctx context.Context, ns string, about *kube.ObjectRef, max int) (kube.Page, error) {
	a := "-"
	if about != nil {
		a = about.UID + "|" + about.Kind + "|" + about.Name
	}
	key := fmt.Sprintf("events|%s|%s|%d", ns, a, max)
	e := c.do(ctx, key, nil, func(e *readEntry) {
		e.page, e.err = c.inner.Events(ctx, ns, about, max)
		e.page.Items = cloneItems(e.page.Items)
	})
	if e.err != nil {
		return kube.Page{}, e.err
	}
	p := e.page
	p.Items = cloneItems(p.Items)
	return p, nil
}

// Metrics reads usage from the metrics API.
func (c *callReader) Metrics(ctx context.Context, t kube.MetricsTarget, ns, sel string) ([]unstructured.Unstructured, error) {
	key := fmt.Sprintf("metrics|%s|%s|%s", t, ns, sel)
	e := c.do(ctx, key, nil, func(e *readEntry) {
		e.items, e.err = c.inner.Metrics(ctx, t, ns, sel)
		e.items = cloneItems(e.items)
	})
	if e.err != nil {
		return nil, e.err
	}
	return cloneItems(e.items), nil
}

// Logs reads pod logs. It is bounded but not memoized.
func (c *callReader) Logs(ctx context.Context, ns, pod string, o kube.LogOptions) (string, error) {
	if !c.slot(ctx) {
		return "", ctx.Err()
	}
	defer c.release(ctx)
	return c.inner.Logs(ctx, ns, pod, o)
}

var _ kube.Reader = (*callReader)(nil)

// future is a read (or a chain of reads) that runs while the caller does
// something else.
type future[T any] struct {
	done   chan struct{}
	cancel context.CancelFunc
	val    T
	panic  any
}

// start runs fn in its own goroutine, with a context that drop can cancel.
func start[T any](ctx context.Context, fn func(ctx context.Context) T) *future[T] {
	cctx, cancel := context.WithCancel(ctx)
	f := &future[T]{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(f.done)
		defer func() {
			if p := recover(); p != nil {
				f.panic = p
			}
		}()
		f.val = fn(cctx)
	}()
	return f
}

// wait returns the value. A panic in fn is raised again here, in the goroutine
// of the tool, where safeDispatch handles it.
func (f *future[T]) wait() T {
	<-f.done
	f.cancel()
	if f.panic != nil {
		panic(f.panic)
	}
	return f.val
}

// drop cancels the work that is not needed after all and waits until it has
// stopped, so that nothing runs after the call returns. It is safe to call
// after wait.
func (f *future[T]) drop() {
	f.cancel()
	<-f.done
}

// parallel runs the functions at the same time and returns when all have
// returned. A panic in one is raised again in the caller.
func parallel(fns ...func()) {
	switch len(fns) {
	case 0:
		return
	case 1:
		fns[0]()
		return
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var pv any
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					mu.Lock()
					if pv == nil {
						pv = p
					}
					mu.Unlock()
				}
			}()
			fn()
		}()
	}
	wg.Wait()
	if pv != nil {
		panic(pv)
	}
}
