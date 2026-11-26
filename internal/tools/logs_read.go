package tools

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// logsReadStats is what the reads of a call found, for the summary and the
// notes.
type logsReadStats struct {
	lines    int  // lines read, in all pods
	atTail   int  // pods that gave as many lines as the tail: older lines exist
	stopped  bool // a read stopped at its size limit
	noLogs   int  // pods whose read failed
	firstErr error
	manyNS   bool // the pods are in more than one namespace
}

// logsReadAll reads the pods, a few at a time. Redaction runs after, in one
// place.
func (r *Runner) logsReadAll(ctx context.Context, c *logsCall) {
	sem := make(chan struct{}, maxLogParallel)
	var wg sync.WaitGroup
	for _, t := range c.targets {
		if t.err != nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t *logTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			r.logsReadOne(ctx, c, t)
		}(t)
	}
	wg.Wait()
}

func (r *Runner) logsReadOne(ctx context.Context, c *logsCall, t *logTarget) {
	defer func() {
		// A panic is an error for this pod, as in the other goroutines
		// of a call; the panic value can hold cluster data and is not shown.
		if p := recover(); p != nil {
			t.raw, t.err = "", errInternal
		}
	}()
	t.raw, t.err = r.Reader.Logs(ctx, t.pod.GetNamespace(), t.pod.GetName(), kube.LogOptions{
		Container: t.container, Previous: c.in.Previous, SinceSeconds: int64(c.since.Seconds()),
		TailLines: int64(c.tail), LimitBytes: logLimitBytes / int64(len(c.targets)),
	})
}

// collect parses and redacts what the reads gave, and joins the pods into one
// timeline. A failed read is an error for a one-pod call; with more pods it is
// counted, and the others are still shown.
func (c *logsCall) collect(red *redact.Redactor) ([]shape.LogEntry, logsReadStats, error) {
	st := logsReadStats{manyNS: logsManyNamespaces(c.targets)}
	var entries []shape.LogEntry
	for _, t := range c.targets {
		if t.err != nil {
			if len(c.targets) == 1 {
				return nil, st, t.err
			}
			st.noLogs++
			if st.firstErr == nil {
				st.firstErr = t.err
			}
			continue
		}
		n := strings.Count(t.raw, "\n")
		st.lines += n
		if n >= c.tail {
			st.atTail++
		}
		if int64(len(t.raw)) >= logLimitBytes/int64(len(c.targets)) {
			st.stopped = true
		}
		entries = append(entries, c.parse(t, red, st.manyNS)...)
	}
	if len(c.targets) > 1 {
		// One timeline; stable, so the order inside a pod is kept.
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := entries[i].Time, entries[j].Time
			return !a.IsZero() && !b.IsZero() && a.Before(b)
		})
	}
	return entries, st, nil
}

// parse turns the raw log of one pod into redacted entries, each marked with
// its pod when the call reads more than one.
func (c *logsCall) parse(t *logTarget, red *redact.Redactor, manyNS bool) []shape.LogEntry {
	es := shape.ParseLogs(t.raw)
	if !c.in.Full {
		shape.ReadableJSON(es) // level and message of JSON records
	}
	redactEntries(red, es)
	if len(c.targets) > 1 {
		for i := range es {
			es[i].Pod = t.pod.GetName()
			if manyNS {
				es[i].Pod = t.pod.GetNamespace() + "/" + es[i].Pod
			}
		}
	}
	return es
}

func logsManyNamespaces(targets []*logTarget) bool {
	spread := map[string]bool{}
	for _, t := range targets {
		spread[t.pod.GetNamespace()] = true
	}
	return len(spread) > 1
}
