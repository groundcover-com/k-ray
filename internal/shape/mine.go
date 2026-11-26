package shape

import (
	"regexp"
	"strings"
)

// Template mining for log lines that are not errors. It follows two known
// ideas. Drain (He et al., "Drain: An Online Log Parsing Approach with Fixed
// Depth Tree") groups lines with a small tree: by token count, then by the
// first two tokens, then by the share of equal tokens at the same position.
// CLP (Compressed Log Processor) splits a line into fixed text and
// variables: the text is kept once per group, and each variable keeps its
// values with counts (see vars.go).
//
// A line is first cut into slots. A slot is a plain token, or a key=value or
// "key: value" pair (the key is the fixed part, the value is the variable), or
// a quoted free text value (fixed text, it must match exactly), or a time
// stamp (ignored). The redactor runs before this code, so a redaction mark is
// one token, and every mark counts as the same value.

const (
	slotPlain int8 = iota
	slotKV
	slotColon
	slotText
)

const (
	// mineThreshold is the least share of equal tokens for a line to join an
	// existing group. A key=value slot counts for kvWeight of a plain token,
	// because lines that share their keys are not alike for that reason only.
	mineThreshold = 0.5
	kvWeight      = 0.25
	// maxSlots bounds the work for one very long line: the rest of the line is
	// one slot.
	maxSlots = 96
	// maxLeaf is how many groups one leaf of the tree keeps for matching. More
	// groups are still made, but later lines do not match them.
	maxLeaf = 100
	// minMineEntries is the size below which a log is grouped as before: a
	// short log is shown in full detail and needs no summary of its variables.
	minMineEntries = 30
)

const wildToken = "<*>"

// slot is one comparable part of a line.
type slot struct {
	kind int8
	cmp  string // comparable form: digits masked, marks collapsed
	val  string // the variable part: the value of a pair, else the token; marks collapsed
	raw  string // as in the line
	key  string // the key of a pair
	ts   bool   // a time stamp: ignored
}

var (
	tsTokenRE   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}([T ])?)?(\d{2}:\d{2}:\d{2}([.,]\d+)?(Z|[+-]\d{2}:?\d{2})?)?$`)
	klogPrefix  = regexp.MustCompile(`^[IWEF]\d{4}$`)
	markRE      = regexp.MustCompile(`\[REDACTED:[^\]\s]*\]`)
	keyTokenRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	markNeedle  = "[REDACTED:"
	markedBlank = "[REDACTED]"
)

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			return true
		}
	}
	return false
}

// canon makes every redaction mark the same text, so that the rule that
// matched does not make two values.
func canon(s string) string {
	if !strings.Contains(s, markNeedle) {
		return s
	}
	return markRE.ReplaceAllString(s, markedBlank)
}

// maskDigits replaces each run of digits, with the dots between digits, by
// "<n>".
func maskDigits(s string) string {
	if !hasDigit(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		for i < len(s) && (isDigit(s[i]) || (s[i] == '.' && i+1 < len(s) && isDigit(s[i+1]))) {
			i++
		}
		b.WriteString("<n>")
	}
	return b.String()
}

func tsToken(t string) bool {
	if len(t) < 8 || !isDigit(t[0]) {
		return len(t) == 5 && klogPrefix.MatchString(t)
	}
	return tsTokenRE.MatchString(t)
}

// fields splits on spaces and tabs without allocating a token.
func fields(s string, buf []string) []string {
	buf = buf[:0]
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if start >= 0 {
				buf = append(buf, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		buf = append(buf, s[start:])
	}
	return buf
}

// cutKV splits "key=value" when the key is a plain name and the value is not
// empty.
func cutKV(t string) (key, val string, ok bool) {
	eq := strings.IndexByte(t, '=')
	if eq <= 0 || eq == len(t)-1 {
		return "", "", false
	}
	if !keyTokenRE.MatchString(t[:eq]) {
		return "", "", false
	}
	return t[:eq], t[eq+1:], true
}

func isColonKey(t string) bool {
	return len(t) > 1 && t[len(t)-1] == ':' && keyTokenRE.MatchString(t[:len(t)-1])
}

func isKeyTok(t string) bool {
	if _, _, ok := cutKV(t); ok {
		return true
	}
	return isColonKey(t)
}

func closedQuote(v string) bool {
	return len(v) >= 2 && v[len(v)-1] == '"' && v[len(v)-2] != '\\'
}

// slots cuts a line into slots. toks is a scratch buffer.
func slots(text string, toks []string, out []slot) ([]slot, []string) {
	toks = fields(text, toks)
	out = out[:0]
	for j := 0; j < len(toks); j++ {
		t := toks[j]
		if len(out) >= maxSlots-1 {
			rest := strings.Join(toks[j:], " ")
			c := canon(rest)
			out = append(out, slot{kind: slotPlain, cmp: maskDigits(c), val: c, raw: rest})
			break
		}
		if tsToken(t) {
			out = append(out, slot{kind: slotPlain, cmp: "<ts>", val: "<ts>", raw: "<ts>", ts: true})
			continue
		}
		if k, v, ok := cutKV(t); ok {
			if v[0] == '"' && !closedQuote(v) {
				// A quoted value with spaces is the message: fixed text.
				end := j
				joined := t
				for end+1 < len(toks) && end-j < 40 && !closedQuote(joined) {
					end++
					joined += " " + toks[end]
				}
				if closedQuote(joined) {
					c := canon(joined)
					out = append(out, slot{kind: slotText, cmp: maskDigits(c), val: c, raw: joined, key: k})
					j = end
					continue
				}
			}
			c := canon(v)
			out = append(out, slot{kind: slotKV, cmp: k + "=", val: c, raw: v, key: k})
			continue
		}
		if isColonKey(t) && j+1 < len(toks) && !isKeyTok(toks[j+1]) && (j+2 == len(toks) || isKeyTok(toks[j+2])) {
			k := t[:len(t)-1]
			v := toks[j+1]
			out = append(out, slot{kind: slotColon, cmp: k + ": ", val: canon(v), raw: v, key: k})
			j++
			continue
		}
		c := canon(t)
		out = append(out, slot{kind: slotPlain, cmp: maskDigits(c), val: c, raw: t})
	}
	return out, toks
}

// gslot is one position of a group's template.
type gslot struct {
	kind   int8
	cmp    string
	key    string
	raw    string // the first value, as in the line
	val    string // the first value, marks collapsed
	wild   bool   // the token differs between lines
	varies bool   // the value differs between lines
	ts     bool
}

// route is the token a line is routed by in the tree: a token with a digit
// counts as a wildcard.
func route(s *slot) string {
	if s.kind == slotPlain && hasDigit(s.raw) {
		return wildToken
	}
	return s.cmp
}

// similarity is the share of equal tokens at the same position. It is 0 when
// a fixed text differs.
func similarity(g []gslot, l []slot) float64 {
	var num, den float64
	for i := range l {
		gs, ls := &g[i], &l[i]
		if ls.ts || gs.ts {
			continue
		}
		w := 1.0
		switch {
		case ls.kind == slotKV || ls.kind == slotColon:
			w = kvWeight
		case ls.kind == slotPlain && strings.Contains(ls.cmp, "<n>"):
			continue // a number: it says nothing about the line
		case gs.kind == slotPlain && strings.Contains(gs.cmp, "<n>"):
			continue
		}
		equal := !gs.wild && gs.kind == ls.kind && gs.cmp == ls.cmp
		if ls.kind == slotText && !equal {
			return 0
		}
		den += w
		if equal {
			num += w
		}
	}
	if den == 0 {
		return 1
	}
	return num / den
}

// tree is the fixed depth tree: token count, then the first two tokens, then
// a short list of groups.
type tree struct {
	byLen map[int]map[string][]*logGroup
	// scratch buffers
	toks  []string
	slots []slot
}

func newTree() *tree { return &tree{byLen: map[int]map[string][]*logGroup{}} }

// place finds the group of a line, or makes one. It returns the group and
// whether it is new.
func (t *tree) place(text string) (*logGroup, bool) {
	t.slots, t.toks = slots(text, t.toks, t.slots)
	ls := t.slots
	k := ""
	if len(ls) > 0 {
		k = route(&ls[0])
	}
	if len(ls) > 1 {
		k += "\x00" + route(&ls[1])
	}
	level, ok := t.byLen[len(ls)]
	if !ok {
		level = map[string][]*logGroup{}
		t.byLen[len(ls)] = level
	}
	leaf := level[k]
	var best *logGroup
	bestSim := -1.0
	for _, g := range leaf {
		if s := similarity(g.d.slots, ls); s > bestSim {
			best, bestSim = g, s
		}
	}
	if best != nil && bestSim >= mineThreshold {
		best.d.merge(ls)
		return best, false
	}
	g := &logGroup{d: &mined{}}
	g.d.slots = make([]gslot, len(ls))
	for i := range ls {
		g.d.slots[i] = gslot{kind: ls[i].kind, cmp: ls[i].cmp, key: ls[i].key, raw: ls[i].raw, val: ls[i].val, ts: ls[i].ts}
	}
	if len(leaf) < maxLeaf {
		level[k] = append(leaf, g)
	}
	return g, true
}

// mined is what a group of the tree adds to a logGroup.
type mined struct {
	slots   []gslot
	members []int32 // indexes of the entries
	nv      int
	ids     []uint8 // value ids, nv per member
}

func (m *mined) merge(ls []slot) {
	for i := range ls {
		g, l := &m.slots[i], &ls[i]
		if !g.wild && (g.cmp != l.cmp || g.kind != l.kind) {
			g.wild, g.kind, g.key = true, slotPlain, ""
			g.varies = true
		}
		if g.val != l.val {
			g.varies = true
		}
	}
}
