package shape

import (
	"fmt"
	"strings"
)

// Head lengths of a rendered group. With errors, the other lines only say
// what normal looks like, so they are cut shorter.
const (
	logsHeadCap          = 400
	logsOtherCapByErrors = 110
)

// logsRenderGroups writes the error groups, then the other groups, within
// max bytes. The errors get most of the budget when there are other lines.
// It returns the body and how many groups and lines it shows.
func logsRenderGroups(s LogSummary, errs, other []*logGroup, entries []LogEntry, totalPods, max int, mine bool) (body string, shown, shownLines int) {
	otherCap := logsHeadCap
	if len(errs) > 0 {
		otherCap = logsOtherCapByErrors
	}
	var b strings.Builder
	budget := max
	errBudget := max * 6 / 10
	if len(other) == 0 {
		errBudget = max
	}
	if len(errs) > 0 {
		used, n, lines := logsWriteErrors(&b, errs, s.ErrorEntries, totalPods, errBudget)
		budget -= used
		shown += n
		shownLines += lines
	}
	if len(other) > 0 {
		otherLines, otherGroups := s.Entries-s.ErrorEntries, s.Groups-len(errs)
		var h string
		switch {
		case mine:
			h = logsUnusualFirstHeader(otherLines, otherGroups)
		case len(errs) > 0 && len(other) < otherGroups:
			h = logsLargestShownHeader(otherLines, otherGroups)
		default:
			h = logsInTimeHeader(otherLines, otherGroups)
		}
		budget -= len(h)
		var picked []string
		var lines int
		if mine {
			picked, lines = logsPickRanked(other, entries, totalPods, otherCap, budget)
		} else {
			picked, lines = logsPickRecent(other, totalPods, otherCap, budget)
		}
		b.WriteString(h)
		for _, l := range picked {
			b.WriteString(l)
		}
		shown += len(picked)
		shownLines += lines
	}
	return strings.TrimRight(b.String(), "\n"), shown, shownLines
}

// logsWriteErrors writes the error groups, most recent first, while they fit
// in budget bytes. It returns the bytes used and the groups and lines shown.
func logsWriteErrors(b *strings.Builder, errs []*logGroup, errorEntries, totalPods, budget int) (used, shown, shownLines int) {
	h := logsErrorsHeader(errorEntries, len(errs))
	b.WriteString(h)
	used = len(h)
	for _, g := range errs {
		line := renderGroup(g, totalPods, logsHeadCap)
		if used+len(line) > budget {
			break
		}
		b.WriteString(line)
		used += len(line)
		shown++
		shownLines += g.count
	}
	return used, shown, shownLines
}

// logsPickRanked renders mined groups from the top of the ranking, while they
// fit in budget bytes.
func logsPickRanked(other []*logGroup, entries []LogEntry, totalPods, headCap, budget int) (picked []string, lines int) {
	used := 0
	for _, g := range other {
		line := renderMined(g, entries, totalPods, headCap)
		if used+len(line) > budget {
			break
		}
		picked = append(picked, line)
		used += len(line)
		lines += g.count
	}
	return picked, lines
}

// logsPickRecent takes the most recent groups that fit in budget bytes, and
// returns them in time order.
func logsPickRecent(other []*logGroup, totalPods, headCap, budget int) (picked []string, lines int) {
	used := 0
	for i := len(other) - 1; i >= 0; i-- {
		line := renderGroup(other[i], totalPods, headCap)
		if used+len(line) > budget {
			break
		}
		picked = append(picked, line)
		used += len(line)
		lines += other[i].count
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	return picked, lines
}

func logsErrorsHeader(lines, groups int) string {
	return fmt.Sprintf("errors: %s in %s (most recent first)\n", Count(lines, "line"), Count(groups, "group"))
}

func logsInTimeHeader(lines, groups int) string {
	return fmt.Sprintf("other lines: %s in %s (oldest to newest)\n", Thousands(lines), Count(groups, "group"))
}

func logsUnusualFirstHeader(lines, groups int) string {
	return fmt.Sprintf("other lines: %s in %s (unusual first, then largest)\n", Thousands(lines), Count(groups, "group"))
}

func logsLargestShownHeader(lines, groups int) string {
	return fmt.Sprintf("other lines: %s in %s; the largest are shown\n", Thousands(lines), Count(groups, "group"))
}
