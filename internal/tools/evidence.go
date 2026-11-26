package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	"github.com/groundcover-com/kray/internal/redact"
	"github.com/groundcover-com/kray/internal/shape"
)

// The evidence lines of a container: the values of its environment, its
// requests and limits, and its probes. An agent that sees a cause asks for
// them next (the limit that proves an OOM kill, the timeout of a probe, the
// address in an environment variable), so describe prints them when the
// object has a problem. A healthy object keeps the short form.

const (
	envValueMax = 80  // bytes of one value, after redaction
	envLineMax  = 400 // bytes of the entries of one env line
)

// evidence says how containersLine prints the evidence lines. A nil value
// keeps the short form: names only.
type evidence struct {
	red *redact.Redactor
	// hasUsage names the containers whose usage line (from the metrics API)
	// already states their requests and limits.
	hasUsage map[string]bool
}

// needsEvidence is the rule for when describe prints the evidence lines: the
// object has a problem or a likely cause, its state is unhealthy or warning,
// a container restarted, or (a workload) one of its pods has a problem. A
// healthy object keeps the short form.
func needsEvidence(problems, causes []string, state shape.State, restarted bool, badPod string) bool {
	return len(problems) > 0 || len(causes) > 0 || state == shape.Unhealthy || state == shape.Warning || restarted || badPod != ""
}

// envPairs redacts the environment entries of a container with the same
// engine that redacts an object: the entries are given to it as the objects
// they are in the API, so that a name that looks secret hides its value, and
// the value patterns, the embedded JSON and the entropy rules apply. A
// reference prints its source and never a value.
func envPairs(c corev1.Container, red *redact.Redactor) []string {
	// ref writes a reference. The final redaction pass of every result hides
	// the text after "NAME=" when NAME looks secret, and that would hide the
	// name of the Secret too: such a name is written with a space instead.
	ref := func(name, what string) string {
		if red.Sensitive(name) {
			return name + " (" + what + ")"
		}
		return name + "=(" + what + ")"
	}
	entries := make([]any, len(c.Env))
	for i, e := range c.Env {
		entries[i] = map[string]any{"name": e.Name, "value": e.Value}
	}
	red.Object(map[string]any{"env": entries})
	out := make([]string, 0, len(c.Env))
	for i, e := range c.Env {
		switch vf := e.ValueFrom; {
		case vf == nil:
			v, _ := entries[i].(map[string]any)["value"].(string)
			out = append(out, e.Name+"="+flatLine(cutBytes(v, envValueMax)))
		case vf.SecretKeyRef != nil:
			out = append(out, ref(e.Name, fmt.Sprintf("secret %s/%s", vf.SecretKeyRef.Name, vf.SecretKeyRef.Key)))
		case vf.ConfigMapKeyRef != nil:
			out = append(out, ref(e.Name, fmt.Sprintf("configmap %s/%s", vf.ConfigMapKeyRef.Name, vf.ConfigMapKeyRef.Key)))
		case vf.FieldRef != nil:
			out = append(out, ref(e.Name, "field "+vf.FieldRef.FieldPath))
		case vf.ResourceFieldRef != nil:
			out = append(out, ref(e.Name, "resource "+vf.ResourceFieldRef.Resource))
		default:
			out = append(out, ref(e.Name, "other source"))
		}
	}
	return out
}

func flatLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", `\n`)
}

// cutBytes cuts s to about n bytes at a character boundary and says how many
// bytes it left out.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return fmt.Sprintf("%s…(+%d bytes)", s[:i], len(s)-i)
}

// envEvidence is the env line, and the env from line, of one container.
func envEvidence(c corev1.Container, red *redact.Redactor) []string {
	var out []string
	if len(c.Env) > 0 {
		pairs := envPairs(c, red)
		var shown []string
		size := 0
		for i, p := range pairs {
			if size+len(p) > envLineMax && len(shown) > 0 {
				shown = append(shown, fmt.Sprintf("(%d more)", len(pairs)-i))
				break
			}
			shown = append(shown, p)
			size += len(p) + 2
		}
		out = append(out, "env: "+strings.Join(shown, ", "))
	}
	var from []string
	for _, e := range c.EnvFrom {
		switch {
		case e.SecretRef != nil:
			from = append(from, "secret "+e.SecretRef.Name)
		case e.ConfigMapRef != nil:
			from = append(from, "configmap "+e.ConfigMapRef.Name)
		}
	}
	if len(from) > 0 {
		out = append(out, "env from: "+strings.Join(from, ", "))
	}
	return out
}

// resourcesEvidence is one line: the requests and limits, or none.
func resourcesEvidence(c corev1.Container) string {
	part := func(name string, l corev1.ResourceList) string {
		var s []string
		for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
			if q, ok := l[r]; ok {
				s = append(s, fmt.Sprintf("%s %s", r, q.String()))
			}
		}
		if len(s) == 0 {
			return ""
		}
		return name + " " + strings.Join(s, ", ")
	}
	var parts []string
	for _, p := range []string{part("requests", c.Resources.Requests), part("limits", c.Resources.Limits)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "resources: none (no requests, no limits)"
	}
	return "resources: " + strings.Join(parts, "; ")
}

// probeEvidence is one line for each probe that is set. The timings print
// even when they are the defaults: they are the evidence for a timeout.
func probeEvidence(c corev1.Container, red *redact.Redactor) []string {
	var out []string
	for _, p := range []struct {
		name string
		p    *corev1.Probe
	}{{"liveness", c.LivenessProbe}, {"readiness", c.ReadinessProbe}, {"startup", c.StartupProbe}} {
		if p.p == nil {
			continue
		}
		pr := p.p
		timeout, period, fails := pr.TimeoutSeconds, pr.PeriodSeconds, pr.FailureThreshold
		// An unset timing is the default that the API server fills in.
		if timeout == 0 {
			timeout = 1
		}
		if period == 0 {
			period = 10
		}
		if fails == 0 {
			fails = 3
		}
		t := fmt.Sprintf("timeout %ds, period %ds, failures %d", timeout, period, fails)
		if pr.InitialDelaySeconds > 0 {
			t += fmt.Sprintf(", initial delay %ds", pr.InitialDelaySeconds)
		}
		if pr.SuccessThreshold > 1 {
			t += fmt.Sprintf(", successes %d", pr.SuccessThreshold)
		}
		out = append(out, fmt.Sprintf("%s: %s; %s", p.name, flatLine(probeHandler(pr.ProbeHandler, red)), t))
	}
	return out
}

func probeHandler(h corev1.ProbeHandler, red *redact.Redactor) string {
	switch {
	case h.HTTPGet != nil:
		s := "HTTP GET "
		if h.HTTPGet.Scheme == corev1.URISchemeHTTPS {
			s = "HTTPS GET "
		}
		return red.Text(fmt.Sprintf("%s%s on %s", s, h.HTTPGet.Path, h.HTTPGet.Port.String()))
	case h.TCPSocket != nil:
		return "TCP connect to " + h.TCPSocket.Port.String()
	case h.GRPC != nil:
		return fmt.Sprintf("gRPC health check on %d", h.GRPC.Port)
	case h.Exec != nil:
		// The command goes through the object rules, which hide the value of a
		// flag such as --password.
		cmd := make([]any, len(h.Exec.Command))
		for i, a := range h.Exec.Command {
			cmd[i] = a
		}
		red.Object(map[string]any{"command": cmd})
		parts := make([]string, len(cmd))
		for i, a := range cmd {
			parts[i], _ = a.(string)
		}
		return "exec " + cutCommand(red.Text(strings.Join(parts, " ")))
	}
	return "(no handler)"
}

// evidenceLines are the lines of one container, after the containers line.
// skipResources is true when a usage line already states its numbers.
func evidenceLines(c corev1.Container, red *redact.Redactor, skipResources bool) []string {
	out := envEvidence(c, red)
	if !skipResources {
		out = append(out, resourcesEvidence(c))
	}
	return append(out, probeEvidence(c, red)...)
}
