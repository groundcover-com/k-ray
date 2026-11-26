package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// A call that names one object and gives no namespace looks in the default
// namespace first, with one read, as it always did. Only when the object is
// not there, one list of the kind in all namespaces finds it. A call that
// gives a namespace never looks elsewhere. The fallback is on the not-found
// path only: when the object is in the default namespace, the reads and the
// output are the same as without the fallback.
//
// A search by name reads the metadata of the kind only (kube.Reader.ListMeta),
// not the objects: a kind such as configmaps can hold hundreds of megabytes.
// Only the objects that match are then read in full (readFull).

// located is the result of looking for one named object.
type located struct {
	obj     *unstructured.Unstructured     // exactly one object
	matches []*unstructured.Unstructured   // several objects, in different namespaces
	header  string                         // set when the object is not in the default namespace
	notErr  error                          // the not-found error of the default namespace
	items   []metav1.PartialObjectMetadata // what the fallback list returned, for the closest names
	more    bool                           // the fallback list was cut at the fetch cap
}

// fullGetMax is how many of the objects that a metadata list matched are read
// one by one. With more, the kind is read with one list in the same scope, as
// before the search read metadata: that is fewer requests, and still capped
// at fetchCap objects.
const fullGetMax = 10

// readFull reads in full the objects that a metadata list of kind k in ns
// matched, in the same order. An object that is gone since the metadata list
// is left out.
func (r *Runner) readFull(ctx context.Context, k kinds.Kind, ns string, metas []metav1.PartialObjectMetadata) ([]*unstructured.Unstructured, error) {
	if len(metas) == 0 {
		return nil, nil
	}
	got := make([]*unstructured.Unstructured, len(metas))
	if len(metas) <= fullGetMax {
		errs := make([]error, len(metas))
		fns := make([]func(), len(metas))
		for i := range metas {
			fns[i] = func() { got[i], errs[i] = r.Reader.Get(ctx, k, metas[i].Namespace, metas[i].Name) }
		}
		parallel(fns...)
		for _, err := range errs {
			if err != nil && !apierrors.IsNotFound(err) {
				return nil, err
			}
		}
	} else {
		page, err := r.Reader.List(ctx, k, ns, "", fetchCap)
		if err != nil {
			return nil, err
		}
		at := make(map[string]int, len(metas))
		for i := range metas {
			at[metas[i].Namespace+"/"+metas[i].Name] = i
		}
		for i := range page.Items {
			if j, ok := at[page.Items[i].GetNamespace()+"/"+page.Items[i].GetName()]; ok {
				got[j] = &page.Items[i]
			}
		}
	}
	out := got[:0]
	for _, u := range got {
		if u != nil {
			out = append(out, u)
		}
	}
	return out, nil
}

// locateNamed reads kind/name in ns. When the caller gave no namespace
// (given is false), the kind is namespaced and the object is not in ns, it
// lists the metadata of the kind in all namespaces once and reads in full the
// objects with that exact name. Any other error is returned as it is.
func (r *Runner) locateNamed(ctx context.Context, k kinds.Kind, ns string, given bool, name string) (located, error) {
	u, err := r.Reader.Get(ctx, k, ns, name)
	if err == nil {
		return located{obj: u}, nil
	}
	if given || !k.Namespaced() || !apierrors.IsNotFound(err) {
		return located{}, err
	}
	loc := located{notErr: err}
	page, lerr := r.Reader.ListMeta(ctx, k, "", fetchCap)
	if lerr != nil {
		return loc, nil // say what the first read said: not found
	}
	loc.items, loc.more = page.Items, page.More
	var metas []metav1.PartialObjectMetadata
	for i := range page.Items {
		if page.Items[i].Name == name {
			metas = append(metas, page.Items[i])
		}
	}
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Namespace < metas[j].Namespace })
	if loc.matches, err = r.readFull(ctx, k, "", metas); err != nil {
		return located{}, err
	}
	if len(loc.matches) == 1 {
		loc.obj, loc.matches = loc.matches[0], nil
		loc.header = r.foundHeader(loc.obj.GetNamespace(), ns)
	}
	return loc, nil
}

func (r *Runner) foundHeader(found, def string) string {
	return fmt.Sprintf("found in namespace %s; none in the default namespace %s", found, def)
}

// notFound is the error for an object that is nowhere. It keeps the text of
// the API and adds that all namespaces were read, with the closest names.
func (l located) notFound(k kinds.Kind, name string) error {
	if l.notErr == nil {
		return fmt.Errorf("%s %q not found", k.Name(), name)
	}
	if l.items == nil && !l.more {
		return l.notErr // the list was not read, or was empty
	}
	var names []seenName
	for i := range l.items {
		names = append(names, seenName{k.Kind(), l.items[i].GetNamespace(), l.items[i].GetName()})
	}
	msg := l.notErr.Error() + " (also looked in all namespaces)"
	if l.more {
		msg += fmt.Sprintf("; only the first %s %s were read", shape.Thousands(len(l.items)), k.Name())
	}
	if c := closestNames(names, name); len(c) > 0 {
		msg += "\nclosest names:\n" + strings.Join(c, "\n")
	}
	return notFoundError{l.notErr, msg}
}

// notFoundError keeps the original error, so that it can still be tested.
type notFoundError struct {
	err error
	msg string
}

func (e notFoundError) Error() string { return e.msg }
func (e notFoundError) Unwrap() error { return e.err }

// ambiguousItem is one of several objects that have the asked name.
type ambiguousItem struct {
	k kinds.Kind
	u *unstructured.Unstructured
}

// ambiguous is the answer when the name is in several namespaces and the tool
// does not guess: one short line for each (namespace, name, state), and what
// to give.
func (r *Runner) ambiguous(title, name string, items []ambiguousItem, hint func(ambiguousItem) string) output {
	now := r.now()
	nss := map[string]bool{}
	mixed := false
	for _, it := range items {
		nss[it.u.GetNamespace()] = true
		if it.k.Kind() != items[0].k.Kind() {
			mixed = true
		}
	}
	out := output{title: title}
	out.summary = []string{fmt.Sprintf("%d objects are named %s, in %d namespaces: give %s", len(items), name, len(nss), r.argName("namespace"))}
	var b strings.Builder
	for i, it := range items {
		if i == matchListMax {
			fmt.Fprintf(&b, "(%d more)\n", len(items)-i)
			break
		}
		if mixed {
			b.WriteString(strings.ToLower(it.k.Kind()) + " ")
		}
		fmt.Fprintf(&b, "%s/%s", it.u.GetNamespace(), it.u.GetName())
		if st := shape.Inspect(it.u, now).State; st != shape.NoHealth {
			b.WriteString(": " + string(st))
		}
		b.WriteString("\n")
		if i < maxDescribed {
			out.next = append(out.next, hint(it))
		}
	}
	out.body = b.String()
	return out
}

// describeLocated describes an object found by kind and name, in the default
// namespace or, with one list, in another.
func (r *Runner) describeLocated(ctx context.Context, in DescribeInput, k kinds.Kind, ns string, red *redact.Redactor) (output, error) {
	loc, err := r.locateNamed(ctx, k, ns, in.Namespace != "", in.Name)
	if err != nil {
		return output{namespace: ns}, err
	}
	switch {
	case loc.obj != nil:
		fns := loc.obj.GetNamespace()
		out, err := r.describeOne(ctx, k, fns, in.Name, loc.obj, red, r.budget(false))
		if loc.header != "" {
			out.summary = append([]string{loc.header}, out.summary...)
		}
		return out, err
	case len(loc.matches) > 1:
		var targets []target
		for _, u := range loc.matches {
			targets = append(targets, target{k: k, u: u})
		}
		var notes []string
		if loc.more {
			notes = append(notes, fmt.Sprintf("only the first %s %s were read.", shape.Thousands(len(loc.items)), k.Name()))
		}
		title := fmt.Sprintf("describe %s %s · all namespaces · context %s", k.Singular(), in.Name, r.Context)
		return r.describeMatches(ctx, in.Name, title, "", targets, notes, red)
	}
	return output{namespace: ns}, loc.notFound(k, in.Name)
}

// logResolved is what a logs call with a pod name found: a pod, or, when no
// pod has the name, the workload that has it.
type logResolved struct {
	pod         *unstructured.Unstructured
	workload    *unstructured.Unstructured
	workloadRef string // kind/name, for example deployment/web
	ns, header  string
}

// workloadKinds are the workload kinds whose logs a name can stand for.
var workloadKinds = []string{"deployments", "statefulsets", "daemonsets", "jobs"}

// locateLogPod finds the pod that a logs call names. The name is looked for
// as a pod, in the default namespace and, with one list, in the others. When
// no pod has the name and the caller gave no namespace, the name is looked for
// as a workload, with one list for each workload kind, and the logs are those
// of the workload. It returns an output when the name is ambiguous.
func (r *Runner) locateLogPod(ctx context.Context, podKind kinds.Kind, ns string, in LogsInput) (logResolved, *output, error) {
	loc, err := r.locateNamed(ctx, podKind, ns, in.Namespace != "", in.Pod)
	if err != nil {
		return logResolved{}, nil, err
	}
	switch {
	case loc.obj != nil:
		return logResolved{pod: loc.obj, ns: loc.obj.GetNamespace(), header: loc.header}, nil, nil
	case len(loc.matches) > 1:
		items := make([]ambiguousItem, len(loc.matches))
		for i, m := range loc.matches {
			items[i] = ambiguousItem{podKind, m}
		}
		title := fmt.Sprintf("logs pod %s · all namespaces · context %s", in.Pod, r.Context)
		out := r.ambiguous(title, in.Pod, items, func(it ambiguousItem) string {
			return r.hintLogs(in.Pod, it.u.GetNamespace(), in.Container, in.Previous, in.Full)
		})
		return logResolved{}, &out, nil
	}

	// No pod has the name. It may be the name of a workload.
	var ks []kinds.Kind
	for _, n := range workloadKinds {
		if k, err := r.Kinds.Lookup(n); err == nil {
			ks = append(ks, k)
		}
	}
	// The metadata of each kind is read, and the objects with the name in full.
	found := make([][]*unstructured.Unstructured, len(ks))
	fns := make([]func(), len(ks))
	for i, k := range ks {
		fns[i] = func() {
			p, err := r.Reader.ListMeta(ctx, k, "", fetchCap)
			if err != nil {
				return
			}
			var metas []metav1.PartialObjectMetadata
			for _, m := range p.Items {
				if m.Name == in.Pod {
					metas = append(metas, m)
				}
			}
			found[i], _ = r.readFull(ctx, k, "", metas)
		}
	}
	parallel(fns...)
	var all, inDefault []ambiguousItem
	for i, k := range ks {
		for _, u := range found[i] {
			it := ambiguousItem{k, u}
			all = append(all, it)
			if u.GetNamespace() == ns {
				inDefault = append(inDefault, it)
			}
		}
	}
	var pick *ambiguousItem
	switch {
	case len(inDefault) == 1:
		pick = &inDefault[0]
	case len(inDefault) == 0 && len(all) == 1:
		pick = &all[0]
	case len(all) > 1:
		sort.SliceStable(all, func(i, j int) bool { return all[i].u.GetNamespace() < all[j].u.GetNamespace() })
		title := fmt.Sprintf("logs %s · all namespaces · context %s", in.Pod, r.Context)
		out := r.ambiguous(title, in.Pod, all, func(it ambiguousItem) string {
			return "logs workload=" + it.k.Singular() + "/" + in.Pod + r.nsPart(it.u.GetNamespace())
		})
		out.summary = []string{fmt.Sprintf("no pod is named %s; %d workloads are named so: %s one of the next calls", in.Pod, len(all), r.verb())}
		return logResolved{}, &out, nil
	}
	if pick == nil {
		return logResolved{}, nil, loc.notFound(podKind, in.Pod)
	}
	ref := pick.k.Singular() + "/" + in.Pod
	fns2 := pick.u.GetNamespace()
	return logResolved{
		workload: pick.u, workloadRef: ref, ns: fns2,
		header: fmt.Sprintf("no pod is named %s; reading the logs of %s in namespace %s", in.Pod, ref, fns2),
	}, nil, nil
}
