package shape

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Variable histograms. For each position of a group's template whose value
// differs between lines (a variable), the summary keeps the values with their
// counts, bounded. A value is rare by count only: the code has no list of
// words that matter.

const (
	// maxDistinct is how many distinct values one variable keeps; more is
	// "many".
	maxDistinct = 50
	// maxVars is how many variables of one group are tracked.
	maxVars = 12
	// lowCard is the most distinct values for which all values are shown.
	lowCardMax = 8
	// fewValues is the most distinct values that are shown when none is rare.
	fewValues = 3
	// rareShare: a value is rare when it has at most 1 in rareShare lines of
	// its group, and the group has at least minRareGroup lines.
	rareShare    = 10
	minRareGroup = 10
	// shown per group.
	maxVarLines   = 3
	maxNumLines   = 2
	maxPartners   = 3
	maxRareShown  = 2
	valueCap      = 40
	sampleCap     = 512
	overflowID    = 255
	minNumericRun = 5
)

// variable is the values of one position of a template.
type variable struct {
	pos   int
	name  string
	ids   map[string]uint8
	vals  []string
	cnt   []int
	over  int // lines whose value was not kept
	num   numStat
	named bool
}

// numStat tracks a numeric variable: the exact minimum and maximum, and a
// sample (every stride-th value, the stride doubles when the sample is full)
// for the median.
type numStat struct {
	ok       bool
	started  bool
	unit     string
	min, max float64
	sample   []float64
	stride   int
	seen     int
}

func (v *variable) distinct() int { return len(v.vals) }

// low reports whether all values are known and few: they are listed.
func (v *variable) low() bool {
	return v.over == 0 && len(v.vals) >= 2 && len(v.vals) <= lowCardMax
}

// parseNum reads a number with an optional unit, such as 12, 1.5s, 340ms.
func parseNum(s string) (float64, string, bool) {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	d := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == d {
		return 0, "", false
	}
	if i+1 < len(s) && s[i] == '.' && isDigit(s[i+1]) {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	f, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, "", false
	}
	unit := s[i:]
	if len(unit) > 3 {
		return 0, "", false
	}
	for _, r := range unit {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == 'µ' || r == '%') {
			return 0, "", false
		}
	}
	return f, unit, true
}

func (n *numStat) add(s string) {
	if n.started && !n.ok {
		return
	}
	f, unit, ok := parseNum(s)
	if !ok || (n.started && unit != n.unit) {
		n.ok, n.started = false, true
		n.sample = nil
		return
	}
	if !n.started {
		n.started, n.ok, n.unit, n.min, n.max, n.stride = true, true, unit, f, f, 1
	}
	if f < n.min {
		n.min = f
	}
	if f > n.max {
		n.max = f
	}
	if n.seen%n.stride == 0 {
		n.sample = append(n.sample, f)
		if len(n.sample) >= sampleCap {
			half := n.sample[:0]
			for i := 0; i < len(n.sample); i += 2 {
				half = append(half, n.sample[i])
			}
			n.sample = half
			n.stride *= 2
		}
	}
	n.seen++
}

func (n *numStat) median() float64 {
	s := append([]float64(nil), n.sample...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func fmtNum(f float64, unit string) string {
	return strconv.FormatFloat(f, 'g', 4, 64) + unit
}

// pair is one rare value.
type pair struct {
	v   *variable
	id  int
	cnt int
}

// partner is a value of another variable that goes with a rare value.
type partner struct {
	name, val string
	cnt       int
}

// analysis is what the summary shows of one group's variables.
type analysis struct {
	vars     []*variable
	rare     []pair // the rarest, at most maxRareShown
	partners [][]partner
	example  int // entry index of the latest line that has the first rare value, or -1
	hasRare  bool
}

// analyze reads the variables of a group once more from its entries.
func (g *logGroup) analyze(entries []LogEntry, t *tree) {
	d := g.d
	if d == nil || g.count < 2 {
		return
	}
	a := &analysis{example: -1}
	unnamed := 0
	for i := range d.slots {
		s := &d.slots[i]
		if !s.varies || s.ts || s.kind == slotText || len(a.vars) >= maxVars {
			continue
		}
		v := &variable{pos: i, ids: map[string]uint8{}}
		if !s.wild && (s.kind == slotKV || s.kind == slotColon) {
			v.name, v.named = s.key, true
		} else {
			unnamed++
		}
		a.vars = append(a.vars, v)
	}
	if len(a.vars) == 0 {
		return
	}
	k := 0
	for _, v := range a.vars {
		if !v.named {
			k++
			if unnamed == 1 {
				v.name = wildToken
			} else {
				v.name = fmt.Sprintf("<*%d>", k)
			}
		}
	}
	nv := len(a.vars)
	d.nv = nv
	d.ids = make([]uint8, 0, len(d.members)*nv)
	for _, m := range d.members {
		t.slots, t.toks = slots(entries[m].Text, t.toks, t.slots)
		ls := t.slots
		for _, v := range a.vars {
			if v.pos >= len(ls) {
				d.ids = append(d.ids, overflowID)
				v.over++
				continue
			}
			val := ls[v.pos].val
			id, ok := v.ids[val]
			if !ok {
				if len(v.vals) < maxDistinct {
					id = uint8(len(v.vals))
					v.ids[val] = id
					v.vals = append(v.vals, val)
					v.cnt = append(v.cnt, 0)
					ok = true
				}
			}
			if ok {
				v.cnt[id]++
				d.ids = append(d.ids, id)
			} else {
				v.over++
				d.ids = append(d.ids, overflowID)
			}
			v.num.add(val)
		}
	}
	// Rare values: by count, in variables with few values.
	n := g.count
	if n >= minRareGroup {
		for _, v := range a.vars {
			if !v.low() {
				continue
			}
			for id, c := range v.cnt {
				if c*rareShare <= n {
					a.rare = append(a.rare, pair{v, id, c})
				}
			}
		}
		sort.SliceStable(a.rare, func(i, j int) bool {
			x, y := a.rare[i], a.rare[j]
			if x.cnt != y.cnt {
				return x.cnt < y.cnt
			}
			if x.v.pos != y.v.pos {
				return x.v.pos < y.v.pos
			}
			return x.v.vals[x.id] < y.v.vals[y.id]
		})
		a.hasRare = len(a.rare) > 0
		if len(a.rare) > maxRareShown {
			a.rare = a.rare[:maxRareShown]
		}
	}
	vi := map[*variable]int{}
	for i, v := range a.vars {
		vi[v] = i
	}
	for _, p := range a.rare {
		a.partners = append(a.partners, g.partnersOf(a, vi, p))
	}
	if len(a.rare) > 0 {
		p := a.rare[0]
		col := vi[p.v]
		for r := len(d.members) - 1; r >= 0; r-- {
			if int(d.ids[r*nv+col]) == p.id {
				a.example = int(d.members[r])
				break
			}
		}
	}
	g.an = a
	d.ids = nil // the rows are not needed after this
}

// partnersOf finds, for the lines that have a rare value, the value of each
// other low-cardinality variable that most of them share.
func (g *logGroup) partnersOf(a *analysis, vi map[*variable]int, p pair) []partner {
	d := g.d
	nv := d.nv
	col := vi[p.v]
	counts := make([]map[int]int, nv)
	for r := range d.members {
		if int(d.ids[r*nv+col]) != p.id {
			continue
		}
		for c := 0; c < nv; c++ {
			if c == col || !a.vars[c].low() {
				continue
			}
			if counts[c] == nil {
				counts[c] = map[int]int{}
			}
			counts[c][int(d.ids[r*nv+c])]++
		}
	}
	var out []partner
	for c, m := range counts {
		bestID, best := -1, 0
		for id, n := range m {
			if id == overflowID {
				continue
			}
			if n > best || (n == best && id < bestID) {
				bestID, best = id, n
			}
		}
		if bestID >= 0 && best*2 >= p.cnt {
			out = append(out, partner{a.vars[c].name, a.vars[c].vals[bestID], best})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].cnt > out[j].cnt })
	if len(out) > maxPartners {
		out = out[:maxPartners]
	}
	return out
}

func capVal(s string) string {
	if len(s) <= valueCap {
		return s
	}
	cut := valueCap
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// templateText is the group's template: fixed text as it is, and each
// variable as <*>.
func (g *logGroup) templateText() string {
	byPos := map[int]*variable{}
	if g.an != nil {
		for _, v := range g.an.vars {
			byPos[v.pos] = v
		}
	}
	parts := make([]string, 0, len(g.d.slots))
	for i, s := range g.d.slots {
		name := wildToken
		if v := byPos[i]; v != nil && !v.named {
			name = v.name
		}
		switch {
		case s.ts:
			parts = append(parts, "<ts>")
		case s.wild || (s.kind == slotPlain && s.varies):
			parts = append(parts, name)
		case s.kind == slotKV:
			if s.varies {
				parts = append(parts, s.key+"="+wildToken)
			} else {
				parts = append(parts, s.key+"="+s.raw)
			}
		case s.kind == slotColon:
			if s.varies {
				parts = append(parts, s.key+": "+wildToken)
			} else {
				parts = append(parts, s.key+": "+s.raw)
			}
		default:
			parts = append(parts, s.raw)
		}
	}
	return strings.Join(parts, " ")
}

// varLines renders what the group's variables show, each line indented to
// sit under the group's first line.
func (g *logGroup) varLines(entries []LogEntry) string {
	a := g.an
	if a == nil {
		return ""
	}
	const pad = "         "
	var b strings.Builder
	// Variables with few values: all values with counts, most frequent first.
	rareVar := map[*variable]bool{}
	for _, p := range a.rare {
		rareVar[p.v] = true
	}
	// A variable is shown when it has a rare value, or when it has so few
	// values that the line is short. An even spread over many values (a
	// quantity, a shard) is not a signal and costs a line.
	var low []*variable
	for _, v := range a.vars {
		if v.low() && (rareVar[v] || v.distinct() <= fewValues) {
			low = append(low, v)
		}
	}
	sort.SliceStable(low, func(i, j int) bool {
		if rareVar[low[i]] != rareVar[low[j]] {
			return rareVar[low[i]]
		}
		if low[i].distinct() != low[j].distinct() {
			return low[i].distinct() < low[j].distinct()
		}
		return low[i].pos < low[j].pos
	})
	if len(low) > maxVarLines {
		low = low[:maxVarLines]
	}
	for _, v := range low {
		order := make([]int, len(v.vals))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool {
			if v.cnt[order[i]] != v.cnt[order[j]] {
				return v.cnt[order[i]] > v.cnt[order[j]]
			}
			return v.vals[order[i]] < v.vals[order[j]]
		})
		items := make([]string, len(order))
		for i, id := range order {
			items[i] = fmt.Sprintf("%s ×%d", capVal(v.vals[id]), v.cnt[id])
		}
		fmt.Fprintf(&b, "%s%s: %s\n", pad, v.name, strings.Join(items, " · "))
	}
	// What the rarest values go with.
	for i, p := range a.rare {
		if len(a.partners[i]) == 0 {
			continue
		}
		items := make([]string, len(a.partners[i]))
		for k, pt := range a.partners[i] {
			items[k] = fmt.Sprintf("%s=%s ×%d", pt.name, capVal(pt.val), pt.cnt)
		}
		fmt.Fprintf(&b, "%s%s=%s ×%d goes with %s\n", pad, p.v.name, capVal(p.v.vals[p.id]), p.cnt, strings.Join(items, ", "))
	}
	// Variables with many numeric values: the range.
	if g.count >= minNumericRun {
		shown := 0
		for _, v := range a.vars {
			if shown == maxNumLines {
				break
			}
			if v.low() || !v.num.ok || len(v.num.sample) < 2 {
				continue
			}
			fmt.Fprintf(&b, "%s%s: min %s · median %s · max %s\n", pad, v.name,
				fmtNum(v.num.min, v.num.unit), fmtNum(v.num.median(), v.num.unit), fmtNum(v.num.max, v.num.unit))
			shown++
		}
	}
	// One example, for the rare value.
	if a.example >= 0 && len(a.rare) > 0 {
		e := entries[a.example]
		fmt.Fprintf(&b, "%se.g. %s  %s\n", pad, clock(e.Time), oneLineCap(e.Text, 200))
	}
	return b.String()
}
