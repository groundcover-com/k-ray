// Command hard is the free size test for the hard agent benchmarks. It makes
// no LLM call. For each task of a fixture set it runs a fixed list of
// commands twice, once with read-only kubectl and once with the kray CLI, and
// measures what a model would have to read: bytes of output (tokens are
// bytes / 4), whether the answer is still in the output, which planted secrets
// appear, and which values a debugger needs are missing from the kray output.
//
//	go run ./bench/hard -kubeconfig test/integration/out/kubeconfig -context kind-kray-bench
//
// The commands are in <set>/<task>/questions.json. The fixtures must be
// applied first (go run ./bench/agents -apply -scenarios <set>).
package main

import (
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
	"time"
)

type truth struct {
	Symptom string     `json:"symptom"`
	Must    [][]string `json:"must"`
}

type questions struct {
	Kubectl []string `json:"kubectl"`
	Kray    []string `json:"kray"`
	// Variants are extra command lists, measured the same way and shown as
	// extra rows (for example a plainer kubectl run, or kray with a filter).
	Variants map[string][]string `json:"variants"`
}

type entry struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	Task  string `json:"task"`
}

type side struct {
	cmds     int
	failed   []string // commands that exited non-zero
	bytes    int
	out      string
	missing  []string // must groups that do not match the output
	canaries []string
}

func main() {
	kubeconfig := flag.String("kubeconfig", "test/integration/out/kubeconfig", "kubeconfig of the benchmark cluster")
	kctx := flag.String("context", "kind-kray-bench", "kubeconfig context; every command uses it")
	sets := flag.String("sets", "bench/agents/hard/tokens,bench/agents/hard/secrets", "comma-separated fixture set directories")
	outPath := flag.String("out", "bench/HARD_RESULTS.md", "where to write the Markdown results")
	flag.Parse()
	if err := run(*kubeconfig, *kctx, strings.Split(*sets, ","), *outPath); err != nil {
		fmt.Fprintln(os.Stderr, "hard:", err)
		os.Exit(1)
	}
}

func run(kc, kctx string, sets []string, outPath string) error {
	kc, err := filepath.Abs(kc)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "hard")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	if b, err := exec.Command("go", "build", "-o", filepath.Join(tmp, "kray-real"), ".").CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, b)
	}
	realKubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return err
	}
	// Shims pin the cluster for every command, so a question file can say
	// "kubectl get pods -A" and never touch another context.
	shims := map[string]string{
		"kubectl": fmt.Sprintf("#!/bin/sh\nexec %q --kubeconfig %q --context %q \"$@\"\n", realKubectl, kc, kctx),
		"kray":    fmt.Sprintf("#!/bin/sh\nsub=$1; shift\nexec %q \"$sub\" --kubeconfig %q --context %q \"$@\"\n", filepath.Join(tmp, "kray-real"), kc, kctx),
	}
	for name, body := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			return err
		}
	}
	env := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+filepath.Join(tmp, "config"), "XDG_STATE_HOME="+filepath.Join(tmp, "state"))

	// The rtk side runs the kubectl command list through the rtk proxy, when
	// rtk is installed. Its kubectl shim calls rtk with the original PATH, so
	// rtk finds the real kubectl and not this shim.
	var rtkEnv []string
	if rtk, err := exec.LookPath("rtk"); err == nil {
		rbin := filepath.Join(tmp, "bin-rtk")
		if err := os.MkdirAll(rbin, 0o755); err != nil {
			return err
		}
		shim := fmt.Sprintf("#!/bin/sh\nPATH=%q exec %q kubectl \"$@\" --kubeconfig %q --context %q\n", os.Getenv("PATH"), rtk, kc, kctx)
		if err := os.WriteFile(filepath.Join(rbin, "kubectl"), []byte(shim), 0o755); err != nil {
			return err
		}
		rtkEnv = append(os.Environ(),
			"PATH="+rbin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"RTK_TELEMETRY_DISABLED=1",
			"XDG_CONFIG_HOME="+filepath.Join(tmp, "config"), "XDG_STATE_HOME="+filepath.Join(tmp, "state"), "XDG_DATA_HOME="+filepath.Join(tmp, "data"))
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# Hard benchmarks: output size, kubectl and kray\n\nMeasured %s on context `%s`. No LLM runs. Tokens are bytes / 4, an estimate. Each side runs the fixed command list in the task's `questions.json`.\n\n", time.Now().UTC().Format("2006-01-02"), kctx)
	md.WriteString("**Answer in output** is whether every `must` group of the task matches the output. **Canaries** are the distinct planted secrets in the output, checked plain, base64 and URL-encoded. **Keep missing** counts the values a debugger needs that kubectl showed and kray did not.\n\n")
	var details strings.Builder
	for _, set := range sets {
		name := filepath.Base(set)
		var canaries, keep []entry
		readJSON(filepath.Join(set, "canaries.json"), &canaries)
		readJSON(filepath.Join(set, "keep.json"), &keep)
		tasks, err := os.ReadDir(set)
		if err != nil {
			return err
		}
		fmt.Fprintf(&md, "## %s\n\n| Task | Side | Commands | Bytes | ~Tokens | Answer in output | Canaries | Keep missing |\n|---|---|---:|---:|---:|---|---:|---|\n", name)
		var ratios strings.Builder
		for _, t := range tasks {
			if !t.IsDir() {
				continue
			}
			var tr truth
			var q questions
			if !readJSON(filepath.Join(set, t.Name(), "truth.json"), &tr) || !readJSON(filepath.Join(set, t.Name(), "questions.json"), &q) {
				continue
			}
			k := measure(q.Kubectl, env, tr.Must, canaries)
			r := measure(q.Kray, env, tr.Must, canaries)
			type row struct {
				who string
				s   side
			}
			rows := []row{{"kubectl", k}, {"kray", r}}
			if rtkEnv != nil {
				rows = append(rows, row{"rtk", measure(q.Kubectl, rtkEnv, tr.Must, canaries)})
			}
			var vnames []string
			for n := range q.Variants {
				vnames = append(vnames, n)
			}
			sort.Strings(vnames)
			for _, n := range vnames {
				rows = append(rows, row{n, measure(q.Variants[n], env, tr.Must, canaries)})
			}
			var kept []string
			lostBy := map[string][]string{}
			for _, e := range keep {
				if e.Task != "" && e.Task != t.Name() {
					continue
				}
				if strings.Contains(k.out, e.Value) {
					kept = append(kept, e.ID)
					for _, x := range rows[1:] {
						if !strings.Contains(x.s.out, e.Value) {
							lostBy[x.who] = append(lostBy[x.who], e.ID)
						}
					}
				}
			}
			for _, row := range rows {
				miss := "-"
				if row.who != "kubectl" {
					miss = fmt.Sprintf("%d of %d", len(lostBy[row.who]), len(kept))
				}
				fmt.Fprintf(&md, "| %s | %s | %d | %d | %d | %s | %d | %s |\n", t.Name(), row.who, row.s.cmds, row.s.bytes, row.s.bytes/4, yesNo(len(row.s.missing) == 0), len(row.s.canaries), miss)
			}
			ratio := "n/a"
			if r.bytes > 0 {
				ratio = fmt.Sprintf("%.1fx", float64(k.bytes)/float64(r.bytes))
			}
			fmt.Fprintf(&ratios, "| %s | %d | %d | %s |\n", t.Name(), k.bytes/4, r.bytes/4, ratio)
			fmt.Fprintf(&details, "### %s / %s\n\n", name, t.Name())
			for _, row := range rows {
				fmt.Fprintf(&details, "- **%s:** canaries: %s. Must groups not matched: %s. Commands with a non-zero exit: %s. Keep values missing: %s.\n", row.who, list(row.s.canaries), list(row.s.missing), list(row.s.failed), list(lostBy[row.who]))
			}
			fmt.Fprintf(&details, "- **keep values kubectl showed:** %s.\n\n", list(kept))
		}
		fmt.Fprintf(&md, "\n| Task | kubectl ~tokens | kray ~tokens | kubectl / kray |\n|---|---:|---:|---:|\n%s\n", ratios.String())
	}
	md.WriteString("## Details\n\n" + details.String())
	if err := os.WriteFile(outPath, []byte(md.String()), 0o644); err != nil {
		return err
	}
	fmt.Print(md.String())
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "NO"
}

func list(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		fmt.Fprintf(os.Stderr, "hard: %s: %v\n", path, err)
		return false
	}
	return true
}

// measure runs the commands in order and totals what they print (stdout and
// stderr, as an agent would see them).
func measure(cmds []string, env []string, must [][]string, canaries []entry) side {
	var s side
	var all bytes.Buffer
	for _, c := range cmds {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "sh", "-c", c)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		cancel()
		s.cmds++
		if err != nil {
			s.failed = append(s.failed, c)
		}
		all.Write(out)
	}
	s.out = all.String()
	s.bytes = len(s.out)
	low := strings.ToLower(s.out)
	for _, group := range must {
		ok := false
		for _, alt := range group {
			if re, err := regexp.Compile("(?i)" + alt); err == nil && re.MatchString(low) {
				ok = true
				break
			}
		}
		if !ok {
			s.missing = append(s.missing, strings.Join(group, " | "))
		}
	}
	s.canaries = seenCanaries(all.Bytes(), canaries)
	sort.Strings(s.canaries)
	return s
}

// seenCanaries returns the ids of the canaries in the text: plain, base64
// (whole 3-byte groups from the start of the value) or URL-encoded.
func seenCanaries(text []byte, list []entry) []string {
	var ids []string
	for _, c := range list {
		forms := [][]byte{[]byte(c.Value)}
		if n := len(c.Value) / 3 * 3; n > 0 {
			forms = append(forms,
				[]byte(base64.StdEncoding.EncodeToString([]byte(c.Value[:n]))),
				[]byte(base64.URLEncoding.EncodeToString([]byte(c.Value[:n]))))
		}
		forms = append(forms, []byte(url.QueryEscape(c.Value)), []byte(url.PathEscape(c.Value)))
		for _, f := range forms {
			if len(f) > 0 && bytes.Contains(text, f) {
				ids = append(ids, c.ID)
				break
			}
		}
	}
	return ids
}
