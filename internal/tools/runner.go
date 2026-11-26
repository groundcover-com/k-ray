// Package tools implements the six read-only tools: list, get, describe,
// logs, events, top. The MCP server and the CLI both call Runner.Call,
// so the two surfaces behave the same.
//
// Every call: strict argument decoding, validation, reads through
// kube.Reader, redaction of everything that leaves, a fenced frame with a
// size budget, and one audit line (fail closed).
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/groundcover-com/kray/internal/audit"
	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/kube/readonly"
	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// Names lists the six tools, in the order they are registered.
var Names = []string{"list", "get", "describe", "logs", "events", "top"}

// Runner runs tool calls.
type Runner struct {
	Reader     kube.Reader
	Kinds      *kinds.Set
	Context    string // kubeconfig context, fixed at start
	Namespace  string // default namespace
	Identity   string
	Budget     int // bytes per result
	FullBudget int // bytes per result with full=true
	ExtraKeys  []string
	Audit      *audit.Logger
	Surface    string // "mcp" or "cli"
	Session    string
	Timeout    time.Duration
	Now        func() time.Time
	Nonce      func() string

	// red is the redactor of the call in progress (set by Call on the copy
	// of the Runner that the tools use). Text that a tool cuts is redacted
	// with it first, see rt and cut.
	red *redact.Redactor
}

// Result is what a surface returns to its caller.
type Result struct {
	Text    string
	IsError bool
}

// output is what one tool produces before redaction and framing.
type output struct {
	title     string
	summary   []string
	body      string // cluster content (goes inside the fence)
	notes     []string
	next      []string
	namespace string
	truncated bool
	full      bool
}

// errInput marks a problem with the arguments (shown as-is to the caller).
type errInput struct{ msg string }

func (e errInput) Error() string { return e.msg }

func badInput(format string, args ...any) error { return errInput{fmt.Sprintf(format, args...)} }

// Call runs one tool with raw JSON arguments.
func (r *Runner) Call(ctx context.Context, tool string, raw []byte) Result {
	start := r.now()
	red := redact.New(r.ExtraKeys)
	var blocked atomic.Int64
	ctx = readonly.WithBlockCounter(ctx, &blocked)
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	// The tools of this call read through a per-call reader: reads that do not
	// depend on each other run at the same time, and the same read is made
	// once. It is dropped with the call; nothing is kept between calls.
	run := *r
	run.red = red
	run.Reader = newCallReader(r.Reader)
	out, err := run.safeDispatch(ctx, tool, raw, red)
	var res Result
	func() {
		defer func() {
			if p := recover(); p != nil {
				res = Result{Text: "error: " + errInternal.Error() + "\n", IsError: true}
			}
		}()
		if err != nil {
			// The text of an error from the cluster is never printed: it
			// is outside the fence (callError).
			res = Result{Text: "error: " + red.Text(callError(err, r.requestOf(tool, raw))) + "\n", IsError: true}
		} else {
			res = r.render(out, red)
		}
	}()

	entry := audit.Entry{
		Time:            start.UTC(),
		Surface:         r.Surface,
		Tool:            tool,
		Args:            auditArgs(raw, red),
		Context:         r.Context,
		Namespace:       out.namespace,
		Identity:        r.Identity,
		ResultBytes:     len(res.Text),
		Truncated:       out.truncated,
		Redactions:      red.Counts().Total(),
		BlockedRequests: blocked.Load(),
		DurationMS:      r.now().Sub(start).Milliseconds(),
		Session:         r.Session,
		Version:         brand.Version,
	}
	if res.IsError {
		entry.Error = strings.TrimSpace(strings.TrimPrefix(res.Text, "error: "))
	}
	if r.Audit == nil {
		return Result{Text: "error: the call was not completed because no audit log is open\n", IsError: true}
	}
	if aerr := r.Audit.Write(entry); aerr != nil {
		return Result{Text: "error: the call was not completed because the audit log could not be written: " + red.Text(aerr.Error()) + "\n", IsError: true}
	}
	return res
}

// errInternal is returned when a tool panics. The panic value is not shown:
// it can hold cluster data.
var errInternal = errors.New("internal error while running the tool; the call was stopped")

// safeDispatch turns a panic into an error, so the call still returns a
// result and an audit line.
func (r *Runner) safeDispatch(ctx context.Context, tool string, raw []byte, red *redact.Redactor) (out output, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = output{}, errInternal
		}
	}()
	return r.dispatch(ctx, tool, raw, red)
}

func (r *Runner) dispatch(ctx context.Context, tool string, raw []byte, red *redact.Redactor) (output, error) {
	switch tool {
	case "list":
		var in ListInput
		if err := decode(raw, &in); err != nil {
			return output{}, err
		}
		return r.list(ctx, in, red)
	case "get":
		var in GetInput
		if err := decode(raw, &in); err != nil {
			return output{}, err
		}
		return r.get(ctx, in, red)
	case "describe":
		var in DescribeInput
		if err := decode(raw, &in); err != nil {
			return output{}, err
		}
		return r.describe(ctx, in, red)
	case "logs":
		var in LogsInput
		if err := decode(raw, &in); err != nil {
			return output{}, err
		}
		return r.logs(ctx, in, red)
	case "events":
		var in EventsInput
		if err := decode(raw, &in); err != nil {
			return output{}, err
		}
		return r.events(ctx, in)
	case "top":
		var in TopInput
		if err := decode(raw, &in); err != nil {
			return output{}, err
		}
		return r.top(ctx, in)
	}
	return output{}, badInput("unknown tool %q; tools: %s", tool, strings.Join(Names, ", "))
}

// decode is strict: unknown fields and trailing data are errors, so no
// argument the schema does not list can reach a tool.
func decode(raw []byte, into any) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = []byte("{}")
	}
	if err := exactNames(raw, into); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return badInput("invalid arguments: %v", err)
	}
	if dec.More() {
		return badInput("invalid arguments: trailing data")
	}
	return nil
}

// exactNames rejects argument names that do not match a field name exactly.
// encoding/json matches names without case, so "NAME" would set Name while
// the audit line shows another "name".
func exactNames(raw []byte, into any) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil // not an object; the strict decode reports it
	}
	allowed := map[string]bool{}
	t := reflect.TypeOf(into)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		allowed[name] = true
	}
	for k := range m {
		if !allowed[k] {
			return badInput("invalid arguments: json: unknown field %q", clip(k))
		}
	}
	return nil
}

func auditArgs(raw []byte, red *redact.Redactor) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		s := string(raw)
		if strings.TrimSpace(s) == "" {
			return map[string]any{}
		}
		// Redact first, then cut: a cut must not split a secret.
		return map[string]any{"_raw": shape.CutWords(red.Text(s), 512)}
	}
	// Redact a copy so the result counts stay about the result. Keys are
	// redacted too: an unknown argument name can hold a secret.
	quiet := redact.New(nil)
	out := map[string]any{}
	n := 0
	for k, v := range m {
		if n++; n > 32 {
			out["_more_args"] = len(m) - 32
			break
		}
		if s, ok := v.(string); ok {
			v = shape.CutWords(quiet.Text(s), 512)
		}
		out[shape.CutWords(quiet.Text(k), 128)] = v
	}
	quiet.Object(out)
	return out
}

func (r *Runner) render(out output, red *redact.Redactor) Result {
	nonce := shape.Nonce()
	if r.Nonce != nil {
		nonce = r.Nonce()
	}
	body := red.Text(out.body) // the final pattern pass
	header := []string{red.Text(out.title)}
	for _, s := range out.summary {
		header = append(header, red.Text(s))
	}
	counts := red.Counts()
	if counts.Total() > 0 { // no such line means zero
		header = append(header, fmt.Sprintf("redactions: %d (%s)", counts.Total(), counts.String()))
	}
	var notes, next []string
	for _, n := range out.notes {
		notes = append(notes, red.Text(n))
	}
	for _, n := range out.next {
		next = append(next, red.Text(n))
	}
	f := shape.Frame{Header: header, Body: body, Notes: notes, Next: next}
	text := f.Render(nonce)
	// Tools fit their own output. This is the safety net that keeps every result inside the budget.
	limit := r.Budget
	if out.full {
		limit = r.FullBudget
	}
	if limit <= 0 {
		limit = 16 << 10
	}
	if len(text) > limit {
		keep := len(body) - (len(text) - limit) - 300
		if keep < 0 {
			keep = 0
		}
		cut, _ := shape.Cut(body, keep)
		f.Body = cut
		f.Notes = append([]string{fmt.Sprintf("output cut at the %s byte limit: %s of %s bytes of cluster content shown.", shape.Thousands(limit), shape.Thousands(len(cut)), shape.Thousands(len(body)))}, notes...)
		text = f.Render(nonce)
	}
	return Result{Text: text}
}

func (r *Runner) budget(full bool) int {
	b := r.Budget
	if full {
		b = r.FullBudget
	}
	if b <= 0 {
		b = 16 << 10
	}
	// Leave room for the header, fences and notes.
	return b - 700
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// rt redacts cluster text. Redaction runs before any cut, so a cut cannot
// split a secret and leave a part that no rule matches.
func (r *Runner) rt(s string) string {
	if r.red == nil {
		return redact.New(r.ExtraKeys).Text(s)
	}
	return r.red.Text(s)
}

// cut redacts s, then cuts it to about n bytes at a word.
func (r *Runner) cut(s string, n int) string {
	return shape.CutWords(r.rt(s), n)
}
