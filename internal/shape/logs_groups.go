package shape

import (
	"fmt"
	"sort"
)

// logsGrouping holds the groups of a log as they are built, in the order of
// their first entry.
type logsGrouping struct {
	mine         bool
	tr           *tree                // the template tree, when mine
	idx          map[string]*logGroup // groups by masked text, when not mined
	groups       []*logGroup
	errorEntries int
}

// logsGroupAll puts each entry in a group. With words, a group that has an
// entry with one of the words is marked as a hit.
func logsGroupAll(entries []LogEntry, mine bool, words []string) *logsGrouping {
	gr := &logsGrouping{mine: mine, idx: map[string]*logGroup{}}
	if mine {
		gr.tr = newTree()
	}
	for i, e := range entries {
		g := gr.groupOf(e, i)
		if len(words) > 0 && !g.hit && EntryHas(e, words) {
			g.hit = true
		}
		g.addEntry(e, i)
		if e.Error {
			gr.errorEntries++
		}
	}
	return gr
}

// groupOf returns the group of entry i, and makes it when it is new. An error
// entry is grouped by its masked text and the causes shown with it, also in a
// mined log.
func (gr *logsGrouping) groupOf(e LogEntry, i int) *logGroup {
	if gr.mine && !e.Error {
		g, isNew := gr.tr.place(e.Text)
		if isNew {
			g.first, g.pods = e.Time, map[string]int{}
			gr.groups = append(gr.groups, g)
		}
		g.d.members = append(g.d.members, int32(i))
		return g
	}
	key := logsGroupKey(e)
	g, ok := gr.idx[key]
	if !ok {
		g = &logGroup{tpl: key, err: e.Error, first: e.Time, pods: map[string]int{}}
		gr.idx[key] = g
		gr.groups = append(gr.groups, g)
	}
	return g
}

// logsGroupKey is the masked text of an entry and of the causes that are
// shown with it.
func logsGroupKey(e LogEntry) string {
	causes, _ := shownMore(e)
	key := fmt.Sprint(e.Error) + "|" + Template(e.Text)
	for _, c := range causes {
		key += "\n" + Template(c)
	}
	return key
}

// addEntry counts entry i in the group: its time span, its most recent
// sample and its pod.
func (g *logGroup) addEntry(e LogEntry, i int) {
	g.count++
	if !e.Time.IsZero() && (g.first.IsZero() || e.Time.Before(g.first)) {
		g.first = e.Time
	}
	if g.last.IsZero() || !e.Time.Before(g.last) {
		g.last = e.Time
	}
	g.lastIdx, g.sample = i, e
	if e.Pod != "" {
		if g.pods[e.Pod] == 0 {
			g.podOrder = append(g.podOrder, e.Pod)
		}
		g.pods[e.Pod]++
	}
}

// logsKeepHits keeps the groups that a match hit, and counts their entries
// and error entries.
func logsKeepHits(groups []*logGroup) (kept []*logGroup, entries, errorEntries int) {
	for _, g := range groups {
		if g.hit {
			kept = append(kept, g)
			entries += g.count
			if g.err {
				errorEntries += g.count
			}
		}
	}
	return kept, entries, errorEntries
}

func logsSplitErrors(groups []*logGroup) (errs, other []*logGroup) {
	for _, g := range groups {
		if g.err {
			errs = append(errs, g)
		} else {
			other = append(other, g)
		}
	}
	return errs, other
}

// logsOrderInTime orders the groups without an error from oldest to newest.
// With errors, only the largest are kept: they only say what normal looks
// like.
func logsOrderInTime(other []*logGroup, haveErrors bool) []*logGroup {
	sort.SliceStable(other, func(i, j int) bool { return other[i].lastIdx < other[j].lastIdx })
	if !haveErrors || len(other) <= otherGroupsWithErrors {
		return other
	}
	return logsKeepLargest(other, otherGroupsWithErrors)
}

// logsKeepLargest keeps the n largest groups, in the order that they have.
// Of two groups of equal size, the more recent one is kept.
func logsKeepLargest(other []*logGroup, n int) []*logGroup {
	big := append([]*logGroup(nil), other...)
	sort.SliceStable(big, func(i, j int) bool {
		if big[i].count != big[j].count {
			return big[i].count > big[j].count
		}
		return big[i].lastIdx > big[j].lastIdx
	})
	keep := map[*logGroup]bool{}
	for _, g := range big[:n] {
		keep[g] = true
	}
	var kept []*logGroup
	for _, g := range other {
		if keep[g] {
			kept = append(kept, g)
		}
	}
	return kept
}
