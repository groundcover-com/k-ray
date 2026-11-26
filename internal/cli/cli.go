// Package cli is the command line. The six tool commands build the same JSON
// arguments as an MCP client and call the same tools.Runner, so both
// surfaces behave the same. init and doctor are for a person only; they are
// not MCP tools.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/groundcover-com/kray/internal/audit"
	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/config"
	"github.com/groundcover-com/kray/internal/kinds"
	"github.com/groundcover-com/kray/internal/kube"
	"github.com/groundcover-com/kray/internal/kube/readonly"
	"github.com/groundcover-com/kray/internal/mcpserver"
	"github.com/groundcover-com/kray/internal/setup"
	"github.com/groundcover-com/kray/internal/tools"
)

type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// Main runs the command line and returns the exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := env{stdin, stdout, stderr}
	if len(args) == 0 {
		e.usage()
		return 2
	}
	if code, ok := e.kubectl(args); ok {
		return code
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "serve", "mcp":
		err = e.serve(rest)
	case "list", "get", "describe", "logs", "events", "top":
		var code int
		code, err = e.tool(cmd, rest)
		if err == nil {
			return code
		}
	case "init":
		err = e.init(rest)
	case "doctor":
		var dangerous bool
		dangerous, err = e.doctor(rest)
		if err == nil && dangerous {
			return 1
		}
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, brand.Name, brand.Version)
		return 0
	case "help", "--help", "-h":
		e.usage()
		return 0
	default:
		fmt.Fprintln(stderr, errNotSupported)
		return 1
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "%s: %v\n", brand.Name, err)
		return 1
	}
	return 0
}

func (e env) usage() {
	n := brand.Name
	fmt.Fprintf(e.stderr, `%[1]s gives an AI agent read-only access to a Kubernetes cluster over MCP (stdio).

Server (for the agent's MCP config):
  %[1]s serve [--context C] [--kubeconfig P] [--config P] [--audit-log P]

The same six tools for a person:
  %[1]s list KIND [-n NS] [-A] [-l SELECTOR] [--match WORDS] [--limit N] [--all] [--full]
  %[1]s get KIND NAME [-n NS] [--full] [--jsonpath TEMPLATE]
  %[1]s describe [KIND] NAME [-n NS] [-A]  |  describe KIND -l SELECTOR [-n NS] [-A]
  %[1]s logs [POD | --workload KIND/NAME | -l SELECTOR [-A]] [-n NS] [-c CONTAINER] [--previous] [--since 10m] [--tail N] [--match WORDS] [--full]
  %[1]s events [-n NS] [-A] [--for KIND/NAME] [--reason R] [--since 1h] [--warnings] [--all] [--match WORDS] [--limit N]
  %[1]s top [pods|nodes] [-n NS] [-A] [-l SELECTOR] [--sort cpu|memory]

Output notes: a list of more than 8 shows rows for unhealthy objects only (--all for every row); logs of a
workload or selector merge the pods; events hide Normal events when there are warnings (--all). A result
with no "redactions: N" line had no redactions.
Every row of a list carries its cause on the same line, so the output can be filtered by name (for example
with grep). A named object is found in any namespace when you give no -n (get, describe, logs).
describe NAME finds an object in any namespace; when several match, it shows each.
--match WORDS keeps only the rows, event groups or log groups that hold one of the words (comma between
words, not case sensitive; no patterns). A large list shows shorter causes and counts the rows it does not
show by namespace; --full shows all.
describe shows the conditions that are not healthy, and the related objects that have a problem.

Setup (a person runs these; they are not available to the agent):
  %[1]s init [--context C] [--dry-run] [--yes] [--duration 8h] [--refresh] [--token-stdin]
  %[1]s doctor [--context C]

  %[1]s version
`, n)
	e.kubectlUsage()
}

type common struct {
	kubeconfig, context, configPath, auditLog string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.kubeconfig, "kubeconfig", "", "kubeconfig file (default: the init kubeconfig if present, else KUBECONFIG or ~/.kube/config)")
	fs.StringVar(&c.context, "context", "", "kubeconfig context (default: current context)")
	fs.StringVar(&c.configPath, "config", "", "config file (default: "+config.DefaultPath()+")")
	fs.StringVar(&c.auditLog, "audit-log", "", "audit log file (default: from the config file, else "+config.DefaultAuditPath()+")")
}

// parse allows flags before and after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newSession() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// runner loads the config, the read-only cluster, the reader and the audit
// log. It fails when the audit log cannot be opened (fail closed).
func (e env) runner(c common, surface string) (*tools.Runner, *readonly.Cluster, func(), error) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	ks, err := kinds.Default().With(cfg.CustomResources)
	if err != nil {
		return nil, nil, nil, err
	}
	cl, err := readonly.Load(readonly.Options{
		Kubeconfig: c.kubeconfig, Context: c.context,
		InitKubeconfig: config.InitKubeconfigPath(), StdinUnavailable: surface == "mcp",
	})
	if err != nil {
		return nil, nil, nil, err
	}
	reader, err := kube.New(cl)
	if err != nil {
		return nil, nil, nil, err
	}
	auditPath := c.auditLog
	if auditPath == "" {
		auditPath = cfg.AuditLog
	}
	logger, err := audit.Open(auditPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w (the server does not run without an audit log)", err)
	}
	r := &tools.Runner{
		Reader: reader, Kinds: ks, Context: cl.Context, Namespace: cl.Namespace, Identity: cl.Identity,
		Budget: cfg.BudgetBytes, FullBudget: cfg.FullBudgetBytes, ExtraKeys: cfg.Redaction.ExtraKeyNames,
		Audit: logger, Surface: surface, Session: newSession(), Timeout: 20 * time.Second,
	}
	return r, cl, func() { _ = logger.Close() }, nil
}

// stderrIsTerminal reports whether w is a terminal that a person reads. It is
// a variable so that a test can replace it.
var stderrIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// warnIdentityOnTerminal is the warning of a one-shot command. The line is for
// a person: an agent that captures stderr cannot act on it and would read it
// again on every call. serve always prints it (its stderr is the client log),
// and doctor reports the same fact.
func (e env) warnIdentityOnTerminal(cl *readonly.Cluster) {
	if stderrIsTerminal(e.stderr) {
		e.warnIdentity(cl)
	}
}

func (e env) warnIdentity(cl *readonly.Cluster) {
	if cl.Source == readonly.SourceInit {
		return
	}
	msg := fmt.Sprintf("%s: not a read-only identity; see `%s init`", brand.Name, brand.Name)
	if cl.ExecPlugin != "" {
		msg += fmt.Sprintf(" (credential plugin: %s)", cl.ExecPlugin)
	}
	fmt.Fprintln(e.stderr, msg)
}

func (e env) serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	var c common
	c.register(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	r, cl, done, err := e.runner(c, "mcp")
	if err != nil {
		return err
	}
	defer done()
	e.warnIdentity(cl)
	s, err := mcpserver.New(r, e.stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = mcpserver.Run(ctx, s)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, io.EOF)) {
		return nil
	}
	return err
}

func (e env) tool(name string, args []string) (int, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	var c common
	c.register(fs)
	ns := fs.String("namespace", "", "namespace")
	fs.StringVar(ns, "n", "", "namespace (short)")
	all := fs.Bool("all-namespaces", false, "all namespaces")
	fs.BoolVar(all, "A", false, "all namespaces (short)")
	sel := fs.String("selector", "", "label selector")
	fs.StringVar(sel, "l", "", "label selector (short)")
	full := fs.Bool("full", false, "larger budget, raw output")
	allRows := fs.Bool("all", false, "list: a row for every object; events: Normal events too")
	workload := fs.String("workload", "", "logs: all pods of a workload, KIND/NAME such as deployment/web")
	limit := fs.Int("limit", 0, "maximum rows or groups")
	container := fs.String("container", "", "container")
	fs.StringVar(container, "c", "", "container (short)")
	previous := fs.Bool("previous", false, "previous container instance")
	fs.BoolVar(previous, "p", false, "previous container instance (short)")
	since := fs.String("since", "", "duration, for example 10m or 2h")
	tail := fs.Int("tail", 0, "lines to read")
	forObj := fs.String("for", "", "events about KIND/NAME")
	reason := fs.String("reason", "", "events with this reason, for example Failed")
	warnings := fs.Bool("warnings", false, "warnings only")
	sortBy := fs.String("sort", "", "cpu or memory")
	jsonpath := fs.String("jsonpath", "", "get: one path of the object, such as '{.spec.replicas}'")
	match := fs.String("match", "", "list, events, logs: only rows or groups with one of these words (comma between words, not case sensitive)")
	pos, err := parse(fs, args)
	if err != nil {
		return 0, err
	}
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	var in any
	switch name {
	case "list":
		in = tools.ListInput{Kind: arg(0), Namespace: *ns, AllNamespaces: *all, LabelSelector: *sel, Limit: *limit, Full: *full, All: *allRows, Match: *match}
	case "get":
		in = tools.GetInput{Kind: arg(0), Name: arg(1), Namespace: *ns, Full: *full, JSONPath: *jsonpath}
	case "describe":
		in = tools.DescribeInput{Kind: arg(0), Name: arg(1), Namespace: *ns, LabelSelector: *sel, AllNamespaces: *all}
	case "logs":
		in = tools.LogsInput{Pod: arg(0), Workload: *workload, LabelSelector: *sel, AllNamespaces: *all, Namespace: *ns, Container: *container, Previous: *previous, Since: *since, Tail: *tail, Full: *full, Match: *match}
	case "events":
		ev := tools.EventsInput{All: *allRows, Reason: *reason, Namespace: *ns, AllNamespaces: *all, Since: *since, WarningsOnly: *warnings, Limit: *limit, Match: *match}
		if *forObj != "" {
			k, n, ok := strings.Cut(*forObj, "/")
			if !ok {
				return 0, fmt.Errorf("--for must be KIND/NAME")
			}
			ev.Kind, ev.Name = k, n
		}
		in = ev
	case "top":
		in = tools.TopInput{Resource: arg(0), Namespace: *ns, AllNamespaces: *all, LabelSelector: *sel, SortBy: *sortBy}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	r, cl, done, err := e.runner(c, "cli")
	if err != nil {
		return 0, err
	}
	defer done()
	e.warnIdentityOnTerminal(cl)
	res := r.Call(context.Background(), name, raw)
	fmt.Fprint(e.stdout, res.Text)
	if res.IsError {
		return 1, nil
	}
	return 0, nil
}

func (e env) init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	o := setup.InitOptions{Out: e.stdout, Err: e.stderr, In: e.stdin}
	fs.StringVar(&o.Kubeconfig, "kubeconfig", "", "the person's kubeconfig (default: KUBECONFIG or ~/.kube/config)")
	fs.StringVar(&o.Context, "context", "", "the person's context (default: current context)")
	fs.BoolVar(&o.DryRun, "dry-run", false, "print the manifests and stop; apply nothing")
	fs.BoolVar(&o.Yes, "yes", false, "apply without asking")
	fs.DurationVar(&o.Duration, "duration", 8*time.Hour, "token lifetime (the API server may shorten it; minimum 10m)")
	fs.BoolVar(&o.Refresh, "refresh", false, "only mint a new token for the existing identity")
	fs.BoolVar(&o.TokenStdin, "token-stdin", false, "read a token (from kubectl create token) on stdin; only write the kubeconfig")
	configPath := fs.String("config", "", "config file (for custom resources)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if o.Kinds, err = kinds.Default().With(cfg.CustomResources); err != nil {
		return err
	}
	o.Target = config.InitKubeconfigPath()
	return setup.Init(context.Background(), o)
}

func (e env) doctor(args []string) (bool, error) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	var c common
	c.register(fs)
	if _, err := parse(fs, args); err != nil {
		return false, err
	}
	return setup.Doctor(context.Background(), setup.DoctorOptions{
		Kubeconfig: c.kubeconfig, Context: c.context, InitKubeconfig: config.InitKubeconfigPath(), Out: e.stdout,
	})
}
