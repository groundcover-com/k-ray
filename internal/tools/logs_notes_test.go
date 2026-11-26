package tools

import (
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestLogsCommonContainer(t *testing.T) {
	same := []*logTarget{{container: "app"}, {container: "app"}}
	mixed := []*logTarget{{container: "app"}, {container: "sidecar"}}
	if got := logsCommonContainer(same); got != "app" {
		t.Errorf("same containers: got %q, want app", got)
	}
	if got := logsCommonContainer(mixed); got != "" {
		t.Errorf("mixed containers: got %q, want empty", got)
	}
}

func TestLogsWhichContainers(t *testing.T) {
	for _, tc := range []struct {
		container string
		previous  bool
		want      string
	}{
		{"app", false, "container app"},
		{"", false, "default containers"},
		{"app", true, "container app (previous instance)"},
	} {
		if got := logsWhichContainers(tc.container, tc.previous); got != tc.want {
			t.Errorf("logsWhichContainers(%q, %v) = %q, want %q", tc.container, tc.previous, got, tc.want)
		}
	}
}

func TestLogsReadSummarySentence(t *testing.T) {
	one := logsLinesRead(1, 2000) + logsSinceClause("") + ")" + logsOtherContainersClause(nil)
	if want := "1 line read (tail=2000)"; one != want {
		t.Errorf("one pod: got %q, want %q", one, want)
	}
	many := logsLinesReadFromPods(1500, 3, 100) + logsSinceClause("5m") + ")" + logsOtherContainersClause([]string{"a", "b"})
	if want := "1,500 lines read from 3 pods (tail=100 per pod, since 5m); other containers: a, b"; many != want {
		t.Errorf("many pods: got %q, want %q", many, want)
	}
}

func TestLogsOtherContainers(t *testing.T) {
	got := logsOtherContainers([]string{"app", "proxy", "log"}, "proxy")
	if want := []string{"app", "log"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLogsHasContainerSeesInitContainers(t *testing.T) {
	if !logsHasContainer("init", []string{"app"}, []string{"init"}) {
		t.Error("an init container is not found")
	}
	if logsHasContainer("other", []string{"app"}, []string{"init"}) {
		t.Error("a missing container is found")
	}
}

func TestLogsManyNamespaces(t *testing.T) {
	pod := func(ns string) *logTarget {
		u := &unstructured.Unstructured{}
		u.SetNamespace(ns)
		return &logTarget{pod: u}
	}
	if logsManyNamespaces([]*logTarget{pod("a"), pod("a")}) {
		t.Error("one namespace reported as many")
	}
	if !logsManyNamespaces([]*logTarget{pod("a"), pod("b")}) {
		t.Error("two namespaces reported as one")
	}
}

func TestLogsNoLogsNoteUsesFixedWords(t *testing.T) {
	got := logsNoLogsNote(2, 5, errors.New("secret text from the cluster"))
	if want := "2 of 5 pods gave no logs (the read of the log failed; read one pod to see the error)."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
