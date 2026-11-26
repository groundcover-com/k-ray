package tools

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/jsonpath"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

func (r *Runner) get(ctx context.Context, in GetInput, red *redact.Redactor) (output, error) {
	k, err := r.kind(in.Kind)
	if err != nil {
		return output{}, err
	}
	if err := objectName("name", in.Name); err != nil {
		return output{}, err
	}
	if len(in.JSONPath) > maxJSONPath {
		return output{}, badInput("jsonpath is longer than %d characters", maxJSONPath)
	}
	ns, err := r.namespaceFor(k, in.Namespace, false)
	if err != nil {
		return output{}, err
	}
	out := output{namespace: ns, full: in.Full}
	loc, err := r.locateNamed(ctx, k, ns, in.Namespace != "", in.Name)
	if err != nil {
		return out, err
	}
	switch {
	case len(loc.matches) > 1:
		items := make([]ambiguousItem, len(loc.matches))
		for i, m := range loc.matches {
			items[i] = ambiguousItem{k, m}
		}
		title := fmt.Sprintf("get %s %s · all namespaces · context %s", k.Singular(), in.Name, r.Context)
		amb := r.ambiguous(title, in.Name, items, func(it ambiguousItem) string {
			return r.hintGet(k.Singular(), in.Name, it.u.GetNamespace())
		})
		amb.full = in.Full
		if loc.more {
			amb.notes = append(amb.notes, fmt.Sprintf("only the first %s %s were read.", shape.Thousands(len(loc.items)), k.Name()))
		}
		return amb, nil
	case loc.obj == nil:
		return out, loc.notFound(k, in.Name)
	}
	u := loc.obj
	if loc.header != "" {
		ns = u.GetNamespace()
		out.namespace = ns
	}
	row := shape.Inspect(u, r.now())
	out.title = fmt.Sprintf("get %s %s%s · context %s", k.Singular(), in.Name, inNS(ns), r.Context)
	out.summary = []string{"state: " + string(row.State)}
	if loc.header != "" {
		out.summary = append([]string{loc.header}, out.summary...)
	}

	if in.JSONPath != "" {
		return r.getPath(out, in, u, red)
	}
	var b strings.Builder
	writeProblems(&b, row, r.rt)
	yaml, rep := objectCompact(u, in.Full, red)
	if len(rep.Outline) > 0 {
		b.WriteString("data outline:\n")
		for _, l := range rep.Outline {
			b.WriteString("- " + l + "\n")
		}
		b.WriteString("---\n")
	}
	budget := r.budget(in.Full) - b.Len()
	cut, wasCut := shape.Cut(yaml, budget)
	b.WriteString(cut)
	out.body = b.String()
	if wasCut {
		out.truncated = true
		note := fmt.Sprintf("object cut: %s of %s bytes shown.", shape.Thousands(len(cut)), shape.Thousands(len(yaml)))
		if !in.Full {
			note += " For a larger budget: " + r.verb() + " get with " + r.arg("full", "true") + "."
		}
		out.notes = append(out.notes, note)
	}
	if left := leftOut(rep); left != "" {
		out.notes = append(out.notes, "left out: "+left+"; "+r.arg("full", "true")+" shows them.")
	}
	out.notes = append(out.notes, rep.Notes...)
	if row.State == shape.Unhealthy || row.State == shape.Warning {
		out.next = append(out.next, r.hintDescribe(k.Singular(), in.Name, ns))
	}
	return out, nil
}

// objectYAML trims and redacts a copy of the object and renders it.
func objectYAML(u *unstructured.Unstructured, full bool, red *redact.Redactor) string {
	s, _ := objectCompact(u, full, red)
	return s
}

// objectCompact trims, redacts, then cuts long values of a copy of the
// object, in this order: redaction sees every value whole before any cut. It
// renders the object and reports what it left out. With full, nothing is
// dropped or cut.
func objectCompact(u *unstructured.Unstructured, full bool, red *redact.Redactor) (string, shape.Report) {
	obj := u.DeepCopy().Object
	rep := shape.Trim(obj, full)
	red.Object(obj)
	if !full {
		shape.CutLong(obj, &rep)
	}
	return shape.YAML(obj), rep
}

// leftOut says in one short phrase what the default output left out.
func leftOut(rep shape.Report) string {
	var parts []string
	if rep.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d default or repeated fields", rep.Dropped))
	}
	if rep.CutValues > 0 {
		word := "long values"
		if rep.CutValues == 1 {
			word = "long value"
		}
		parts = append(parts, fmt.Sprintf("%d %s", rep.CutValues, word))
	}
	return strings.Join(parts, ", ")
}

func writeProblems(b *strings.Builder, row shape.Row, redact func(string) string) {
	if len(row.Problems) == 0 {
		return
	}
	b.WriteString("looks wrong:\n")
	for _, p := range shape.ProblemLines(row.Lines(redact)) {
		b.WriteString("- " + p + "\n")
	}
	b.WriteString("---\n")
}

func inNS(ns string) string {
	if ns == "" {
		return ""
	}
	return " · namespace " + ns
}

// maxJSONPath is the longest jsonpath template that get accepts.
const maxJSONPath = 1024

// getPath answers a jsonpath template. The template runs against the object
// after it is trimmed of its managed fields and redacted, never against the
// raw object, and against the full shape: no defaults dropped, no values cut,
// so a path that a person knows from kubectl finds its field. A missing key
// gives an empty answer, as in kubectl.
func (r *Runner) getPath(out output, in GetInput, u *unstructured.Unstructured, red *redact.Redactor) (output, error) {
	obj := u.DeepCopy().Object
	shape.Trim(obj, true)
	red.Object(obj)
	// The template is echoed in an error and in the title: after redaction.
	echo := red.Text(clip(in.JSONPath))
	tpl := in.JSONPath
	if !strings.Contains(tpl, "{") {
		tpl = "{" + tpl + "}" // as kubectl: a bare path is a template
	}
	jp := jsonpath.New("get").AllowMissingKeys(true)
	if err := jp.Parse(tpl); err != nil {
		return out, badInput("jsonpath %q does not parse: %s", echo, red.Text(oneLineErr(err)))
	}
	var buf bytes.Buffer
	if err := jp.Execute(&buf, obj); err != nil {
		return out, badInput("jsonpath %q failed: %s", echo, red.Text(oneLineErr(err)))
	}
	out.title = fmt.Sprintf("get %s %s%s · jsonpath %s · context %s", strings.ToLower(u.GetKind()), in.Name, inNS(out.namespace), echo, r.Context)
	text := buf.String()
	if text == "" {
		out.notes = append(out.notes, "the path found no value.")
	}
	cut, wasCut := shape.Cut(text, r.budget(in.Full)-len(echo))
	out.body = cut
	if wasCut {
		out.truncated = true
		out.notes = append(out.notes, fmt.Sprintf("answer cut: %s of %s bytes shown. Use a narrower path.", shape.Thousands(len(cut)), shape.Thousands(len(text))))
	}
	return out, nil
}

func oneLineErr(err error) string {
	return clip(strings.Join(strings.Fields(err.Error()), " "))
}
