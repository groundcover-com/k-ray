package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// describeFoundObj is one object whose name matched the wanted name.
type describeFoundObj struct {
	k     kinds.Kind
	u     *unstructured.Unstructured
	exact bool
}

// describeNameSearch is what a search by name read and found.
type describeNameSearch struct {
	matches     []describeFoundObj
	names       []seenName // every name that was read, for the closest names
	firstErr    error
	readKinds   []string // names of the kinds that were read
	failedKinds []string // "name (reason)" of the kinds that were not
	read        int
	partial     string // the note of the first list that was cut
	podItems    []unstructured.Unstructured
	podKind     kinds.Kind
}

// describeStageRead is the read of one kind of a stage.
type describeStageRead struct {
	page kube.MetaPage
	full []*unstructured.Unstructured // the objects that match, in full
	err  error
}

// describeFoundNamespace checks the name and the namespace of a search by
// name. The namespace is "" when the search covers all namespaces.
func describeFoundNamespace(in DescribeInput) (string, error) {
	if err := objectName("name", in.Name); err != nil {
		return "", err
	}
	if in.Namespace == "" || in.AllNamespaces {
		return "", nil
	}
	if err := labelName("namespace", in.Namespace); err != nil {
		return "", err
	}
	return in.Namespace, nil
}

// describeFoundStages are the kinds that a search by name reads, stage by
// stage: the workloads and services first, then the pods. A kind that the
// caller gives is the only stage.
func (r *Runner) describeFoundStages(in DescribeInput) ([][]kinds.Kind, error) {
	if in.Kind != "" {
		k, err := r.kind(in.Kind)
		if err != nil {
			return nil, err
		}
		return [][]kinds.Kind{{k}}, nil
	}
	lookup := func(names ...string) []kinds.Kind {
		var ks []kinds.Kind
		for _, n := range names {
			if k, err := r.Kinds.Lookup(n); err == nil {
				ks = append(ks, k)
			}
		}
		return ks
	}
	return [][]kinds.Kind{lookup("deployments", "statefulsets", "daemonsets", "jobs", "cronjobs", "services"), lookup("pods")}, nil
}

// describeFoundScope names where a search by name looked.
func describeFoundScope(ns string) string {
	if ns != "" {
		return "namespace " + ns
	}
	return "all namespaces"
}

// describeSearchByName reads the stages in order and stops at the first stage
// with a match. The pods are read only when no workload or service has the
// name: that list is the largest.
func (r *Runner) describeSearchByName(ctx context.Context, stages [][]kinds.Kind, ns, want string) describeNameSearch {
	var s describeNameSearch
	for _, stage := range stages {
		reads := r.describeReadStage(ctx, stage, ns, want)
		for i, k := range stage {
			s.add(k, reads[i], want)
		}
		if len(s.matches) > 0 {
			break
		}
	}
	return s
}

// describeReadStage reads the kinds of one stage at the same time. Each read
// is of the metadata only; the objects that match are then read in full.
func (r *Runner) describeReadStage(ctx context.Context, stage []kinds.Kind, ns, want string) []describeStageRead {
	reads := make([]describeStageRead, len(stage))
	fns := make([]func(), len(stage))
	for i, k := range stage {
		fns[i] = func() {
			kns := ns
			if !k.Namespaced() {
				kns = ""
			}
			reads[i].page, reads[i].err = r.Reader.ListMeta(ctx, k, kns, fetchCap)
			if reads[i].err != nil {
				return
			}
			var metas []metav1.PartialObjectMetadata
			for _, m := range reads[i].page.Items {
				if ok, _ := nameMatch(m.Name, want); ok {
					metas = append(metas, m)
				}
			}
			reads[i].full, reads[i].err = r.readFull(ctx, k, kns, metas)
		}
	}
	parallel(fns...)
	return reads
}

// add takes in the read of one kind, in the order of the stage.
func (s *describeNameSearch) add(k kinds.Kind, rd describeStageRead, want string) {
	if rd.err != nil {
		if s.firstErr == nil {
			s.firstErr = rd.err
		}
		s.failedKinds = append(s.failedKinds, k.Name()+" ("+failureReason(rd.err)+")")
		return
	}
	s.read++
	s.readKinds = append(s.readKinds, k.Name())
	if rd.page.More && s.partial == "" {
		s.partial = describePartialNote(len(rd.page.Items), k.Name())
	}
	for _, m := range rd.page.Items {
		n := m.Name
		if k.Kind() == "Pod" {
			n = workloadNameOf(m.Name, m.OwnerReferences)
		}
		s.names = append(s.names, seenName{k.Kind(), m.Namespace, n})
	}
	for _, u := range rd.full {
		if m, exact := nameMatch(u.GetName(), want); m {
			s.matches = append(s.matches, describeFoundObj{k, u, exact})
			if k.Kind() == "Pod" {
				s.podItems = append(s.podItems, *u)
				s.podKind = k
			}
		}
	}
}

// notRead says once, in a fixed phrase, which kinds could not be read,
// whether or not a match was found: the search did not cover them.
func (s *describeNameSearch) notRead() string {
	if len(s.failedKinds) == 0 {
		return ""
	}
	return "could not read: " + strings.Join(s.failedKinds, ", ")
}

// describeKeepExact keeps the exact matches when there are any.
func describeKeepExact(matches []describeFoundObj) []describeFoundObj {
	exactOnly := matches[:0:0]
	for _, m := range matches {
		if m.exact {
			exactOnly = append(exactOnly, m)
		}
	}
	if len(exactOnly) > 0 {
		return exactOnly
	}
	return matches
}

// describeNotFound is the answer when no object has the name: the closest
// names, and the kinds that were looked at. Only the kinds that were read
// were looked at.
func describeNotFound(in DescribeInput, title, ns, scope string, s describeNameSearch, notes []string) output {
	out := output{title: title, namespace: ns, notes: notes}
	out.summary = []string{describeNoObjectNamed(in.Name)}
	if lines := closestNames(s.names, in.Name); len(lines) > 0 {
		out.body = "closest names:\n" + strings.Join(lines, "\n")
	}
	kindsLooked := joinAnd(s.readKinds)
	if in.Kind != "" {
		kindsLooked = in.Kind
	}
	out.notes = append(out.notes, describeLookedAtNote(kindsLooked, scope))
	if nr := s.notRead(); nr != "" {
		out.notes = append(out.notes, nr+".")
	}
	return out
}

// describeFoundTargets turns the matches into targets. Pods that belong to a
// workload are the workload.
func (r *Runner) describeFoundTargets(ctx context.Context, matches []describeFoundObj, s describeNameSearch) []target {
	var targets []target
	for _, m := range matches {
		if len(s.podItems) == 0 || m.k.Kind() != "Pod" {
			targets = append(targets, target{k: m.k, u: m.u})
		}
	}
	if len(s.podItems) > 0 {
		targets = append(targets, r.collapseToWorkloads(ctx, s.podKind, s.podItems)...)
	}
	return targets
}

// describeSplitServices separates the services from the other targets.
func describeSplitServices(targets []target) (primary, services []target) {
	for _, t := range targets {
		if t.k.Kind() == "Service" {
			services = append(services, t)
		} else {
			primary = append(primary, t)
		}
	}
	return primary, services
}

// describeSortTargets sorts targets by kind, namespace and name.
func describeSortTargets(ts []target) {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].k.Kind() != ts[j].k.Kind() {
			return ts[i].k.Kind() < ts[j].k.Kind()
		}
		if a, b := ts[i].u.GetNamespace(), ts[j].u.GetNamespace(); a != b {
			return a < b
		}
		return ts[i].u.GetName() < ts[j].u.GetName()
	})
}

// describeBestTarget is the one match that answers the name, or nil. One
// match is the answer. So is one workload with a service of the same name:
// the service is named after it. Otherwise the name is ambiguous, and every
// match is shown: the one with a problem is not always the one that was
// meant.
func describeBestTarget(primary, services []target) *target {
	switch {
	case len(primary) == 1:
		return &primary[0]
	case len(primary) == 0 && len(services) == 1:
		return &services[0]
	}
	return nil
}

// describeInNamespace is inNS in the words of a line about a match.
func describeInNamespace(ns string) string {
	return strings.Replace(inNS(ns), " · namespace ", " in namespace ", 1)
}

// describeTargetLine is the one line of a match that is not described: its
// name, the pods that led to it, its state and its first problem.
func (r *Runner) describeTargetLine(t target, now time.Time) string {
	row := shape.Inspect(t.u, now)
	s := t.ref() + describeInNamespace(t.u.GetNamespace())
	if t.pods > 0 {
		s += fmt.Sprintf(" (%s)", plural(t.pods, "pod", "pods"))
	}
	if row.State != shape.NoHealth {
		s += ": " + string(row.State)
		if len(row.Problems) > 0 {
			s += ": " + oneLine(row.Lines(r.rt)[0], 120)
		}
	}
	return s
}

// describeAlsoMatched lists the other matches above the description of the
// best one, at most matchLinesShown of them.
func (r *Runner) describeAlsoMatched(others []target, now time.Time) string {
	if len(others) == 0 {
		return ""
	}
	var ls []string
	for i, t := range others {
		if i == matchLinesShown {
			ls = append(ls, fmt.Sprintf("(%d more)", len(others)-i))
			break
		}
		ls = append(ls, r.describeTargetLine(t, now))
	}
	return "also matched:\n" + strings.Join(ls, "\n") + "\n\n"
}

// describeFoundOne describes the one match that answers the name, under the
// lines of the other matches.
func (r *Runner) describeFoundOne(ctx context.Context, best target, targets []target, notes []string, now time.Time, red *redact.Redactor) (output, error) {
	var others []target
	for _, t := range targets {
		if t.key() != best.key() {
			others = append(others, t)
		}
	}
	extra := r.describeAlsoMatched(others, now)
	out, err := r.describeOne(ctx, best.k, best.u.GetNamespace(), best.u.GetName(), best.u, red, r.budget(false)-len(extra))
	if err != nil {
		return out, err
	}
	// The line repeats the title when it is one object that the name matched
	// itself (no workload of pods, no other match).
	if len(others) > 0 || best.pods > 0 {
		out.summary = append([]string{describeFoundSummary(best)}, out.summary...)
	}
	out.body = extra + out.body
	out.notes = append(out.notes, notes...)
	return out, nil
}
