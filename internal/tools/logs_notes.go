package tools

import (
	"fmt"
	"strings"

	"github.com/groundcover-com/kray/internal/shape"
)

// logsHeading writes the title, the summary line and the notes about what was
// not read.
func (r *Runner) logsHeading(c *logsCall, st logsReadStats) {
	container := logsCommonContainer(c.targets)
	c.out.title = r.logsTitle(c, logsWhichContainers(container, c.in.Previous), st.manyNS)

	var others []string
	if len(c.targets) == 1 {
		others = logsOtherContainers(c.allContainers[0], container)
	}
	since := ""
	if c.since > 0 {
		since = c.in.Since
	}
	sum := logsLinesRead(st.lines, c.tail)
	if len(c.targets) > 1 {
		sum = logsLinesReadFromPods(st.lines, len(c.targets)-st.noLogs, c.tail)
	}
	sum += logsSinceClause(since) + ")" + logsOtherContainersClause(others)
	c.out.summary = append(append([]string(nil), c.header...), sum)

	if c.total > len(c.targets) {
		c.out.truncated = true
		c.out.notes = append(c.out.notes, r.logsMorePodsNote(c.total, len(c.targets)))
	}
	if c.capped {
		c.out.notes = append(c.out.notes, logsTailCappedNote(c.tail, len(c.targets)))
	}
	if st.noLogs > 0 {
		c.out.notes = append(c.out.notes, logsNoLogsNote(st.noLogs, len(c.targets), st.firstErr))
	}
}

// logsCommonContainer is the container that all targets read, or "" when
// they differ.
func logsCommonContainer(targets []*logTarget) string {
	name := targets[0].container
	for _, t := range targets {
		if t.container != name {
			name = ""
		}
	}
	return name
}

func logsWhichContainers(container string, previous bool) string {
	which := "container " + container
	if container == "" {
		which = "default containers"
	}
	if previous {
		which += " (previous instance)"
	}
	return which
}

func (r *Runner) logsTitle(c *logsCall, which string, manyNS bool) string {
	if len(c.targets) == 1 {
		p := c.targets[0].pod
		podNS := c.ns
		if podNS == "" {
			podNS = p.GetNamespace()
		}
		title := fmt.Sprintf("logs pod %s%s · %s · context %s", p.GetName(), inNS(podNS), which, r.Context)
		if c.pod == nil {
			title += " · " + c.scope
		}
		return title
	}
	shownNS := c.ns
	if shownNS == "" && !manyNS {
		shownNS = c.targets[0].pod.GetNamespace()
	}
	return fmt.Sprintf("logs %s%s · %s · context %s", c.scope, inNS(shownNS), which, r.Context)
}

func logsOtherContainers(containers []string, read string) []string {
	var others []string
	for _, c := range containers {
		if c != read {
			others = append(others, c)
		}
	}
	return others
}

func logsLinesRead(lines, tail int) string {
	return fmt.Sprintf("%s read (tail=%d", shape.Count(lines, "line"), tail)
}

func logsLinesReadFromPods(lines, pods, tail int) string {
	return fmt.Sprintf("%s read from %s (tail=%d per pod", shape.Count(lines, "line"), shape.Count(pods, "pod"), tail)
}

func logsSinceClause(since string) string {
	if since == "" {
		return ""
	}
	return ", since " + since
}

func logsOtherContainersClause(others []string) string {
	if len(others) == 0 {
		return ""
	}
	return "; other containers: " + strings.Join(others, ", ")
}

func (r *Runner) logsMorePodsNote(total, read int) string {
	return fmt.Sprintf("%d pods match; the %d read are the ones with problems first. Narrow with %s.", total, read, r.selectorName())
}

func logsTailCappedNote(tail, pods int) string {
	return fmt.Sprintf("tail is capped at %d lines per pod when %d pods are read.", tail, pods)
}

func logsNoLogsNote(noLogs, pods int, firstErr error) string {
	return fmt.Sprintf("%d of %d pods gave no logs (%s).", noLogs, pods, readFailure(firstErr))
}

// logsBody writes the body: raw lines with full, else the grouped summary. It
// returns false when a match left nothing to show; the call then ends without
// the cut notes.
func (r *Runner) logsBody(c *logsCall, entries []shape.LogEntry, st logsReadStats) bool {
	if c.in.Full {
		return r.logsRawBody(c, entries)
	}
	return r.logsSummaryBody(c, entries, st)
}

func (r *Runner) logsRawBody(c *logsCall, entries []shape.LogEntry) bool {
	if c.mt != nil {
		kept := logsKeepMatching(entries, c.mt.words)
		c.out.summary = append(c.out.summary, logsEntriesMatchLine(len(kept), len(entries)))
		entries = kept
		if len(entries) == 0 {
			c.out.body = "no log line matches"
			return false
		}
	}
	if len(c.targets) > 1 {
		for i := range entries {
			entries[i].Text = "[" + entries[i].Pod + "] " + entries[i].Text
		}
	}
	body, left := shape.RenderRaw(entries, r.budget(true))
	c.out.body = body
	if left > 0 {
		c.out.truncated = true
		c.out.notes = append(c.out.notes, r.logsOlderEntriesNote(len(entries)-left, len(entries), left))
	}
	return true
}

// logsKeepMatching keeps the entries that hold one of the words: with full,
// the match filters raw lines, as there are no groups.
func logsKeepMatching(entries []shape.LogEntry, words []string) []shape.LogEntry {
	var kept []shape.LogEntry
	for _, e := range entries {
		if shape.EntryHas(e, words) {
			kept = append(kept, e)
		}
	}
	return kept
}

func logsEntriesMatchLine(kept, all int) string {
	return fmt.Sprintf("%s of %s log entries match", shape.Thousands(kept), shape.Thousands(all))
}

func (r *Runner) logsOlderEntriesNote(shown, all, left int) string {
	return fmt.Sprintf("showing the last %s of %s log entries; %s older entries left out. Narrow with %s or %s.", shape.Thousands(shown), shape.Thousands(all), shape.Thousands(left), r.argName("since"), r.argName("tail"))
}

func (r *Runner) logsSummaryBody(c *logsCall, entries []shape.LogEntry, st logsReadStats) bool {
	pods := 0
	if len(c.targets) > 1 {
		pods = len(c.targets) - st.noLogs
	}
	budget := r.budget(false)
	var s shape.LogSummary
	if c.mt != nil {
		s = shape.SummarizeLogsMatch(entries, pods, budget, c.mt.words)
		c.out.summary = append(c.out.summary, logsGroupsMatchLine(s))
		if s.Groups == 0 {
			c.out.body = "no log line matches"
			return false
		}
	} else {
		s = shape.SummarizeLogsPods(entries, pods, budget)
	}
	c.out.body = s.Body
	if s.Truncated > 0 {
		c.out.truncated = true
	}
	c.out.notes = append(c.out.notes, r.logsSummaryNote(s, len(c.targets) > 1))
	return true
}

func logsGroupsMatchLine(s shape.LogSummary) string {
	return fmt.Sprintf("%s of %s lines match, in %s of %s groups", shape.Thousands(s.Entries), shape.Thousands(s.TotalEntries), shape.Thousands(s.Groups), shape.Thousands(s.TotalGroups))
}

// logsSummaryNote says what the summary holds, what it left out and how to
// see the rest.
func (r *Runner) logsSummaryNote(s shape.LogSummary, manyPods bool) string {
	note := logsGroupsShown(s)
	if s.Truncated > 0 {
		note += logsGroupsLeftOut(s) + r.logsHowToSeeLeftOut(manyPods)
	}
	return note + r.logsRawLinesHint()
}

func logsGroupsShown(s shape.LogSummary) string {
	return fmt.Sprintf("%s (%s) in %s", shape.Count(s.Entries, "line"), shape.Count(s.ErrorEntries, "error"), shape.Count(s.Groups, "group"))
}

func logsGroupsLeftOut(s shape.LogSummary) string {
	return fmt.Sprintf(", %s (%s) not shown", shape.Count(s.Truncated, "group"), shape.Count(s.TruncatedLines, "line"))
}

func (r *Runner) logsHowToSeeLeftOut(manyPods bool) string {
	how := fmt.Sprintf(". To see them: narrow with %s, %s or %s", r.argName("match"), r.argName("since"), r.argName("tail"))
	if manyPods {
		how += ", or read one pod"
	}
	return how
}

func (r *Runner) logsRawLinesHint() string {
	return fmt.Sprintf(". For raw lines: %s logs with %s.", r.verb(), r.arg("full", "true"))
}

// logsCutNotes says what the tail or the size limit left out, and points to
// the previous instance of a container that restarted.
func (r *Runner) logsCutNotes(c *logsCall, st logsReadStats) {
	if st.atTail > 0 {
		c.out.truncated = true
		if len(c.targets) == 1 {
			c.out.notes = append(c.out.notes, r.logsOlderLinesNote(c.tail))
		} else {
			c.out.notes = append(c.out.notes, r.logsPodsOlderLinesNote(st.atTail, c.tail))
		}
	}
	if st.stopped {
		c.out.truncated = true
		c.out.notes = append(c.out.notes, r.logsSizeLimitNote())
	}
	t := c.targets[0]
	if len(c.targets) == 1 && !c.in.Previous && restarted(t.pod, t.container) {
		c.out.next = append(c.out.next, r.hintLogs(t.pod.GetName(), t.pod.GetNamespace(), t.container, true, false))
	}
}

func (r *Runner) logsOlderLinesNote(tail int) string {
	return fmt.Sprintf("older lines exist beyond %s; raise it (max 5000) or set %s.", r.arg("tail", fmt.Sprint(tail)), r.argName("since"))
}

func (r *Runner) logsPodsOlderLinesNote(pods, tail int) string {
	return fmt.Sprintf("%d pods have older lines beyond %s per pod; set %s or raise tail.", pods, r.arg("tail", fmt.Sprint(tail)), r.argName("since"))
}

func (r *Runner) logsSizeLimitNote() string {
	return "a read stopped at its log size limit; use " + r.argName("since") + " to read a shorter window."
}
