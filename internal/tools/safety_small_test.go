package tools

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/redact"
)

// Item 6b: an env value that starts with NUL is a value, not a reference.
func TestDiffEnvValueThatStartsWithNULIsRedactedByName(t *testing.T) {
	tmpl := func(v string) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "x:1",
			Env: []corev1.EnvVar{{Name: "DB_PASSWORD", Value: v}, {Name: "MODE", Value: "a"}},
		}}}}
	}
	changes, _ := diffTemplates(tmpl("\u0000hunter2"), tmpl("\u0000hunter3"), redactor{redact.New(nil)})
	var out []string
	for _, c := range changes {
		out = append(out, c.text)
	}
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "hunter") {
		t.Errorf("a secret value that starts with NUL is shown:\n%s", joined)
	}
	if !strings.Contains(joined, "DB_PASSWORD") {
		t.Errorf("the change is not reported:\n%s", joined)
	}
	// A reference to a Secret is a name and stays readable.
	ref := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app", Image: "x:1",
		Env: []corev1.EnvVar{{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "pw"}}}},
	}}}}
	changes, _ = diffTemplates(tmpl("x"), ref, redactor{redact.New(nil)})
	out = nil
	for _, c := range changes {
		out = append(out, c.text)
	}
	mustContain(t, strings.Join(out, "\n"), "from secret db key pw")
}

// Item 6c: a row with fewer cells than the header does not stop the call.
func TestDropSharedToleratesShortRows(t *testing.T) {
	header := []string{"NAME", "READY", "STATUS", "AGE"}
	cells := [][]string{
		{"a", "1/1", "Running", "5m"},
		{"b"},
		{"c", "1/1"},
		{"d", "1/1", "Running", "5m"},
	}
	_, _, _ = dropShared(header, cells, 1)
	_, _, _ = dropShared(header, [][]string{{}, {"x"}}, 1)
	_, _, _ = dropShared(header, [][]string{{"a", "1/1", "Running", "5m"}, {"b", "1/1", "Running", "5m"}, {}}, 1)
}

// Item 6a: a panic in a log read becomes an error for that pod, not a crash
// of the process.
type panicLogs struct{ *fakeReader }

func (p panicLogs) Logs(ctx context.Context, ns, pod string, o kube.LogOptions) (string, error) {
	panic("boom " + pod)
}

func TestLogsReadPanicIsAnErrorForThatPod(t *testing.T) {
	r, _ := newRunner(t, nil)
	r.Reader = panicLogs{&fakeReader{objs: objsFrom(t, shopPod)}}
	res := call(t, r, "logs", map[string]any{"pod": "web-1", "namespace": "shop"})
	if !res.IsError {
		t.Fatalf("want an error result:\n%s", res.Text)
	}
	mustNotContain(t, res.Text, "boom")
}
