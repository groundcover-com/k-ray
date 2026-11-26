// Package harness is the canary safety test. Its target is any MCP server
// command: it starts the command, lists its tools, calls every tool with
// every fixture object and with hostile inputs, and then checks that
//
//   - no canary (raw or base64) appears in any result, error, stderr line or
//     the target's own audit file,
//   - the API server audit log shows only get and list from the target's
//     identity, and no secrets, exec, attach, portforward or proxy request,
//   - normal calls are useful (a server that refuses everything fails),
//   - on Linux, no listen() call and no connection to any host other than
//     the API server (strace).
//
// Nothing in this package knows about one particular server.
package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Target is the server under test.
type Target struct {
	Name      string
	Command   []string
	Env       []string // full environment for the command
	AuditFile string   // the target's own audit log, if it has one
	// TraceNetwork runs the command under strace (Linux only) to record
	// connect, bind and listen calls.
	TraceNetwork bool
	APIServer    string // host:port of the API server, for the network check
	Identity     string // Kubernetes username the target runs as
}

// Object is one fixture object.
type Object struct {
	Kind      string // plural resource, for example "pods"
	KindName  string // Kind, for example "Pod"
	Name      string
	Namespace string // "" for cluster-scoped
	Container string // for pods
}

// Fixture is what was planted in the cluster.
type Fixture struct {
	Namespace string
	Canaries  map[string]string // place -> canary (one token)
	// Parts holds the canaries of several parts: place -> parts. A place of
	// a secret of several words or lines has one part for each word or line.
	// Every part is checked by itself, so a leak of one part is a leak.
	Parts   map[string][]string
	Objects []Object
}

// Expect is one usefulness check: the call must succeed and its text must
// contain Contains.
type Expect struct {
	Tool     string
	Args     map[string]any
	Contains string
}

// AuditEvent is the part of a Kubernetes audit event the checks need.
type AuditEvent struct {
	Verb       string `json:"verb"`
	RequestURI string `json:"requestURI"`
	User       struct {
		Username string `json:"username"`
	} `json:"user"`
	ObjectRef *struct {
		Resource    string `json:"resource"`
		Subresource string `json:"subresource"`
		Namespace   string `json:"namespace"`
		Name        string `json:"name"`
	} `json:"objectRef"`
	ResponseStatus *struct {
		Code int `json:"code"`
	} `json:"responseStatus"`
}

// Options control a run.
type Options struct {
	Expect   []Expect
	MaxCalls int // default 600
	// AuditLog returns the API server audit log (JSON lines). It is read
	// after the calls.
	AuditLog func() ([]byte, error)
	// Started marks where this run begins in the audit log (lines before it
	// are ignored), so several runs can share one cluster.
	Started time.Time
	OutDir  string // where to write the transcript and report
}

// Call is one tool call and what came back.
type Call struct {
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Hostile bool           `json:"hostile"`
	IsError bool           `json:"isError"`
	Text    string         `json:"text"`
	Err     string         `json:"err,omitempty"`
}

// Leak is a canary found somewhere.
type Leak struct {
	Place, Form, Where string
	// Part is the 1-based number of the part of a place with several parts
	// that leaked; 0 for a place with one token.
	Part int
}

// Report is the result of a run.
type Report struct {
	Target          string
	Tools           []string
	Calls           []Call
	Leaks           []Leak
	BadRequests     []string // from the API server audit log
	AuditEvents     int
	Useless         []string
	Network         []string
	NetworkVerified bool
	NetworkNote     string
}

// OK is true when every check passed.
func (r *Report) OK() bool {
	return len(r.Leaks) == 0 && len(r.BadRequests) == 0 && len(r.Useless) == 0 && len(r.Network) == 0 && r.AuditEvents > 0
}

// Run runs the harness against one target.
func Run(ctx context.Context, t Target, f Fixture, o Options) (*Report, error) {
	if o.MaxCalls == 0 {
		o.MaxCalls = 600
	}
	rep := &Report{Target: t.Name}
	cmdline := t.Command
	traceFile := ""
	if t.TraceNetwork {
		if _, err := exec.LookPath("strace"); err == nil && isLinux() {
			traceFile = filepath.Join(o.OutDir, t.Name+".strace")
			cmdline = append([]string{"strace", "-f", "-qq", "-e", "trace=connect,bind,listen", "-o", traceFile}, cmdline...)
		} else {
			rep.NetworkNote = "not verified: strace is not available on this host (Linux only)"
		}
	}
	cmd := exec.Command(cmdline[0], cmdline[1:]...)
	cmd.Env = t.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "canary-harness", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w (stderr: %s)", t.Name, err, stderr.String())
	}
	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		_ = cs.Close()
		return nil, fmt.Errorf("list tools: %w", err)
	}
	for _, tool := range tl.Tools {
		rep.Tools = append(rep.Tools, tool.Name)
	}

	plans := Plan(tl.Tools, f)
	for _, e := range o.Expect {
		plans = append(plans, planned{tool: e.Tool, args: e.Args})
	}
	if len(plans) > o.MaxCalls {
		plans = plans[:o.MaxCalls]
	}
	for _, p := range plans {
		c := Call{Tool: p.tool, Args: p.args, Hostile: p.hostile}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		res, err := cs.CallTool(cctx, &mcp.CallToolParams{Name: p.tool, Arguments: p.args})
		cancel()
		if err != nil {
			c.Err = err.Error()
		} else {
			c.IsError = res.IsError
			c.Text = contentText(res)
		}
		rep.Calls = append(rep.Calls, c)
	}
	_ = cs.Close()
	_ = cmd.Wait()

	// Usefulness.
	for _, e := range o.Expect {
		ok := false
		for _, c := range rep.Calls {
			if c.Tool == e.Tool && sameArgs(c.Args, e.Args) && !c.IsError && c.Err == "" && strings.Contains(c.Text, e.Contains) {
				ok = true
			}
		}
		if !ok {
			b, _ := json.Marshal(e.Args)
			rep.Useless = append(rep.Useless, fmt.Sprintf("%s %s did not succeed with %q", e.Tool, b, e.Contains))
		}
	}

	// Canaries everywhere the target wrote.
	for i, c := range rep.Calls {
		rep.Leaks = append(rep.Leaks, f.find(c.Text+"\n"+c.Err, fmt.Sprintf("call %d (%s)", i, c.Tool))...)
	}
	rep.Leaks = append(rep.Leaks, f.find(stderr.String(), "stderr")...)
	if t.AuditFile != "" {
		b, err := os.ReadFile(t.AuditFile)
		if err != nil {
			rep.Leaks = append(rep.Leaks, Leak{Place: "-", Form: "-", Where: "own audit file unreadable: " + err.Error()})
		} else {
			rep.Leaks = append(rep.Leaks, f.find(string(b), "own audit file")...)
		}
	}

	// API server audit log.
	if o.AuditLog != nil {
		// Give the API server a moment to flush.
		time.Sleep(2 * time.Second)
		b, err := o.AuditLog()
		if err != nil {
			return rep, fmt.Errorf("read API server audit log: %w", err)
		}
		rep.AuditEvents, rep.BadRequests = CheckAudit(b, t.Identity, o.Started)
	}

	if traceFile != "" {
		b, err := os.ReadFile(traceFile)
		if err != nil {
			rep.NetworkNote = "strace output unreadable: " + err.Error()
		} else {
			rep.NetworkVerified = true
			rep.Network = CheckNetwork(string(b), t.APIServer)
		}
	}
	if o.OutDir != "" {
		_ = os.MkdirAll(o.OutDir, 0o755)
		tb, _ := json.MarshalIndent(rep.Calls, "", "  ")
		_ = os.WriteFile(filepath.Join(o.OutDir, t.Name+"-calls.json"), tb, 0o644)
		_ = os.WriteFile(filepath.Join(o.OutDir, t.Name+"-report.md"), []byte(rep.String()), 0o644)
	}
	return rep, nil
}

func contentText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteByte('\n')
		} else {
			j, _ := json.Marshal(c)
			b.Write(j)
		}
	}
	if res.StructuredContent != nil {
		j, _ := json.Marshal(res.StructuredContent)
		b.Write(j)
	}
	return b.String()
}

func sameArgs(a, b map[string]any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// CanaryForms are the encodings checked for each canary.
func CanaryForms(c string) map[string]string {
	return map[string]string{
		"raw":            c,
		"base64":         base64.StdEncoding.EncodeToString([]byte(c)),
		"base64-raw":     base64.RawStdEncoding.EncodeToString([]byte(c)),
		"base64-url-raw": base64.RawURLEncoding.EncodeToString([]byte(c)),
	}
}

// PartForms are the encodings checked for each part of a canary of several
// parts: every form of CanaryForms, the URL-escaped form, and the first and
// last 12 characters. The last two catch a cut that leaves a piece of a part
// (the cut of a table cell inside a quoted value, a line cut short). A part is
// at least 16 characters long, so 12 random characters never match by chance.
func PartForms(p string) map[string]string {
	forms := CanaryForms(p)
	forms["url-escaped"] = url.QueryEscape(p)
	if len(p) >= 16 {
		forms["prefix"] = p[:12]
		forms["suffix"] = p[len(p)-12:]
	}
	return forms
}

var partFormOrder = []string{"raw", "base64", "base64-raw", "base64-url-raw", "url-escaped", "prefix", "suffix"}

// find checks text for every canary of the fixture: the tokens and every
// part of the places with several parts.
func (f Fixture) find(text, where string) []Leak {
	return append(findCanaries(f.Canaries, text, where), findParts(f.Parts, text, where)...)
}

// findParts checks every part of every place by itself and reports the place
// and the part that leaked. A place reports each part at most once.
func findParts(parts map[string][]string, text, where string) []Leak {
	var out []Leak
	places := make([]string, 0, len(parts))
	for p := range parts {
		places = append(places, p)
	}
	sort.Strings(places)
	for _, p := range places {
		for i, part := range parts[p] {
			forms := PartForms(part)
			for _, k := range partFormOrder {
				if f, ok := forms[k]; ok && f != "" && strings.Contains(text, f) {
					out = append(out, Leak{Place: p, Part: i + 1, Form: k, Where: where})
					break
				}
			}
		}
	}
	return out
}

func findCanaries(canaries map[string]string, text, where string) []Leak {
	var out []Leak
	places := make([]string, 0, len(canaries))
	for p := range canaries {
		places = append(places, p)
	}
	sort.Strings(places)
	for _, p := range places {
		forms := CanaryForms(canaries[p])
		keys := []string{"raw", "base64", "base64-raw", "base64-url-raw"}
		for _, k := range keys {
			if strings.Contains(text, forms[k]) {
				out = append(out, Leak{Place: p, Form: k, Where: where})
				break
			}
		}
	}
	return out
}

// CheckAudit reads API server audit events for one user and returns how
// many there were and every request that is not a plain read.
func CheckAudit(log []byte, user string, since time.Time) (int, []string) {
	var n int
	var bad []string
	for _, line := range bytes.Split(log, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e struct {
			AuditEvent
			RequestReceivedTimestamp time.Time `json:"requestReceivedTimestamp"`
		}
		if err := json.Unmarshal(line, &e); err != nil || e.User.Username != user {
			continue
		}
		if !since.IsZero() && e.RequestReceivedTimestamp.Before(since) {
			continue
		}
		n++
		reason := ""
		switch {
		case e.Verb != "get" && e.Verb != "list":
			reason = "verb " + e.Verb
		case e.ObjectRef != nil && e.ObjectRef.Resource == "secrets":
			reason = "secrets"
		case e.ObjectRef != nil && e.ObjectRef.Subresource != "" && e.ObjectRef.Subresource != "log":
			reason = "subresource " + e.ObjectRef.Subresource
		}
		for _, seg := range strings.Split(strings.SplitN(e.RequestURI, "?", 2)[0], "/") {
			switch strings.ToLower(seg) {
			case "exec", "attach", "portforward", "proxy", "secrets":
				reason = "path segment " + seg
			}
		}
		if reason != "" {
			bad = append(bad, fmt.Sprintf("%s %s (%s)", e.Verb, e.RequestURI, reason))
		}
	}
	return n, bad
}

// CheckNetwork reads strace output and returns every listen() call and
// every inet connect() to a peer other than apiServer.
func CheckNetwork(trace, apiServer string) []string {
	var bad []string
	host, port, _ := strings.Cut(apiServer, ":")
	for _, line := range strings.Split(trace, "\n") {
		switch {
		case strings.Contains(line, "listen("):
			bad = append(bad, "listen: "+strings.TrimSpace(line))
		case strings.Contains(line, "connect(") && strings.Contains(line, "AF_INET"):
			if !(strings.Contains(line, "htons("+port+")") && strings.Contains(line, host)) {
				bad = append(bad, "connect: "+strings.TrimSpace(line))
			}
		}
	}
	return bad
}

// String is a Markdown report.
func (r *Report) String() string {
	var b strings.Builder
	status := "PASS"
	if !r.OK() {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "# Canary harness: %s: %s\n\n", r.Target, status)
	hostile := 0
	for _, c := range r.Calls {
		if c.Hostile {
			hostile++
		}
	}
	fmt.Fprintf(&b, "- tools: %s\n- calls: %d (%d hostile)\n- API server audit events for the target identity: %d\n", strings.Join(r.Tools, ", "), len(r.Calls), hostile, r.AuditEvents)
	section := func(title string, items []string) {
		fmt.Fprintf(&b, "\n## %s: %d\n\n", title, len(items))
		for _, i := range items {
			fmt.Fprintf(&b, "- %s\n", i)
		}
	}
	var leaks []string
	for _, l := range r.Leaks {
		part := ""
		if l.Part > 0 {
			part = fmt.Sprintf(" part %d", l.Part)
		}
		leaks = append(leaks, fmt.Sprintf("canary %q%s (%s form) in %s", l.Place, part, l.Form, l.Where))
	}
	section("Canary leaks", leaks)
	section("Requests other than get/list, or to forbidden paths", r.BadRequests)
	section("Normal calls that were not useful", r.Useless)
	if r.NetworkVerified {
		section("Listen calls and connections to other hosts", r.Network)
	} else {
		fmt.Fprintf(&b, "\n## Network\n\n%s\n", r.NetworkNote)
	}
	return b.String()
}
