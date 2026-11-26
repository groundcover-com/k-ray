package tools

import (
	"reflect"
	"testing"

	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/shape"
)

func listTestKind(t *testing.T, name string) kinds.Kind {
	t.Helper()
	k, err := kinds.Default().Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestListServiceCauseSentences(t *testing.T) {
	cases := []struct{ got, want string }{
		{listScaledToZeroCause(0, 0, []string{"deployment/web"}), "no ready endpoints (0 of 0): deployment/web is scaled to 0 replicas"},
		{listScaledToZeroCause(0, 2, []string{"deployment/web", "deployment/web2"}), "no ready endpoints (0 of 2): deployment/web, deployment/web2 are scaled to 0 replicas"},
		{listNoReadyEndpointsCause(0, 3), "no ready endpoints (0 of 3)"},
		{listNoPortsCause(), "endpoints have no ports: a targetPort matches no container port"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestListSummary(t *testing.T) {
	pods := listTestKind(t, "pods")
	counts := map[shape.State]int{shape.Unhealthy: 2, shape.Healthy: 1}
	cases := []struct {
		total int
		more  bool
		want  string
	}{
		{3, false, "3 pods: 2 unhealthy, 1 healthy"},
		{1, false, "1 pod: 2 unhealthy, 1 healthy"},
		{2000, true, "first 2,000 pods fetched (the cluster has more): 2 unhealthy, 1 healthy"},
	}
	for _, c := range cases {
		if got := listSummary(pods, c.total, c.more, counts); got != c.want {
			t.Errorf("listSummary(%d, %v) = %q, want %q", c.total, c.more, got, c.want)
		}
	}
	if got := listStateCounts(map[shape.State]int{}); got != "" {
		t.Errorf("no counts: got %q, want empty", got)
	}
}

func TestListCell(t *testing.T) {
	plain := func(s string) string { return s }
	row := shape.Row{Namespace: "shop", Name: "api", Problems: []string{"first", "second", "third"}}
	if got := listCell(map[string]string{"shop/api": "the cause"}, row, plain); got != "the cause" {
		t.Errorf("a diagnosed cause wins: got %q", got)
	}
	if got := listCell(nil, row, plain); got != "first (+2 more)" {
		t.Errorf("first problem and a count: got %q", got)
	}
	if got := listCell(nil, shape.Row{Name: "ok"}, plain); got != "-" {
		t.Errorf("no problem: got %q", got)
	}
}

func listTestRows(states ...shape.State) []shape.Row {
	rows := make([]shape.Row, len(states))
	for i, s := range states {
		rows[i] = shape.Row{Name: string(s) + string(rune('a'+i)), State: s}
	}
	return rows
}

func TestListVisible(t *testing.T) {
	// A short list keeps every row.
	short := listTestRows(shape.Unhealthy, shape.Healthy)
	if v, h, more := listVisible(short, false, false); len(v) != 2 || h != nil || more != 0 {
		t.Errorf("short list: %d visible, %d healthy, %d more", len(v), len(h), more)
	}
	// A long list counts its healthy rows apart.
	long := listTestRows(shape.Unhealthy, shape.Healthy, shape.Healthy, shape.Healthy, shape.Healthy, shape.Healthy, shape.Healthy, shape.Healthy, shape.Warning)
	if v, h, _ := listVisible(long, false, false); len(v) != 2 || len(h) != 7 {
		t.Errorf("long list: %d visible, %d healthy", len(v), len(h))
	}
	if v, h, _ := listVisible(long, false, true); len(v) != len(long) || h != nil {
		t.Errorf("all=true: %d visible, %d healthy", len(v), len(h))
	}
	// A match shows healthy rows up to a small number, after the others.
	var many []shape.State
	for range matchHealthyShown + 2 {
		many = append(many, shape.Healthy)
	}
	matched := listTestRows(append(many, shape.Unhealthy)...)
	v, h, more := listVisible(matched, true, false)
	if len(v) != matchHealthyShown+1 || h != nil || more != 2 || v[0].State != shape.Unhealthy {
		t.Errorf("match: %d visible, %d healthy, %d more, first %s", len(v), len(h), more, v[0].State)
	}
}

func TestListFoldedName(t *testing.T) {
	row := shape.Row{Name: "api-1"}
	cases := []struct {
		f    listFolded
		want string
	}{
		{listFolded{row: row, n: 1}, "api-1"},
		{listFolded{row: row, n: 3, workloads: map[string]bool{"api": true}}, "api-1 (+2 like it)"},
		{listFolded{row: row, n: 3, workloads: map[string]bool{"api": true, "web": true}}, "api-1 (+2 like it, in 2 workloads)"},
	}
	for _, c := range cases {
		if got := listFoldedName(c.f); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestListSizes(t *testing.T) {
	if got := listShownMax(200, false, 30); got != 30 {
		t.Errorf("limit above rows: %d", got)
	}
	if got := listShownMax(5, false, 30); got != 5 {
		t.Errorf("limit below rows: %d", got)
	}
	if got := listShownMax(5, true, 900); got != 500 {
		t.Errorf("full: %d", got)
	}
	if got := listBudget(16000, false, 100); got != 15900 {
		t.Errorf("small list budget: %d", got)
	}
	// A large list takes the healthy line off the lower budget again.
	if got := listBudget(16000, true, 100); got != largeListBody-100 {
		t.Errorf("large list budget: %d", got)
	}
	for _, c := range []struct{ table, healthy, want string }{
		{"t", "", "t"}, {"", "h", "h"}, {"t", "h", "t\nh"}, {"", "", ""},
	} {
		if got := listJoinBody(c.table, c.healthy); got != c.want {
			t.Errorf("listJoinBody(%q, %q) = %q", c.table, c.healthy, got)
		}
	}
}

func TestListCandidatesStopAtTheFirstHealthyRow(t *testing.T) {
	rows := listTestRows(shape.Unhealthy, shape.Warning, shape.Healthy, shape.Unhealthy)
	objs := objsFrom(t, `[
{"apiVersion":"v1","kind":"Service","metadata":{"name":"unhealthya","namespace":""}},
{"apiVersion":"v1","kind":"Service","metadata":{"name":"warningb","namespace":""}},
{"apiVersion":"v1","kind":"Service","metadata":{"name":"healthyc","namespace":""}},
{"apiVersion":"v1","kind":"Service","metadata":{"name":"unhealthyd","namespace":""}}]`)
	c := &listCandidates{kind: "Service", rows: rows, byKey: listByKey(objs), seenOwner: map[string]bool{}}
	var names []string
	for _, x := range c.batch(10) {
		names = append(names, x.row.Name)
	}
	if want := []string{"unhealthya", "warningb"}; !reflect.DeepEqual(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}
