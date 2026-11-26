package tools

import (
	"strings"
)

// Hints are the follow-up calls that a result suggests. They render in the
// caller's syntax: MCP arguments for an agent, flags for the command line,
// so a person can paste a hint after the command name. A hint never has the
// command name in it: tool output names no product.
//
// A hint must work as written. It names the namespace when that is not the
// default one, and it asks for previous logs only when a container restarted.

func (r *Runner) cli() bool { return r.Surface == "cli" }

// verb is how the caller runs a tool: an agent calls it, a person runs it.
func (r *Runner) verb() string {
	if r.cli() {
		return "run"
	}
	return "call"
}

// argName renders an argument name: "tail" or "--tail".
func (r *Runner) argName(name string) string {
	if r.cli() {
		return "--" + name
	}
	return name
}

// arg renders one argument: "full=true" or "--full"; "tail=500" or "--tail 500".
func (r *Runner) arg(name, value string) string {
	if !r.cli() {
		return name + "=" + value
	}
	if value == "true" {
		return "--" + name
	}
	return "--" + name + " " + value
}

// nsPart names the namespace unless it is the default one.
func (r *Runner) nsPart(ns string) string {
	def := r.Namespace
	if def == "" {
		def = "default"
	}
	if ns == "" || ns == def {
		return ""
	}
	if r.cli() {
		return " -n " + ns
	}
	return " namespace=" + ns
}

func (r *Runner) hintDescribe(kind, name, ns string) string {
	return "describe " + kind + " " + name + r.nsPart(ns)
}

func (r *Runner) hintGet(kind, name, ns string) string {
	return "get " + kind + " " + name + r.nsPart(ns)
}

func (r *Runner) hintLogs(pod, ns, container string, previous, full bool) string {
	var b strings.Builder
	if r.cli() {
		b.WriteString("logs " + pod + r.nsPart(ns))
		if container != "" {
			b.WriteString(" -c " + container)
		}
	} else {
		b.WriteString("logs pod=" + pod + r.nsPart(ns))
		if container != "" {
			b.WriteString(" container=" + container)
		}
	}
	if previous {
		b.WriteString(" " + r.arg("previous", "true"))
	}
	if full {
		b.WriteString(" " + r.arg("full", "true"))
	}
	return b.String()
}

func (r *Runner) hintEvents(kind, name, ns string) string {
	if r.cli() {
		return "events" + r.nsPart(ns) + " --for " + kind + "/" + name
	}
	return "events" + r.nsPart(ns) + " kind=" + kind + " name=" + name
}

// selectorName is the label selector argument: "labelSelector" or "--selector".
func (r *Runner) selectorName() string {
	if r.cli() {
		return "--selector"
	}
	return "labelSelector"
}

// eventsFilter names the arguments that narrow events to one object.
func (r *Runner) eventsFilter() string {
	if r.cli() {
		return "--for KIND/NAME"
	}
	return "kind and name"
}
