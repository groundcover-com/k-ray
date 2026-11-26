package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/groundcover-com/kray/internal/shape"
)

// A list with many rows that have a problem is shown smaller. These are the
// sizes. full and all give the whole list.
const (
	// largeListRows is the number of rows with a problem, after folding, above
	// which a list is "large".
	largeListRows = 20
	// largeListBody is the size in bytes of the table of a large list, with
	// the lines that go with it. The result is about a kilobyte larger.
	largeListBody = 5600
	// shortCauseMax is the size in bytes of a cause on the row of a large list.
	shortCauseMax = 100
	// healthyNamespacesLarge is how many namespaces the count of healthy
	// objects names in a large list.
	healthyNamespacesLarge = 8
	// matchHealthyShown is how many healthy objects that match get a row.
	matchHealthyShown = 10
	// notShownNamespaces is how many namespaces the not-shown line names.
	notShownNamespaces = 60
)

// shortCause shortens a cause to about max bytes: the first clause, which says
// what is wrong, with the key noun kept (the image, the missing object, the
// exit code). A quoted copy of a name that the clause already has is dropped.
// If there is room, the quote of the last error follows.
func shortCause(c string, limit int) string {
	c = strings.Join(strings.Fields(c), " ")
	if len(c) <= limit {
		return c
	}
	suffix := ""
	if i := strings.LastIndex(c, " (+"); i > 0 && strings.HasSuffix(c, " more)") {
		suffix = c[i:]
		c = c[:i]
	}
	segs := strings.Split(c, "; ")
	head, tail := segs[0], ""
	for _, s := range segs[1:] {
		if strings.HasPrefix(s, "last error: ") {
			tail = s
		}
	}
	head = dropRepeats(head)
	if i := strings.Index(head, " | "); i > 0 {
		head = head[:i] // the second cause stays out: describe has it
	}
	room := limit - len(suffix)
	out := head
	// A clause at the end, after a comma, explains; the noun is early.
	for len(out) > room {
		i := strings.LastIndex(out, ", ")
		if i <= room/3 {
			break
		}
		out = out[:i]
	}
	if len(out) > room {
		if cut := shape.CutWords(out, room); !strings.HasPrefix(cut, "(long") {
			out = cut
		} else {
			out = middleCut(out, room)
		}
	}
	if tail != "" && len(out)+2+len(tail) <= room {
		out += "; " + tail
	}
	return out + suffix
}

// dropRepeats drops the parts of "a: b: c" that hold a long word that an
// earlier part has: an image reference that the message repeats in quotes.
func dropRepeats(s string) string {
	parts := strings.Split(s, ": ")
	if len(parts) < 2 {
		return s
	}
	seen := map[string]bool{}
	var kept []string
	for _, p := range parts {
		dup := false
		var words []string
		for _, w := range strings.Fields(p) {
			w = strings.Trim(w, `"'.,()`)
			if len(w) >= 12 {
				words = append(words, w)
				if seen[w] {
					dup = true
				}
			}
		}
		if dup && len(kept) > 0 {
			continue
		}
		for _, w := range words {
			seen[w] = true
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ": ")
}

var restartsSaidRE = regexp.MustCompile(`(\d+) restarts?\b|restarted (\d+) times?\b`)

// said reports whether the cause already says the value of a column.
func said(col, val, cause string) bool {
	if col == "RESTARTS" {
		if val == "0" {
			return true
		}
		for _, m := range restartsSaidRE.FindAllStringSubmatch(cause, -1) {
			if m[1] == val || m[2] == val {
				return true
			}
		}
		return false
	}
	return len(val) >= 3 && strings.Contains(strings.ToLower(cause), strings.ToLower(val))
}

// dropShared removes the kind columns (from index first, before the last
// column, which is the cause) that tell nothing: the same value in every row,
// or a value the cause of the row already says. It returns the new table and
// the shared values, as "COLUMN value", for the one line that says them.
func dropShared(header []string, cells [][]string, first int) ([]string, [][]string, []string) {
	if len(cells) < 3 {
		return header, cells, nil
	}
	// A row with fewer cells than the header is padded, so that no index is
	// out of range.
	if len(header) > 0 {
		padded := make([][]string, len(cells))
		for i, c := range cells {
			if len(c) < len(header) {
				c = append(append([]string(nil), c...), make([]string, len(header)-len(c))...)
			}
			padded[i] = c
		}
		cells = padded
	}
	last := len(header) - 1
	var keep []int
	var same []string
	for j := 0; j < len(header); j++ {
		if j < first || j >= last {
			keep = append(keep, j)
			continue
		}
		uniform, allSaid := true, true
		for _, c := range cells {
			if c[j] != cells[0][j] {
				uniform = false
			}
			if !said(header[j], c[j], c[last]) {
				allSaid = false
			}
		}
		switch {
		case uniform:
			same = append(same, header[j]+" "+cells[0][j])
		case allSaid:
		default:
			keep = append(keep, j)
		}
	}
	if len(keep) == len(header) && len(same) == 0 {
		return header, cells, nil
	}
	h := make([]string, len(keep))
	for i, j := range keep {
		h[i] = header[j]
	}
	out := make([][]string, len(cells))
	for r, c := range cells {
		row := make([]string, len(keep))
		for i, j := range keep {
			row[i] = c[j]
		}
		out[r] = row
	}
	return h, out, same
}

// notShownLine counts the rows that are not shown, by namespace, so that a
// text filter on a namespace tells where the rest is. The namespace of each
// hidden row is given.
func notShownLine(namespaces []string) string {
	by := map[string]int{}
	for _, ns := range namespaces {
		by[ns]++
	}
	order := make([]string, 0, len(by))
	for ns := range by {
		order = append(order, ns)
	}
	sort.Slice(order, func(i, j int) bool {
		if by[order[i]] != by[order[j]] {
			return by[order[i]] > by[order[j]]
		}
		return order[i] < order[j]
	})
	var parts []string
	for i, ns := range order {
		if i == notShownNamespaces {
			parts = append(parts, fmt.Sprintf("+%d more namespaces", len(order)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", ns, by[ns]))
	}
	rows := "rows"
	if len(namespaces) == 1 {
		rows = "row"
	}
	return fmt.Sprintf("not shown: %d %s (%s)", len(namespaces), rows, strings.Join(parts, ", "))
}
