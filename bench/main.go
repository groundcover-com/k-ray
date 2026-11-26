// Command bench compares the size of kubectl output with the size of this
// server's output for the same objects, on a live cluster. It runs kubectl
// and the CLI of this repository; it is a development tool and is not part
// of the binary.
//
//	go run ./bench -kubeconfig test/integration/out/kubeconfig -out bench/RESULTS.md
//
// Tokens are estimated as bytes / 4. That is an estimate, not a tokenizer.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type item struct {
	what          string
	kubectl, ours []string
}

func main() {
	kubeconfig := flag.String("kubeconfig", "test/integration/out/kubeconfig", "kubeconfig of the cluster to measure")
	ns := flag.String("namespace", "", "namespace of the sample workload (default: the first namespace that starts with fixture-, else kube-system)")
	outPath := flag.String("out", "bench/RESULTS.md", "where to write the Markdown results")
	flag.Parse()
	if err := run(*kubeconfig, *ns, *outPath); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func kubectl(kc string, args ...string) ([]byte, error) {
	cmd := exec.Command("kubectl", append([]string{"--kubeconfig", kc}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}

func first(kc string, args ...string) (string, error) {
	out, err := kubectl(kc, append(args, "-o", "jsonpath={.items[0].metadata.name}")...)
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no object for %v", args)
	}
	return string(out), nil
}

func run(kc, ns, outPath string) error {
	if ns == "" {
		out, err := kubectl(kc, "get", "namespaces", "-o", "json")
		if err != nil {
			return err
		}
		var l struct {
			Items []struct {
				Metadata struct{ Name string }
			}
		}
		_ = json.Unmarshal(out, &l)
		ns = "kube-system"
		for _, i := range l.Items {
			if strings.HasPrefix(i.Metadata.Name, "fixture-") {
				ns = i.Metadata.Name
			}
		}
	}
	deploy, err := first(kc, "get", "deployments", "-n", ns)
	if err != nil {
		return err
	}
	pod, err := first(kc, "get", "pods", "-n", ns)
	if err != nil {
		return err
	}
	node, err := first(kc, "get", "nodes")
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "bench")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "server")
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, b)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "XDG_CONFIG_HOME=" + filepath.Join(tmp, "c"), "XDG_STATE_HOME=" + filepath.Join(tmp, "s"), "KUBECONFIG=" + kc}

	items := []item{
		{"list pods, all namespaces", []string{"get", "pods", "-A", "-o", "wide"}, []string{"list", "pods", "-A"}},
		{"list pods, all namespaces, as YAML", []string{"get", "pods", "-A", "-o", "yaml"}, []string{"list", "pods", "-A"}},
		{"get deployment " + deploy, []string{"get", "deployment", deploy, "-n", ns, "-o", "yaml"}, []string{"get", "deployment", deploy, "-n", ns}},
		{"describe deployment " + deploy, []string{"describe", "deployment", deploy, "-n", ns}, []string{"describe", "deployment", deploy, "-n", ns}},
		{"describe pod " + pod, []string{"describe", "pod", pod, "-n", ns}, []string{"describe", "pod", pod, "-n", ns}},
		{"logs " + pod + " (tail 2000)", []string{"logs", pod, "-n", ns, "--tail=2000", "--all-containers"}, []string{"logs", pod, "-n", ns, "--tail", "2000"}},
		{"events, all namespaces", []string{"get", "events", "-A"}, []string{"events", "-A"}},
		{"get node " + node, []string{"get", "node", node, "-o", "yaml"}, []string{"get", "node", node}},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Output size: kubectl and this server\n\nMeasured %s on the cluster in `%s`, namespace `%s`.\nTokens are estimated as bytes / 4.\n\n", time.Now().UTC().Format("2006-01-02"), kc, ns)
	b.WriteString("| What | kubectl bytes | kubectl ~tokens | server bytes | server ~tokens | ratio |\n|---|---:|---:|---:|---:|---:|\n")
	for _, it := range items {
		k, err := kubectl(kc, it.kubectl...)
		if err != nil {
			return err
		}
		cmd := exec.Command(bin, it.ours...)
		cmd.Env = env
		o, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(it.ours, " "), err, o)
		}
		ratio := "-"
		if len(o) > 0 {
			ratio = fmt.Sprintf("%.1fx", float64(len(k))/float64(len(o)))
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %s |\n", it.what, len(k), len(k)/4, len(o), len(o)/4, ratio)
	}
	b.WriteString("\nThe server output includes its header, the data fence, notes and redaction markers. kubectl output is shown with its default flags (kubectl hides managedFields by default).\n")
	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Print(b.String())
	return nil
}
