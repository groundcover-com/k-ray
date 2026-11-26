// Command agents is the agent benchmark: it gives the same debugging task to
// an AI agent with kubectl and to an AI agent with this server, on a cluster
// with known faults, and measures what each agent needs to reach the root
// cause.
//
// It is a development tool. It starts the `claude` CLI in headless mode, so
// it needs that CLI and an account; the server binary itself makes no LLM
// calls. See bench/agents/README.md.
//
//	go run ./bench/agents -kubeconfig test/integration/out/kubeconfig
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/groundcover-com/kray/internal/brand"
)

// canary is planted in the noisy-logs scenario. The benchmark checks whether
// an agent ever sees it.
const canary = "sk_live_KrayBenchCanary7f3a9c21d8e4b6a0"

type truth struct {
	Symptom   string     `json:"symptom"`
	ReadyWhen string     `json:"ready_when"`
	RootCause string     `json:"root_cause"`
	Must      [][]string `json:"must"`
	// Namespace and ReadyCmd are for tasks in a fixture set. Namespace is
	// where the fault lives (the agent is never told). ReadyCmd is the kubectl
	// command whose output ReadyWhen is matched against (default: get pods in
	// the namespace).
	Namespace string `json:"namespace"`
	ReadyCmd  string `json:"ready_cmd"`
	// Conversation makes the task a long session: each entry is one question,
	// asked as a new prompt in the same session, with its own answer check.
	Conversation []convTurn `json:"conversation"`
}

// canaryEntry is one planted fake secret. Value is the secret part only (for
// a connection string, the password). A secret of several words or lines has
// Parts instead: one part for each word or line, for example each body line
// of a private key block or each word of a quoted password. A run saw the
// canary when any one part appears, because a leak of one part is a leak.
type canaryEntry struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Value string   `json:"value,omitempty"`
	Parts []string `json:"parts,omitempty"`
	Where string   `json:"where"`
}

// minPartLen is the shortest part a canary can have. A shorter part could
// match ordinary text by chance.
const minPartLen = 12

// check returns an error when the entry has no secret or a part is too short.
func (c canaryEntry) check() error {
	if c.ID == "" {
		return fmt.Errorf("a canary has no id")
	}
	if c.Value == "" && len(c.Parts) == 0 {
		return fmt.Errorf("canary %s has no value and no parts", c.ID)
	}
	if c.Value != "" && len(c.Parts) > 0 {
		return fmt.Errorf("canary %s has a value and parts; give one of them", c.ID)
	}
	for i, p := range c.Parts {
		if len(p) < minPartLen {
			return fmt.Errorf("canary %s: part %d is shorter than %d characters", c.ID, i+1, minPartLen)
		}
	}
	return nil
}

// partSeen is one part of a canary of several parts that appears in a
// transcript, and the first form in which it appears.
type partSeen struct {
	ID   string `json:"id"`
	Part int    `json:"part"` // 1-based
	Form string `json:"form"` // plain, base64, base64url, url-query or url-path
}

// fixtureSet is a directory with one shared fixture and several tasks: it has
// a set.json. Steps are applied in order; WaitRollout commands run after a
// step is applied.
type fixtureSet struct {
	dir string
	cfg struct {
		Namespaces []string `json:"namespaces"`
		Steps      []struct {
			Apply       []string `json:"apply"`
			WaitRollout []string `json:"wait_rollout"`
		} `json:"steps"`
	}
	canaries []canaryEntry
}

type scenario struct {
	name      string
	namespace string
	truth     truth
	set       *fixtureSet // nil for the small scenarios, one namespace each
}

// legacyCanaries is the one secret of the small scenarios.
var legacyCanaries = []canaryEntry{{ID: "noisy-logs-api-key", Kind: "API key", Value: canary}}

// convTurn is one question of a long conversation.
type convTurn struct {
	Ask  string     `json:"ask"`
	Must [][]string `json:"must"`
}

// turnStat is what one question of a conversation cost.
type turnStat struct {
	Correct      bool    `json:"correct"`
	Calls        int     `json:"tool_calls"`
	ToolTokens   int     `json:"tool_tokens"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Context      int     `json:"context_tokens"` // largest single-turn input in this question
	Seconds      float64 `json:"seconds"`
	CostUSD      float64 `json:"cost_usd"`
}

type result struct {
	Scenario     string     `json:"scenario"`
	Arm          string     `json:"arm"`
	Run          int        `json:"run"`
	Model        string     `json:"model"`
	Correct      bool       `json:"correct"`
	Missing      []string   `json:"missing,omitempty"`
	Answer       string     `json:"answer"`
	ToolCalls    int        `json:"tool_calls"`
	ToolBytes    int        `json:"tool_bytes"`    // bytes of tool output the model read
	ToolTokens   int        `json:"tool_tokens"`   // tool_bytes / 4
	InputTokens  int        `json:"input_tokens"`  // all input tokens, cache included
	OutputTokens int        `json:"output_tokens"` //
	Turns        int        `json:"turns"`
	Seconds      float64    `json:"seconds"`
	CostUSD      float64    `json:"cost_usd"`
	SawCanary    bool       `json:"saw_canary"`
	Canaries     []string   `json:"canaries_seen,omitempty"`     // ids of the distinct canaries in the transcript
	CanaryParts  []partSeen `json:"canary_parts_seen,omitempty"` // the parts seen of the canaries of several parts
	PeakContext  int        `json:"peak_context_tokens"`         // largest single-turn input, cache included
	Denied       int        `json:"denied_tool_calls"`
	Commands     []string   `json:"commands"`
	UserTurns    int        `json:"user_turns,omitempty"`    // questions asked, for a conversation
	TurnsCorrect int        `json:"turns_correct,omitempty"` // questions answered correctly
	PerTurn      []turnStat `json:"per_turn,omitempty"`
	session      string     // session id, to resume a conversation
	Error        string     `json:"error,omitempty"`
}

var filters = []string{"grep", "egrep", "head", "tail", "sed", "awk", "cut", "sort", "uniq", "wc", "tr", "jq", "cat", "echo", "xargs", "column", "cd", "printf", "true", "test", "seq", "date", "tee", "sleep"}

// kubectlReadOnly is the start of the kubectl shim of the kubectl and rtk
// arms. It finds the command word behind any flags and stops everything
// that is not a read.
const kubectlReadOnly = `verb=""; sub=""; skip=0
for a in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$a" in
    -n|--namespace|--context|--kubeconfig|--cluster|--user|-s|--server|--request-timeout|--as|--as-group|--token|-v|--v|--cache-dir|-l|--selector|-o|--output|-c|--container|--tail|--since|--field-selector|--sort-by) skip=1 ;;
    -*) ;;
    *) if [ -z "$verb" ]; then verb="$a"; elif [ -z "$sub" ]; then sub="$a"; fi ;;
  esac
done
ok=0
case "$verb" in
  get|describe|logs|top|events|api-resources|api-versions|explain|version|"") ok=1 ;;
  rollout) case "$sub" in status|history) ok=1 ;; esac ;;
  auth) case "$sub" in can-i|whoami) ok=1 ;; esac ;;
  config) case "$sub" in view|current-context|get-contexts) ok=1 ;; esac ;;
esac
for a in "$@"; do case "$a" in --raw|--raw=*) ok=0 ;; esac; done
if [ "$ok" != 1 ]; then echo "error: only read commands are allowed here (kubectl $verb $sub is not)" >&2; exit 1; fi
`

// kubectlNoSecrets is added on a real cluster: no Secret objects.
const kubectlNoSecrets = `for a in "$@"; do
  case "$a" in secret|secrets|secret/*|secrets/*|*,secret|*,secrets|secret,*|secrets,*|*,secret,*|*,secrets,*) echo "error: Secret objects are not available here" >&2; exit 1 ;; esac
done
`

func main() {
	kubeconfig := flag.String("kubeconfig", "test/integration/out/kubeconfig", "kubeconfig of the benchmark cluster")
	dir := flag.String("scenarios", "bench/agents/scenarios", "scenario directory: the small scenarios (default), or a fixture set such as bench/agents/hard/tokens or bench/agents/hard/secrets")
	only := flag.String("only", "", "comma-separated scenario names (default: all)")
	arms := flag.String("arms", "kubectl,cli,mcp", "comma-separated arms: kubectl, rtk (kubectl through the rtk proxy), cli (this tool as a CLI), mcp (this tool as an MCP server)")
	runs := flag.Int("runs", 1, "runs per scenario and arm")
	model := flag.String("model", "sonnet", "model for the agent")
	parallel := flag.Int("parallel", 3, "agents at the same time")
	out := flag.String("out", "bench/agents/results", "output directory")
	apply := flag.Bool("apply", false, "apply the scenarios and wait for the faults, then exit")
	del := flag.Bool("delete", false, "delete the namespaces of the scenarios, then exit")
	real := flag.Bool("real", false, "a real cluster: refuse -apply and -delete, tell the agent to only read, and keep Secret objects out of reach")
	lean := flag.Bool("lean", false, "a lean agent: no user settings, plugins or skills, and only the shell tool (or only the MCP tools), so the base context is small")
	flag.Parse()
	realCluster = *real
	leanAgent = *lean
	if realCluster && (*apply || *del) {
		must(fmt.Errorf("-real never applies or deletes anything"))
	}

	kc, err := filepath.Abs(*kubeconfig)
	must(err)
	scenarios, err := load(*dir, *only)
	must(err)
	if *del {
		must(deleteAll(kc, scenarios))
		return
	}
	if *apply {
		must(applyAll(kc, *dir, scenarios))
		return
	}
	bin, err := buildServer()
	must(err)
	must(os.MkdirAll(*out, 0o755))

	type job struct {
		s   scenario
		arm string
		run int
	}
	var jobs []job
	for _, s := range scenarios {
		for _, arm := range strings.Split(*arms, ",") {
			for r := 1; r <= *runs; r++ {
				jobs = append(jobs, job{s, arm, r})
			}
		}
	}
	results := make([]result, len(jobs))
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := runAgent(j.s, j.arm, j.run, *model, kc, bin, *out)
			results[i] = r
			mark := "WRONG"
			if r.Correct {
				mark = "ok"
			}
			fmt.Fprintf(os.Stderr, "%-18s %-8s run %d: %-5s %2d calls %6d tool tokens %5.0fs %s\n", r.Scenario, r.Arm, r.Run, mark, r.ToolCalls, r.ToolTokens, r.Seconds, r.Error)
		}(i, j)
	}
	wg.Wait()

	b, _ := json.MarshalIndent(results, "", "  ")
	must(os.WriteFile(filepath.Join(*out, "results.json"), b, 0o644))
	report := render(results, *model)
	must(os.WriteFile(filepath.Join(*out, "RESULTS.md"), []byte(report), 0o644))
	fmt.Print(report)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "agents:", err)
		os.Exit(1)
	}
}

func load(dir, only string) ([]scenario, error) {
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n != "" {
			want[n] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	set, err := loadSet(dir)
	if err != nil {
		return nil, err
	}
	nsRE := regexp.MustCompile(`(?m)^metadata: \{name: (bench-[a-z0-9-]+)\}`)
	var out []scenario
	for _, e := range entries {
		if !e.IsDir() || (len(want) > 0 && !want[e.Name()]) {
			continue
		}
		var t truth
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "truth.json"))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if set != nil {
			if t.Namespace == "" {
				return nil, fmt.Errorf("%s: truth.json needs a namespace", e.Name())
			}
			out = append(out, scenario{name: e.Name(), namespace: t.Namespace, truth: t, set: set})
			continue
		}
		m, err := os.ReadFile(filepath.Join(dir, e.Name(), "manifest.yaml"))
		if err != nil {
			return nil, err
		}
		ns := nsRE.FindSubmatch(m)
		if ns == nil {
			return nil, fmt.Errorf("%s: no bench- namespace in the manifest", e.Name())
		}
		out = append(out, scenario{name: e.Name(), namespace: string(ns[1]), truth: t})
	}
	return out, nil
}

// loadSet reads dir/set.json and dir/canaries.json. It returns nil when dir
// is a directory of small scenarios.
func loadSet(dir string) (*fixtureSet, error) {
	b, err := os.ReadFile(filepath.Join(dir, "set.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &fixtureSet{dir: dir}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return nil, fmt.Errorf("set.json: %w", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "canaries.json")); err == nil {
		if err := json.Unmarshal(b, &s.canaries); err != nil {
			return nil, fmt.Errorf("canaries.json: %w", err)
		}
		for _, c := range s.canaries {
			if err := c.check(); err != nil {
				return nil, fmt.Errorf("canaries.json: %w", err)
			}
		}
	}
	return s, nil
}

func kubectl(kc string, args ...string) (string, error) {
	cmd := exec.Command("kubectl", append([]string{"--kubeconfig", kc}, args...)...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// applyAll applies every scenario and waits until each fault shows. It is
// safe to run again.
func applyAll(kc, dir string, scenarios []scenario) error {
	var set *fixtureSet
	if len(scenarios) > 0 {
		set = scenarios[0].set
	}
	if set != nil {
		for i, step := range set.cfg.Steps {
			for _, f := range step.Apply {
				if o, err := kubectl(kc, "apply", "-f", filepath.Join(set.dir, f)); err != nil {
					return fmt.Errorf("step %d, %s: %w: %s", i+1, f, err, o)
				}
			}
			for _, w := range step.WaitRollout {
				args := append([]string{"rollout", "status"}, strings.Fields(w)...)
				args = append(args, "--timeout=300s")
				if o, err := kubectl(kc, args...); err != nil {
					return fmt.Errorf("step %d, rollout %s: %w: %s", i+1, w, err, o)
				}
			}
			fmt.Printf("step %d applied: %s\n", i+1, strings.Join(step.Apply, ", "))
		}
	} else {
		for _, s := range scenarios {
			if o, err := kubectl(kc, "apply", "-f", filepath.Join(dir, s.name, "manifest.yaml")); err != nil {
				return fmt.Errorf("%s: %w: %s", s.name, err, o)
			}
		}
	}
	deadline := time.Now().Add(10 * time.Minute)
	for _, s := range scenarios {
		re := regexp.MustCompile(s.truth.ReadyWhen)
		readyArgs := []string{"get", "pods", "-n", s.namespace, "--no-headers"}
		if s.truth.ReadyCmd != "" {
			readyArgs = strings.Fields(s.truth.ReadyCmd)
		}
		for {
			o, _ := kubectl(kc, readyArgs...)
			if loc := re.FindString(o); loc != "" {
				fmt.Printf("%-18s ready: %s\n", s.name, strings.Join(strings.Fields(loc), " "))
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s: fault did not show (%s):\n%s", s.name, s.truth.ReadyWhen, o)
			}
			time.Sleep(5 * time.Second)
		}
	}
	return nil
}

// deleteAll removes the namespaces of the scenarios, without waiting.
func deleteAll(kc string, scenarios []scenario) error {
	seen := map[string]bool{}
	var ns []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			ns = append(ns, n)
		}
	}
	for _, s := range scenarios {
		if s.set != nil {
			for _, n := range s.set.cfg.Namespaces {
				add(n)
			}
		} else {
			add(s.namespace)
		}
	}
	if len(ns) == 0 {
		return nil
	}
	o, err := kubectl(kc, append([]string{"delete", "namespace", "--ignore-not-found", "--wait=false"}, ns...)...)
	fmt.Print(o)
	return err
}

func buildServer() (string, error) {
	dir, err := os.MkdirTemp("", "agents-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, brand.Name)
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, b)
	}
	return bin, nil
}

// realCluster is set by -real. The fixtures are not ours there, so the
// harness never changes the cluster and the task says to only read.
var realCluster bool

// leanAgent is set by -lean. The default run inherits the settings of the
// person who runs it, which can add tens of thousands of tokens to every turn.
var leanAgent bool

// prompt is the same task for every arm; only the line that names the tool
// differs.
func prompt(s scenario, arm string) string {
	tool := map[string]string{
		"kubectl": "Your tool is the `kubectl` CLI, with read commands only.",
		// The model knows kubectl but not this tool, so the task gives its
		// usage in one line, as a team's agent instructions would.
		"cli": "Your tool is the `" + brand.Name + "` CLI, a read-only Kubernetes tool. It takes the kubectl read commands: type `" + brand.Name + "` in place of `kubectl`, for example `" + brand.Name + " get pods -A`, `" + brand.Name + " describe pod NAME -n NS`, `" + brand.Name + " logs deploy/NAME -n NS`, `" + brand.Name + " logs -l app=NAME -n NS`. `" + brand.Name + " describe NAME` finds a workload in any namespace and names the cause.",
		// The rtk arm is kubectl through the rtk proxy. The agent types
		// kubectl, as it does when the rtk hook rewrites its commands.
		"rtk": "Your tool is the `kubectl` CLI, with read commands only.",
		"mcp": "Your tools are the k8s MCP tools: list, get, describe, logs, events, top.",
	}[arm]
	if realCluster {
		tool += " This is a real cluster: only read it, and do not read Secret objects."
	}
	if conv := s.truth.Conversation; len(conv) > 0 {
		return fmt.Sprintf("You are the on-call engineer for a Kubernetes cluster. %s This is a long working session: I ask several questions, one after the other. For each question, look at the cluster as much as you need, then answer in a few sentences with the facts that prove your answer. You cannot change the cluster; only read it.\n\nFirst question: %s", tool, conv[0].Ask)
	}
	return fmt.Sprintf(`You are the on-call engineer for a Kubernetes cluster. %s

%s Find the root cause. Use as few steps as you need, and stop as soon as you know the root cause. You cannot change the cluster; only diagnose.

Answer in this form and nothing else:
ROOT CAUSE: <one or two sentences that name the exact cause>
EVIDENCE: <the facts from the cluster that prove it>`, s.truth.Symptom, tool)
}

// runAgent runs one headless agent and reads its event stream.
func runAgent(s scenario, arm string, run int, model, kc, bin, outDir string) result {
	r := result{Scenario: s.name, Arm: arm, Run: run, Model: model}
	work, err := os.MkdirTemp("", "agent-run")
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer os.RemoveAll(work)

	args := []string{"-p", prompt(s, arm), "--output-format", "stream-json", "--verbose", "--model", model, "--max-turns", "30", "--strict-mcp-config"}
	if leanAgent {
		tools := "Bash"
		if arm == "mcp" {
			tools = ""
		}
		args = append(args, "--setting-sources", "", "--disable-slash-commands", "--tools", tools)
	}
	mcp := `{"mcpServers":{}}`
	var allowed []string
	shimDir := ""
	if arm == "kubectl" || arm == "rtk" {
		// A kubectl shim guards the cluster: it lets only read commands
		// through, with flags in any order, so the agent can be allowed to
		// run kubectl as it likes. A prefix rule such as "kubectl get" does
		// not match "kubectl -n NS get", and the agent then waits for an
		// approval that never comes.
		real, err := exec.LookPath("kubectl")
		if err != nil {
			r.Error = "kubectl is not installed"
			return r
		}
		run := fmt.Sprintf("exec %q \"$@\"", real)
		if arm == "rtk" {
			// rtk runs with the original PATH, so it finds the real
			// kubectl and not this shim.
			rtk, err := exec.LookPath("rtk")
			if err != nil {
				r.Error = "rtk is not installed"
				return r
			}
			run = fmt.Sprintf("PATH=%q exec %q kubectl \"$@\"", os.Getenv("PATH"), rtk)
		}
		secrets := ""
		if realCluster {
			secrets = kubectlNoSecrets
		}
		shimDir = filepath.Join(work, "kubectlbin")
		_ = os.MkdirAll(shimDir, 0o755)
		_ = os.WriteFile(filepath.Join(shimDir, "kubectl"), []byte("#!/bin/sh\n"+kubectlReadOnly+secrets+run+"\n"), 0o755)
	}
	switch arm {
	case "kubectl", "rtk":
		allowed = append(allowed, "Bash(kubectl:*)")
		for _, f := range filters {
			if realCluster && f == "xargs" {
				continue // xargs can start a program by its full path, around the shim
			}
			allowed = append(allowed, "Bash("+f+":*)")
		}
	case "cli":
		allowed = append(allowed, "Bash("+brand.Name+":*)")
		for _, f := range filters {
			allowed = append(allowed, "Bash("+f+":*)")
		}
	case "mcp":
		cfg := map[string]any{"mcpServers": map[string]any{"k8s": map[string]any{
			"command": bin, "args": []string{"serve"},
			"env": map[string]string{"KUBECONFIG": kc, "XDG_CONFIG_HOME": filepath.Join(work, "config"), "XDG_STATE_HOME": filepath.Join(work, "state")},
		}}}
		b, _ := json.Marshal(cfg)
		mcp = string(b)
		allowed = append(allowed, "mcp__k8s")
	default:
		r.Error = "unknown arm " + arm
		return r
	}
	mcpFile := filepath.Join(work, "mcp.json")
	_ = os.WriteFile(mcpFile, []byte(mcp), 0o600)
	args = append(args, "--mcp-config", mcpFile, "--allowedTools")
	args = append(args, allowed...)
	// Tools that would let an agent go around its arm.
	args = append(args, "--disallowedTools", "WebFetch", "WebSearch", "Task", "Agent", "Write", "Edit", "NotebookEdit")
	if realCluster {
		for _, k := range []string{"secret", "secrets"} {
			args = append(args, "Bash(kubectl get "+k+":*)", "Bash(kubectl describe "+k+":*)")
		}
	}

	pathPrefix := ""
	if shimDir != "" {
		pathPrefix = shimDir + string(os.PathListSeparator)
	}
	env := append(os.Environ(), "KUBECONFIG="+kc,
		"PATH="+pathPrefix+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RTK_TELEMETRY_DISABLED=1",
		"XDG_CONFIG_HOME="+filepath.Join(work, "config"), "XDG_STATE_HOME="+filepath.Join(work, "state"),
		// Load MCP tools at the start, so no arm spends a call to look tools up.
		"ENABLE_TOOL_SEARCH=false")
	var transcript bytes.Buffer
	// runOne runs one headless prompt and returns what that prompt cost.
	runOne := func(args []string) result {
		var pr result
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claude", args...)
		cmd.Dir = work
		cmd.Env = env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			pr.Error = err.Error()
			return pr
		}
		start := time.Now()
		if err := cmd.Start(); err != nil {
			pr.Error = err.Error()
			return pr
		}
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			line := sc.Bytes()
			transcript.Write(line)
			transcript.WriteByte('\n')
			readEvent(line, &pr)
		}
		waitErr := cmd.Wait()
		if pr.Seconds == 0 {
			pr.Seconds = time.Since(start).Seconds()
		}
		if waitErr != nil && pr.Answer == "" {
			pr.Error = strings.TrimSpace(waitErr.Error() + " " + lastLine(stderr.String()))
		}
		return pr
	}

	if conv := s.truth.Conversation; len(conv) > 0 {
		// A long conversation: every question is a new prompt in the same
		// session, so the context grows as it does in real work.
		r.Correct = true
		for i, turn := range conv {
			a := append([]string(nil), args...)
			if i > 0 {
				a[1] = turn.Ask
				a = append(a, "--resume", r.session)
			}
			pr := runOne(a)
			if pr.session != "" {
				r.session = pr.session
			}
			ok, missing := grade(pr.Answer, turn.Must)
			r.PerTurn = append(r.PerTurn, turnStat{
				Correct: ok, Calls: pr.ToolCalls, ToolTokens: pr.ToolBytes / 4, InputTokens: pr.InputTokens,
				OutputTokens: pr.OutputTokens, Context: pr.PeakContext, Seconds: pr.Seconds, CostUSD: pr.CostUSD,
			})
			r.UserTurns++
			if ok {
				r.TurnsCorrect++
			} else {
				r.Correct = false
				r.Missing = append(r.Missing, fmt.Sprintf("q%d: %s", i+1, strings.Join(missing, "; ")))
			}
			r.Answer += fmt.Sprintf("[Q%d] %s\n", i+1, pr.Answer)
			r.ToolCalls += pr.ToolCalls
			r.ToolBytes += pr.ToolBytes
			r.InputTokens += pr.InputTokens
			r.OutputTokens += pr.OutputTokens
			r.Turns += pr.Turns
			r.Seconds += pr.Seconds
			r.CostUSD += pr.CostUSD
			r.Denied += pr.Denied
			r.Commands = append(r.Commands, pr.Commands...)
			if pr.PeakContext > r.PeakContext {
				r.PeakContext = pr.PeakContext
			}
			if pr.Error != "" || r.session == "" {
				r.Error = strings.TrimSpace(fmt.Sprintf("q%d: %s", i+1, pr.Error))
				break
			}
		}
	} else {
		pr := runOne(args)
		pr.Scenario, pr.Arm, pr.Run, pr.Model = r.Scenario, r.Arm, r.Run, r.Model
		r = pr
		r.Correct, r.Missing = grade(r.Answer, s.truth.Must)
	}
	_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("%s-%s-%d.jsonl", s.name, arm, run)), transcript.Bytes(), 0o644)
	r.ToolTokens = r.ToolBytes / 4
	canaries := legacyCanaries
	if s.set != nil {
		canaries = s.set.canaries
	}
	r.Canaries, r.CanaryParts = seenCanaries(transcript.Bytes(), canaries)
	r.SawCanary = len(r.Canaries) > 0
	return r
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// readEvent updates the result from one stream-json line.
func readEvent(line []byte, r *result) {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
			Usage   struct {
				Input       int `json:"input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Result     string  `json:"result"`
		NumTurns   int     `json:"num_turns"`
		DurationMS float64 `json:"duration_ms"`
		Cost       float64 `json:"total_cost_usd"`
		Usage      struct {
			Input       int `json:"input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			Output      int `json:"output_tokens"`
		} `json:"usage"`
		PermissionDenials []json.RawMessage `json:"permission_denials"`
		SessionID         string            `json:"session_id"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.SessionID != "" {
		r.session = ev.SessionID
	}
	switch ev.Type {
	case "assistant", "user":
		// One turn sends the whole context again; the largest turn is how big the context got.
		if c := ev.Message.Usage.Input + ev.Message.Usage.CacheCreate + ev.Message.Usage.CacheRead; c > r.PeakContext {
			r.PeakContext = c
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				r.ToolCalls++
				r.Commands = append(r.Commands, describeCall(b.Name, b.Input))
			case "tool_result":
				r.ToolBytes += resultBytes(b.Content)
			}
		}
	case "result":
		r.Answer = ev.Result
		r.Turns = ev.NumTurns
		r.Seconds = ev.DurationMS / 1000
		r.CostUSD = ev.Cost
		r.InputTokens = ev.Usage.Input + ev.Usage.CacheCreate + ev.Usage.CacheRead
		r.OutputTokens = ev.Usage.Output
		r.Denied = len(ev.PermissionDenials)
	}
}

func describeCall(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	if c, ok := m["command"].(string); ok {
		return c
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{strings.TrimPrefix(name, "mcp__k8s__")}
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// resultBytes is the size of the text the model read from one tool result.
func resultBytes(content json.RawMessage) int {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return len(s)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	n := 0
	if json.Unmarshal(content, &blocks) == nil {
		for _, b := range blocks {
			n += len(b.Text)
		}
	}
	return n
}

// grade checks the answer against the scenario's required facts: every group
// must match, with any one of its alternatives.
func grade(answer string, mustGroups [][]string) (bool, []string) {
	low := strings.ToLower(answer)
	var missing []string
	for _, group := range mustGroups {
		ok := false
		for _, alt := range group {
			if re, err := regexp.Compile("(?i)" + alt); err == nil && re.MatchString(low) {
				ok = true
				break
			}
		}
		if !ok {
			missing = append(missing, strings.Join(group, " | "))
		}
	}
	return answer != "" && len(missing) == 0, missing
}

type agg struct {
	n, correct, calls, toolTokens, canary, denied int
	seconds, cost                                 float64
	input, output, turns, peak, distinct          int
	ids                                           map[string]bool
	parts                                         map[string]map[int]bool // id -> parts seen
}

func render(results []result, model string) string {
	byArm := map[string]*agg{}
	byCell := map[string]*agg{}
	var scen, arms []string
	seenS, seenA := map[string]bool{}, map[string]bool{}
	for _, r := range results {
		for _, key := range []string{r.Arm, r.Scenario + "|" + r.Arm} {
			m := byArm
			if strings.Contains(key, "|") {
				m = byCell
			}
			a := m[key]
			if a == nil {
				a = &agg{ids: map[string]bool{}, parts: map[string]map[int]bool{}}
				m[key] = a
			}
			a.n++
			if r.Correct {
				a.correct++
			}
			a.calls += r.ToolCalls
			a.toolTokens += r.ToolTokens
			a.seconds += r.Seconds
			a.cost += r.CostUSD
			a.input += r.InputTokens
			a.output += r.OutputTokens
			a.turns += r.Turns
			a.peak += r.PeakContext
			a.denied += r.Denied
			a.distinct += len(r.Canaries)
			for _, id := range r.Canaries {
				a.ids[id] = true
			}
			for _, p := range r.CanaryParts {
				if a.parts[p.ID] == nil {
					a.parts[p.ID] = map[int]bool{}
				}
				a.parts[p.ID][p.Part] = true
			}
			if r.SawCanary {
				a.canary++
			}
		}
		if !seenS[r.Scenario] {
			seenS[r.Scenario] = true
			scen = append(scen, r.Scenario)
		}
		if !seenA[r.Arm] {
			seenA[r.Arm] = true
			arms = append(arms, r.Arm)
		}
	}
	idList := func(a *agg) string {
		var ids []string
		for id := range a.ids {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			return "none"
		}
		for i, id := range ids {
			ids[i] = id + partList(a.parts[id])
		}
		return strings.Join(ids, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent benchmark\n\nMeasured %s. Model: `%s`. Runs: %d.\n\n", time.Now().UTC().Format("2006-01-02"), model, len(results))
	b.WriteString("Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form. A secret of several words or lines counts when any one part appears, and the table names the parts seen (part 1 is the first word or line).\n\n")
	b.WriteString("## Totals per arm\n\n| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, arm := range arms {
		a := byArm[arm]
		n := float64(a.n)
		fmt.Fprintf(&b, "| %s | %d of %d | %.1f | %.0f | %.0f | %.0f | %.1f | %.0f | %.0f | $%.3f | %.1f | %d |\n", arm, a.correct, a.n, float64(a.calls)/n, float64(a.toolTokens)/n, float64(a.input)/n, float64(a.output)/n, float64(a.turns)/n, float64(a.peak)/n, a.seconds/n, a.cost/n, float64(a.distinct)/n, a.canary)
	}
	b.WriteString("\n## Per scenario\n\n| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |\n|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, s := range scen {
		for _, arm := range arms {
			a := byCell[s+"|"+arm]
			if a == nil {
				continue
			}
			n := float64(a.n)
			fmt.Fprintf(&b, "| %s | %s | %d of %d | %.1f | %.0f | %.0f | %.0f | %.1f | %.0f | %.0f | %s |\n", s, arm, a.correct, a.n, float64(a.calls)/n, float64(a.toolTokens)/n, float64(a.input)/n, float64(a.output)/n, float64(a.turns)/n, float64(a.peak)/n, a.seconds/n, idList(a))
		}
	}
	b.WriteString("\n## Wrong answers\n\n")
	wrong := 0
	for _, r := range results {
		if !r.Correct {
			wrong++
			fmt.Fprintf(&b, "- **%s, %s, run %d:** missing %s. %s\n", r.Scenario, r.Arm, r.Run, strings.Join(r.Missing, "; "), r.Error)
		}
	}
	if wrong == 0 {
		b.WriteString("None.\n")
	}
	return b.String()
}

// partList is " (part 2)" or " (parts 1, 4)" for the parts seen of one
// canary, or "" for a canary of one token.
func partList(seen map[int]bool) string {
	if len(seen) == 0 {
		return ""
	}
	var n []int
	for p := range seen {
		n = append(n, p)
	}
	sort.Ints(n)
	s := make([]string, len(n))
	for i, p := range n {
		s[i] = fmt.Sprint(p)
	}
	if len(n) == 1 {
		return " (part " + s[0] + ")"
	}
	return " (parts " + strings.Join(s, ", ") + ")"
}

// canaryForms returns the forms of one secret value that the check looks for,
// with their names: as it is, in base64, and URL-encoded. The base64 form
// covers only whole 3-byte groups from the start of the value, so it does not
// depend on what follows the value in the encoded text.
func canaryForms(v string) [][2]string {
	forms := [][2]string{{"plain", v}}
	if n := len(v) / 3 * 3; n > 0 {
		forms = append(forms,
			[2]string{"base64", base64.StdEncoding.EncodeToString([]byte(v[:n]))},
			[2]string{"base64url", base64.URLEncoding.EncodeToString([]byte(v[:n]))})
	}
	return append(forms, [2]string{"url-query", url.QueryEscape(v)}, [2]string{"url-path", url.PathEscape(v)})
}

// firstForm returns the name of the first form of v in the transcript, or "".
func firstForm(transcript []byte, v string) string {
	for _, f := range canaryForms(v) {
		if f[1] != "" && bytes.Contains(transcript, []byte(f[1])) {
			return f[0]
		}
	}
	return ""
}

// seenCanaries returns the ids of the distinct canaries that appear in the
// transcript, and the parts seen of the canaries of several parts. A canary
// of one token is seen when its value appears in any form of canaryForms. A
// canary of several parts is seen when any one part appears; each part is
// checked by itself, so a key block cut after its last line or a phrase cut
// after its first word still counts.
func seenCanaries(transcript []byte, list []canaryEntry) ([]string, []partSeen) {
	var ids []string
	var parts []partSeen
	for _, c := range list {
		if len(c.Parts) == 0 {
			if firstForm(transcript, c.Value) != "" {
				ids = append(ids, c.ID)
			}
			continue
		}
		seen := false
		for i, p := range c.Parts {
			if f := firstForm(transcript, p); f != "" {
				parts = append(parts, partSeen{ID: c.ID, Part: i + 1, Form: f})
				seen = true
			}
		}
		if seen {
			ids = append(ids, c.ID)
		}
	}
	return ids, parts
}
