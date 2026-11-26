package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const partsDir = "parts"

// manifests returns every object of every manifest file of a set, by file.
func manifests(t *testing.T, dir string) map[string][]map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no manifests in %s: %v", dir, err)
	}
	out := map[string][]map[string]any{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(b), -1) {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			var obj map[string]any
			if err := yaml.UnmarshalStrict([]byte(doc), &obj); err != nil {
				t.Fatalf("%s, document %d: %v", f, i+1, err)
			}
			for _, k := range []string{"apiVersion", "kind", "metadata"} {
				if obj[k] == nil {
					t.Fatalf("%s, document %d: no %s", f, i+1, k)
				}
			}
			out[filepath.Base(f)] = append(out[filepath.Base(f)], obj)
		}
	}
	return out
}

// container returns the first container of the named Deployment.
func container(t *testing.T, objs map[string][]map[string]any, name string) map[string]any {
	t.Helper()
	for _, list := range objs {
		for _, o := range list {
			md, _ := o["metadata"].(map[string]any)
			if o["kind"] != "Deployment" || md["name"] != name {
				continue
			}
			spec := o["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			return spec["containers"].([]any)[0].(map[string]any)
		}
	}
	t.Fatalf("no Deployment %s", name)
	return nil
}

func strs(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func partsCanaries(t *testing.T) map[string]canaryEntry {
	t.Helper()
	set, err := loadSet(partsDir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]canaryEntry{}
	for _, c := range set.canaries {
		m[c.ID] = c
	}
	for _, id := range []string{"signer-key-block", "cache-password-phrase", "report-password-arg", "pricing-api-key"} {
		if _, ok := m[id]; !ok {
			t.Fatalf("no canary %s", id)
		}
	}
	return m
}

// needSh skips the test when the local shell cannot run the workload
// scripts: sh, base64 -d and tr.
func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if out, err := exec.Command("sh", "-c", "echo aGk= | base64 -d | tr 'A-Za-z' 'N-ZA-Mn-za-m'").Output(); err != nil || string(out) != "uv" {
		t.Skip("no base64 -d or tr")
	}
}

// local makes a workload script safe to run here: no long sleeps, and /tmp
// is a test directory.
func local(script, tmp string) string {
	script = regexp.MustCompile(`sleep [0-9]+`).ReplaceAllString(script, "sleep 0")
	return strings.ReplaceAll(script, "/tmp/", tmp+"/")
}

func TestPartsSetLoads(t *testing.T) {
	objs := manifests(t, partsDir)
	scen, err := load(partsDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(scen) < 3 {
		t.Fatalf("%d tasks, want at least 3", len(scen))
	}
	set := scen[0].set
	nsOK := map[string]bool{}
	for _, n := range set.cfg.Namespaces {
		if !strings.HasPrefix(n, "parts-") {
			t.Errorf("namespace %s has no parts- prefix", n)
		}
		nsOK[n] = true
	}
	applied := map[string]bool{}
	for _, st := range set.cfg.Steps {
		for _, f := range st.Apply {
			applied[f] = true
			if _, ok := objs[f]; !ok {
				t.Errorf("set.json applies %s, which is not a manifest of the set", f)
			}
		}
	}
	for f, list := range objs {
		if !applied[f] {
			t.Errorf("%s is never applied", f)
		}
		for _, o := range list {
			md := o["metadata"].(map[string]any)
			ns, _ := md["namespace"].(string)
			if o["kind"] == "Namespace" {
				ns = md["name"].(string)
			}
			if !nsOK[ns] {
				t.Errorf("%s: %s %s is in namespace %q, which set.json does not list", f, o["kind"], md["name"], ns)
			}
		}
	}
	for _, s := range scen {
		if !nsOK[s.namespace] {
			t.Errorf("%s: namespace %s is not in set.json", s.name, s.namespace)
		}
		if _, err := regexp.Compile(s.truth.ReadyWhen); err != nil || s.truth.ReadyWhen == "" {
			t.Errorf("%s: ready_when %q: %v", s.name, s.truth.ReadyWhen, err)
		}
		if len(s.truth.Must) == 0 || s.truth.Symptom == "" || s.truth.RootCause == "" {
			t.Errorf("%s: truth.json needs symptom, root_cause and must", s.name)
		}
		// The root cause passes its own grading.
		if ok, missing := grade(s.truth.RootCause, s.truth.Must); !ok {
			t.Errorf("%s: the root cause does not pass its must groups: %v", s.name, missing)
		}
	}
}

// TestPartsNotInSpecInClearText checks that the secrets the workloads print
// at run time are not in any manifest in any form the check looks for, so a
// run that reads only the spec does not count them. The --password argument
// is the exception: the spec is where it is planted.
func TestPartsNotInSpecInClearText(t *testing.T) {
	cs := partsCanaries(t)
	var all bytes.Buffer
	files, _ := filepath.Glob(filepath.Join(partsDir, "*.yaml"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		all.Write(b)
	}
	for _, id := range []string{"signer-key-block", "cache-password-phrase", "pricing-api-key"} {
		if ids, parts := seenCanaries(all.Bytes(), []canaryEntry{cs[id]}); len(ids) != 0 {
			t.Errorf("%s is in a manifest: %+v", id, parts)
		}
	}
	c := container(t, manifests(t, partsDir), "report-builder")
	args := strs(c["args"])
	want := strings.Join(cs["report-password-arg"].Parts, " ")
	found := false
	for i, a := range args {
		if a == "--password" && i+1 < len(args) && args[i+1] == want {
			found = true
		}
	}
	if !found {
		t.Errorf("report-builder args %q do not hold --password, then %q as one element", args, want)
	}
}

func TestSignerPrintsTheKeyBlockAndExitsZero(t *testing.T) {
	needSh(t)
	c := container(t, manifests(t, partsDir), "token-signer")
	cmd := strs(c["command"])
	if !startsWith(cmd, "sh", "-c") {
		t.Fatalf("command %q", cmd)
	}
	script := local(strs(c["args"])[0], t.TempDir())
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("the script must exit 0 (the fault is a clean exit): %v\n%s", err, out)
	}
	lines := strings.Split(string(out), "\n")
	parts := partsCanaries(t)["signer-key-block"].Parts
	want := append(append([]string{"-----BEGIN PRIVATE KEY-----"}, parts...), "-----END PRIVATE KEY-----")
	start := -1
	for i, l := range lines {
		if l == want[0] {
			start = i
		}
	}
	if start < 0 || start+len(want) > len(lines) {
		t.Fatalf("no key block in the output:\n%s", out)
	}
	for i, w := range want {
		if lines[start+i] != w {
			t.Fatalf("line %d of the block is %q, want %q", i+1, lines[start+i], w)
		}
	}
	for i, p := range parts[:len(parts)-1] {
		if len(p) != 64 {
			t.Errorf("body line %d has %d characters, want 64", i+1, len(p))
		}
	}
	if last := parts[len(parts)-1]; len(last) >= 64 {
		t.Errorf("the last line has %d characters, want a short line", len(last))
	}
	if !strings.Contains(string(out), "signer daemon started") {
		t.Errorf("no start line:\n%s", out)
	}
}

func TestQuoteProbeMessageCutsInsideThePassword(t *testing.T) {
	needSh(t)
	cs := partsCanaries(t)
	c := container(t, manifests(t, partsDir), "quote-api")
	tmp := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		env = append(env, m["name"].(string)+"="+m["value"].(string))
	}
	app := exec.Command("sh", "-c", local(strs(c["args"])[0], tmp))
	app.Env = env
	logOut, err := app.Output()
	if err != nil {
		t.Fatalf("app: %v\n%s", err, logOut)
	}

	// The "api key" log line is JSON and holds the key.
	var apiLine map[string]any
	for _, l := range strings.Split(string(logOut), "\n") {
		if strings.Contains(l, `"api key"`) {
			if err := json.Unmarshal([]byte(l), &apiLine); err != nil {
				t.Fatalf("api key line is not JSON: %v: %s", err, l)
			}
		}
	}
	if apiLine["api key"] != cs["pricing-api-key"].Value {
		t.Fatalf("api key line holds %v, want %s", apiLine["api key"], cs["pricing-api-key"].Value)
	}

	probe := strs(c["readinessProbe"].(map[string]any)["exec"].(map[string]any)["command"])
	if len(probe) != 3 || probe[0] != "sh" || probe[1] != "-c" {
		t.Fatalf("probe command %q", probe)
	}
	run := func(lookup string) (string, error) {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "nslookup"), []byte("#!/bin/sh\n"+lookup+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", local(probe[2], tmp))
		cmd.Env = append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}, env[1:]...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// The name resolves: the probe passes and prints nothing.
	if out, err := run("exit 0"); err != nil || out != "" {
		t.Fatalf("probe with a name that resolves: %v, %q", err, out)
	}
	// The name does not resolve: the probe fails with the JSON report.
	out, err := run("echo \"** server can't find $1: NXDOMAIN\"; exit 1")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("probe must exit 1, got %v: %s", err, out)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("probe output is not JSON: %v: %s", err, out)
	}
	if !strings.Contains(out, `"error":"lookup redis: no such host"`) {
		t.Errorf("probe output does not name the failed lookup: %s", out)
	}
	words := cs["cache-password-phrase"].Parts
	if !strings.Contains(out, `"password":"`+strings.Join(words, " ")+`"`) {
		t.Fatalf("probe output does not hold the quoted phrase: %s", out)
	}
	// The kubelet's event message is "Readiness probe failed: " and the
	// output. It must be longer than 300 bytes, the error must come before
	// byte 300, and byte 300 must fall inside the second word.
	msg := "Readiness probe failed: " + out
	if len(msg) <= 300 {
		t.Fatalf("message is %d bytes, want more than 300", len(msg))
	}
	if i := strings.Index(msg, "no such host"); i < 0 || i > 300 {
		t.Errorf("the error is at byte %d, want before 300", i)
	}
	w2 := strings.Index(msg, words[1])
	if w2 < 0 || w2 >= 300 || w2+len(words[1]) <= 300 {
		t.Fatalf("second word at bytes %d..%d, want byte 300 inside it", w2, w2+len(words[1]))
	}
	t.Logf("message %d bytes; byte 300 is %d bytes into the second word", len(msg), 300-w2)
}

func TestReportBuilderRejectsTheFlag(t *testing.T) {
	needSh(t)
	c := container(t, manifests(t, partsDir), "report-builder")
	cmd := strs(c["command"])
	if len(cmd) != 4 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("command %q", cmd)
	}
	script := local(cmd[2], t.TempDir())
	run := func(args []string) (string, int) {
		out, err := exec.Command("sh", append([]string{"-c", script, cmd[3]}, args...)...).CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}
	args := strs(c["args"])
	out, code := run(args)
	if code != 2 || !strings.Contains(out, "unknown flag: --worker-count") {
		t.Fatalf("exit %d, output %q; want exit 2 and unknown flag: --worker-count", code, out)
	}
	// The value of --password, with its spaces, is not taken for a flag, and
	// the right flag name makes the program start.
	fixed := append([]string(nil), args...)
	for i, a := range fixed {
		if a == "--worker-count=4" {
			fixed[i] = "--workers=4"
		}
	}
	if out, code := run(fixed); code != 0 || !strings.Contains(out, "report-builder listening") {
		t.Fatalf("with --workers=4: exit %d, output %q", code, out)
	}
}

func startsWith(got []string, want ...string) bool {
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
