package shape

import (
	"strings"
	"testing"
	"time"
)

func TestLogsGroupKeyKeepsErrorsWithDifferentCausesApart(t *testing.T) {
	a := LogEntry{Text: "request failed", Error: true, More: []string{"Caused by: timeout"}}
	b := LogEntry{Text: "request failed", Error: true, More: []string{"Caused by: refused"}}
	plain := LogEntry{Text: "request failed"}
	if logsGroupKey(a) == logsGroupKey(b) {
		t.Errorf("errors with different causes have one key: %q", logsGroupKey(a))
	}
	if logsGroupKey(a) == logsGroupKey(plain) {
		t.Errorf("an error and a plain line have one key: %q", logsGroupKey(a))
	}
}

func TestLogsGroupAllCountsPodsAndHits(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entries := []LogEntry{
		{Time: t0.Add(2 * time.Second), Text: "user 1 logged in", Pod: "web-2"},
		{Time: t0, Text: "user 2 logged in", Pod: "web-1"},
		{Time: t0.Add(time.Second), Text: "disk full", Error: true, Pod: "web-1"},
	}
	gr := logsGroupAll(entries, false, []string{"disk"})
	if len(gr.groups) != 2 || gr.errorEntries != 1 {
		t.Fatalf("groups=%d errorEntries=%d, want 2 and 1", len(gr.groups), gr.errorEntries)
	}
	g := gr.groups[0]
	if g.count != 2 || g.hit || !g.first.Equal(t0) || g.lastIdx != 1 {
		t.Errorf("first group: count=%d hit=%v first=%v lastIdx=%d", g.count, g.hit, g.first, g.lastIdx)
	}
	if strings.Join(g.podOrder, ",") != "web-2,web-1" {
		t.Errorf("pod order = %v, want web-2,web-1", g.podOrder)
	}
	if !gr.groups[1].hit {
		t.Error("the group with the word is not a hit")
	}
	kept, n, nErr := logsKeepHits(gr.groups)
	if len(kept) != 1 || n != 1 || nErr != 1 {
		t.Errorf("logsKeepHits = %d groups, %d entries, %d errors; want 1, 1, 1", len(kept), n, nErr)
	}
}

func TestLogsKeepLargestKeepsOrderAndPrefersRecent(t *testing.T) {
	a := &logGroup{count: 5, lastIdx: 0}
	b := &logGroup{count: 1, lastIdx: 1}
	c := &logGroup{count: 5, lastIdx: 2}
	d := &logGroup{count: 9, lastIdx: 3}
	got := logsKeepLargest([]*logGroup{a, b, c, d}, 2)
	if len(got) != 2 || got[0] != c || got[1] != d {
		t.Errorf("got %v, want the groups at 2 and 3 in that order", got)
	}
}

func TestLogsPickRecentPrintsInTimeOrder(t *testing.T) {
	var groups []*logGroup
	for _, text := range []string{"first", "second", "third"} {
		groups = append(groups, &logGroup{count: 1, sample: LogEntry{Text: text}})
	}
	last2 := len(renderGroup(groups[1], 0, logsHeadCap)) + len(renderGroup(groups[2], 0, logsHeadCap))
	picked, lines := logsPickRecent(groups, 0, logsHeadCap, last2)
	if lines != 2 || len(picked) != 2 {
		t.Fatalf("picked %d groups, %d lines; want 2 and 2", len(picked), lines)
	}
	if !strings.Contains(picked[0], "second") || !strings.Contains(picked[1], "third") {
		t.Errorf("picked = %q, want second then third", picked)
	}
}

func TestLogsOtherHeaders(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{logsErrorsHeader(1, 2), "errors: 1 line in 2 groups (most recent first)\n"},
		{logsInTimeHeader(1200, 1), "other lines: 1,200 in 1 group (oldest to newest)\n"},
		{logsUnusualFirstHeader(3, 2), "other lines: 3 in 2 groups (unusual first, then largest)\n"},
		{logsLargestShownHeader(3, 2), "other lines: 3 in 2 groups; the largest are shown\n"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
