package cli

// This file is a translation layer. It reads the kubectl read commands that
// an agent or a person already knows (get, describe, logs, events, top,
// rollout status) and turns them into calls to the existing six tools. It
// never runs kubectl and never talks to the cluster itself: every call goes
// through tools.Runner, so every guard of the tools applies.
// Anything that would change the cluster or open a session gets one fixed
// line and no cluster request.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/groundcover-com/kray/internal/brand"
	"github.com/groundcover-com/kray/internal/kinds"
)

// cliError is a message for the person. It is printed as is, without a
// product name.
type cliError string

func (e cliError) Error() string { return string(e) }

const errNotSupported = cliError("This command is not supported.")

// call is one tool call: the tool name and its JSON arguments.
type call struct {
	Tool string
	Args map[string]any
}

func (c call) json() []byte {
	b, _ := json.Marshal(c.Args)
	return b
}

type flagClass int

const (
	fBool flagClass = iota
	fValue
)

type flagSpec struct {
	canon string
	class flagClass
}

// flagSpecs maps every spelling to the canonical flag name and its class.
var flagSpecs = map[string]flagSpec{
	"n": {"namespace", fValue}, "namespace": {"namespace", fValue},
	"A": {"all-namespaces", fBool}, "all-namespaces": {"all-namespaces", fBool},
	"l": {"selector", fValue}, "selector": {"selector", fValue},
	"c": {"container", fValue}, "container": {"container", fValue},
	"p": {"previous", fBool}, "previous": {"previous", fBool},
	"o": {"output", fValue}, "output": {"output", fValue},
	"f": {"follow", fBool}, "follow": {"follow", fBool},
	"w": {"watch", fBool}, "watch": {"watch", fBool},
	"h": {"help", fBool}, "help": {"help", fBool},

	"context": {"context", fValue}, "kubeconfig": {"kubeconfig", fValue},
	"config": {"config", fValue}, "audit-log": {"audit-log", fValue},
	"tail": {"tail", fValue}, "since": {"since", fValue}, "since-time": {"since-time", fValue},
	"for": {"for", fValue}, "reason": {"reason", fValue}, "types": {"types", fValue}, "limit": {"limit", fValue},
	"sort": {"sort", fValue}, "sort-by": {"sort-by", fValue},
	"field-selector": {"field-selector", fValue}, "workload": {"workload", fValue},
	"match": {"match", fValue},
	"full":  {"full", fBool}, "warnings": {"warnings", fBool},
	"all":            {"all", fBool},
	"all-containers": {"all-containers", fBool},

	// Accepted and ignored: they only change kubectl formatting or plumbing.
	"no-headers": {"no-headers", fBool}, "show-labels": {"show-labels", fBool},
	"timestamps": {"timestamps", fBool}, "prefix": {"prefix", fBool},
	"ignore-not-found": {"ignore-not-found", fBool}, "show-kind": {"show-kind", fBool},
	"containers":       {"containers", fBool},
	"max-log-requests": {"max-log-requests", fValue}, "request-timeout": {"request-timeout", fValue},
	"timeout": {"timeout", fValue},
}

var ignoredFlags = map[string]bool{
	"no-headers": true, "show-labels": true, "timestamps": true, "prefix": true,
	"ignore-not-found": true, "show-kind": true, "containers": true, "sort-by": true,
	"max-log-requests": true, "request-timeout": true, "timeout": true,
}

// flagsFor lists the flags that change a call, per command. Ignored flags
// are allowed everywhere.
var flagsFor = map[string][]string{
	"get":      {"namespace", "all-namespaces", "selector", "output", "full", "limit", "field-selector", "all", "reason", "match"},
	"describe": {"namespace", "all-namespaces", "selector"},
	"logs":     {"namespace", "all-namespaces", "selector", "container", "previous", "since", "tail", "full", "workload", "match"},
	"events":   {"namespace", "all-namespaces", "for", "reason", "types", "since", "warnings", "limit", "output", "all", "match"},
	"top":      {"namespace", "all-namespaces", "selector", "sort", "sort-by"},
	"rollout":  {"namespace"},
}

var connFlags = []string{"context", "kubeconfig", "config", "audit-log", "help"}

// refused are the kubectl commands that change the cluster, open a session
// or read something kray does not offer.
var refused = map[string]bool{
	"apply": true, "create": true, "delete": true, "edit": true, "patch": true,
	"replace": true, "scale": true, "autoscale": true, "set": true, "label": true,
	"annotate": true, "taint": true, "cordon": true, "uncordon": true, "drain": true,
	"exec": true, "attach": true, "port-forward": true, "proxy": true, "cp": true,
	"run": true, "expose": true, "debug": true, "certificate": true, "auth": true,
	"config": true, "cluster-info": true, "explain": true, "api-resources": true,
	"api-versions": true, "wait": true, "diff": true, "kustomize": true,
	"completion": true, "plugin": true, "options": true, "alpha": true, "convert": true,
}

var supported = map[string]bool{"get": true, "describe": true, "logs": true, "events": true, "top": true, "rollout": true}

// notKubectl are the words that stay with the original command switch.
var notKubectl = map[string]bool{
	"serve": true, "mcp": true, "init": true, "doctor": true, "version": true,
	"--version": true, "-v": true, "help": true, "--help": true, "-h": true, "list": true,
}

type parsed struct {
	flags   map[string]string
	pos     []string
	unknown []string
}

func (p *parsed) has(name string) bool { _, ok := p.flags[name]; return ok }

// isTrue reports a bool flag that was set and not set to false.
func (p *parsed) isTrue(name string) bool {
	v, ok := p.flags[name]
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err != nil || b
}

// lex splits argv into flags and positional words. Flags may come anywhere.
// An unknown flag is recorded, not rejected, so that the command word is
// still found.
func lex(args []string) (*parsed, error) {
	p := &parsed{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "--":
			p.pos = append(p.pos, args[i+1:]...)
			return p, nil
		case strings.HasPrefix(tok, "--"):
			name, val, hasVal := strings.Cut(tok[2:], "=")
			spec, ok := flagSpecs[name]
			if !ok || len(name) < 2 {
				p.unknown = append(p.unknown, "--"+name)
				continue
			}
			if spec.class == fBool {
				if !hasVal {
					val = "true"
				}
			} else if !hasVal {
				if i+1 >= len(args) {
					return nil, cliError("--" + name + " needs a value.")
				}
				i++
				val = args[i]
			}
			p.flags[spec.canon] = val
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			rest := tok[1:]
			for rest != "" {
				letter := rest[:1]
				rest = rest[1:]
				spec, ok := flagSpecs[letter]
				if !ok {
					p.unknown = append(p.unknown, "-"+letter)
					break
				}
				if spec.class == fBool {
					p.flags[spec.canon] = "true"
					continue
				}
				val := strings.TrimPrefix(rest, "=")
				if rest == "" {
					if i+1 >= len(args) {
						return nil, cliError("-" + letter + " needs a value.")
					}
					i++
					val = args[i]
				}
				p.flags[spec.canon] = val
				break
			}
		default:
			p.pos = append(p.pos, tok)
		}
	}
	return p, nil
}

type plan struct {
	conn  common
	calls []call
	help  bool
}

// translate turns kubectl style arguments into tool calls. A returned
// cliError is a message for the person; errNotSupported is the fixed line.
func translate(args []string) (*plan, error) {
	if len(args) > 0 && args[0] == "kubectl" {
		args = args[1:]
	}
	p, err := lex(args)
	if err != nil {
		return nil, err
	}
	if len(p.pos) == 0 {
		return nil, errNotSupported
	}
	verb := p.pos[0]
	if !supported[verb] {
		return nil, errNotSupported
	}
	if p.isTrue("help") {
		return &plan{help: true}, nil
	}
	if len(p.unknown) > 0 {
		return nil, cliError("The flag " + p.unknown[0] + " is not supported.")
	}
	if err := checkFlags(verb, p); err != nil {
		return nil, err
	}
	pl := &plan{conn: common{
		kubeconfig: p.flags["kubeconfig"], context: p.flags["context"],
		configPath: p.flags["config"], auditLog: p.flags["audit-log"],
	}}
	t := translator{p: p, ks: kinds.Default()}
	switch verb {
	case "get":
		pl.calls, err = t.get()
	case "describe":
		pl.calls, err = t.describe()
	case "logs":
		pl.calls, err = t.logs()
	case "events":
		pl.calls, err = t.events()
	case "top":
		pl.calls, err = t.top()
	case "rollout":
		pl.calls, err = t.rollout()
	}
	if err != nil {
		return nil, err
	}
	return pl, nil
}

func checkFlags(verb string, p *parsed) error {
	if p.isTrue("follow") {
		if verb == "logs" {
			return cliError("-f/--follow is not supported: there is no streaming. Use --since or --tail.")
		}
		return cliError("-f is not supported.")
	}
	if p.isTrue("watch") {
		return cliError("-w/--watch is not supported: there is no streaming. Run the command again.")
	}
	if p.has("since-time") {
		return cliError("--since-time is not supported. Use --since with a duration, for example --since=10m.")
	}
	if p.isTrue("all-containers") {
		return cliError("--all-containers is not supported. Use -c to name one container.")
	}
	allowed := map[string]bool{}
	for _, f := range flagsFor[verb] {
		allowed[f] = true
	}
	for _, f := range connFlags {
		allowed[f] = true
	}
	for name := range p.flags {
		switch {
		case ignoredFlags[name], allowed[name]:
		case name == "follow", name == "watch", name == "all-containers", name == "since-time":
			// set to false: nothing to do
		default:
			return cliError("The flag --" + name + " is not supported for " + verb + ".")
		}
	}
	return nil
}

type translator struct {
	p  *parsed
	ks *kinds.Set
}

func (t translator) args() []string { return t.p.pos[1:] }

func (t translator) base(c map[string]any) map[string]any {
	if v := t.p.flags["namespace"]; v != "" {
		c["namespace"] = v
	}
	return c
}

func (t translator) multi(c map[string]any) map[string]any {
	t.base(c)
	if t.p.isTrue("all-namespaces") {
		c["allNamespaces"] = true
	}
	if t.p.isTrue("all") {
		c["all"] = true
	}
	return c
}

func (t translator) isEvents(kind string) bool {
	k, err := t.ks.Lookup(kind)
	return err == nil && k.Resource() == "events"
}

// kindName splits KIND/NAME. ok is false when there is no slash.
func kindName(s string) (kind, name string, ok bool) {
	kind, name, ok = strings.Cut(s, "/")
	return
}

const severalNames = cliError("Several names are not supported. Call once for each name.")

func (t translator) get() ([]call, error) {
	pos := t.args()
	if len(pos) == 0 {
		return nil, cliError("get needs a resource kind, for example: get pods.")
	}
	if len(pos) > 2 {
		return nil, severalNames
	}
	kind, name := pos[0], ""
	if k, n, ok := kindName(kind); ok {
		if len(pos) > 1 {
			return nil, severalNames
		}
		if k == "" || n == "" || strings.Contains(k, ",") {
			return nil, cliError("Use KIND/NAME with one kind and one name.")
		}
		kind, name = k, n
	} else if len(pos) == 2 {
		name = pos[1]
	}
	if name != "" {
		if strings.Contains(kind, ",") {
			return nil, cliError("A name cannot be used with several kinds.")
		}
		if strings.EqualFold(kind, "all") {
			return nil, cliError("get all does not take a name.")
		}
		if t.p.has("selector") {
			return nil, cliError("A name and -l cannot be used together.")
		}
		if t.p.isTrue("all-namespaces") {
			return nil, cliError("A name cannot be used with -A.")
		}
		if t.p.has("field-selector") {
			return nil, cliError("--field-selector is not supported with a name.")
		}
		if t.p.has("match") {
			return nil, cliError("--match is for a list: get KIND --match WORDS.")
		}
		if err := t.outputOK(); err != nil {
			return nil, err
		}
		a := t.base(map[string]any{"kind": kind, "name": name})
		if t.p.isTrue("full") {
			a["full"] = true
		}
		if tpl, ok := t.jsonpath(); ok {
			a["jsonpath"] = tpl
		}
		return []call{{"get", a}}, nil
	}
	if err := t.outputOK(); err != nil {
		return nil, err
	}
	if _, ok := t.jsonpath(); ok {
		return nil, cliError("-o jsonpath is for one object: name it (get KIND NAME -o jsonpath=...), or narrow a list with --match.")
	}
	var list []string
	for _, k := range strings.Split(kind, ",") {
		k = strings.TrimSpace(k)
		switch {
		case k == "":
			return nil, cliError("A kind is empty in the list of kinds.")
		case strings.EqualFold(k, "all"):
			list = append(list, "pods", "services", "deployments", "replicasets", "statefulsets", "daemonsets", "jobs", "cronjobs")
		default:
			list = append(list, k)
		}
	}
	var calls []call
	for _, k := range list {
		if t.isEvents(k) {
			if t.p.has("selector") {
				return nil, cliError("-l is not supported for events. Use --field-selector involvedObject.name=NAME.")
			}
			a, err := t.eventArgs(t.p.flags["field-selector"])
			if err != nil {
				return nil, err
			}
			calls = append(calls, call{"events", a})
			continue
		}
		if t.p.has("field-selector") {
			return nil, cliError("--field-selector is not supported except for events.")
		}
		if t.p.has("reason") {
			return nil, cliError("--reason is for events: get events --reason NAME.")
		}
		calls = append(calls, call{"list", t.listArgs(k)})
	}
	return calls, nil
}

func (t translator) listArgs(kind string) map[string]any {
	a := t.multi(map[string]any{"kind": kind})
	if v := t.p.flags["selector"]; v != "" {
		a["labelSelector"] = v
	}
	if v := t.p.flags["limit"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n != 0 {
			a["limit"] = n
		}
	}
	if v := t.p.flags["match"]; v != "" {
		a["match"] = v
	}
	if t.p.isTrue("full") {
		a["full"] = true
	}
	return a
}

// outputOK accepts the -o values that kray can honor. wide, yaml and json
// only change how kubectl prints; there is one compact form here.
func (t translator) outputOK() error {
	v, ok := t.p.flags["output"]
	if !ok {
		return nil
	}
	base, _, _ := strings.Cut(v, "=")
	switch strings.ToLower(base) {
	case "wide", "yaml", "json":
		return nil
	case "jsonpath":
		if strings.TrimSpace(strings.TrimPrefix(v, base)) == "=" || !strings.Contains(v, "=") {
			return cliError("-o jsonpath needs a template: -o jsonpath='{.spec.replicas}'.")
		}
		return nil
	case "jsonpath-file":
		return cliError("-o jsonpath-file is not supported. Use -o jsonpath='{...}'.")
	case "custom-columns", "custom-columns-file":
		return cliError("-o custom-columns is not supported. Use -o wide.")
	case "go-template", "go-template-file", "template", "templatefile":
		return cliError("-o go-template is not supported. Use -o json or -o yaml.")
	case "name":
		return cliError("-o name is not supported. Use -o wide.")
	}
	return cliError("-o " + v + " is not supported. Use -o wide, yaml or json.")
}

// jsonpath returns the template of -o jsonpath=TEMPLATE.
func (t translator) jsonpath() (string, bool) {
	v := t.p.flags["output"]
	base, tpl, ok := strings.Cut(v, "=")
	if !ok || !strings.EqualFold(base, "jsonpath") {
		return "", false
	}
	return tpl, true
}

func (t translator) describe() ([]call, error) {
	pos := t.args()
	if len(pos) == 0 {
		if t.p.has("selector") {
			return nil, cliError("-l needs a kind: describe KIND -l SELECTOR.")
		}
		return nil, cliError("describe needs a resource kind or a name, for example: describe pod NAME, or describe NAME.")
	}
	if len(pos) > 2 {
		return nil, severalNames
	}
	kind, name := pos[0], ""
	if k, n, ok := kindName(kind); ok {
		if len(pos) > 1 {
			return nil, severalNames
		}
		if k == "" || n == "" {
			return nil, cliError("Use KIND/NAME with one kind and one name.")
		}
		kind, name = k, n
	} else if len(pos) == 2 {
		name = pos[1]
	} else if _, err := t.ks.Lookup(kind); err != nil {
		// One word that is not a kind: a name. The tool finds the object.
		if t.p.has("selector") {
			return nil, cliError("-l needs a kind: describe KIND -l SELECTOR.")
		}
		a := t.base(map[string]any{"name": kind})
		if t.p.isTrue("all-namespaces") {
			a["allNamespaces"] = true
		}
		return []call{{"describe", a}}, nil
	}
	if name == "" {
		if t.p.has("selector") {
			a := t.base(map[string]any{"kind": kind, "labelSelector": t.p.flags["selector"]})
			if t.p.isTrue("all-namespaces") {
				a["allNamespaces"] = true
			}
			return []call{{"describe", a}}, nil
		}
		return []call{{"list", t.listArgs(kind)}}, nil
	}
	if t.p.has("selector") {
		return nil, cliError("A name and -l cannot be used together.")
	}
	a := t.base(map[string]any{"kind": kind, "name": name})
	if t.p.isTrue("all-namespaces") {
		a["allNamespaces"] = true
	}
	return []call{{"describe", a}}, nil
}

func (t translator) rollout() ([]call, error) {
	pos := t.args()
	if len(pos) == 0 {
		return nil, errNotSupported
	}
	switch pos[0] {
	case "status":
	case "history":
		return nil, cliError("rollout history is not supported. Use describe on the object.")
	default:
		return nil, errNotSupported
	}
	rest := pos[1:]
	var kind, name string
	switch {
	case len(rest) == 1:
		k, n, ok := kindName(rest[0])
		if !ok || k == "" || n == "" {
			return nil, cliError("rollout status needs KIND/NAME or KIND NAME.")
		}
		kind, name = k, n
	case len(rest) == 2:
		kind, name = rest[0], rest[1]
	default:
		return nil, cliError("rollout status needs KIND/NAME or KIND NAME.")
	}
	return []call{{"describe", t.base(map[string]any{"kind": kind, "name": name})}}, nil
}

// workloadResources are the kinds that logs reads through their pods.
var workloadResources = map[string]bool{
	"deployments": true, "statefulsets": true, "daemonsets": true, "jobs": true, "replicasets": true,
}

// target sets "pod" or "workload" in a for KIND and NAME.
func (t translator) target(a map[string]any, kind, name string) error {
	k, err := t.ks.Lookup(kind)
	if err != nil {
		return cliError(err.Error())
	}
	switch {
	case k.Resource() == "pods":
		a["pod"] = name
	case workloadResources[k.Resource()]:
		a["workload"] = k.Singular() + "/" + name
	default:
		return cliError("logs reads pods, deployments, statefulsets, daemonsets, jobs and replicasets.")
	}
	return nil
}

func (t translator) isLogKind(w string) bool {
	k, err := t.ks.Lookup(w)
	return err == nil && (k.Resource() == "pods" || workloadResources[k.Resource()])
}

func (t translator) logs() ([]call, error) {
	pos := t.args()
	a := t.base(map[string]any{})
	container := t.p.flags["container"]
	sel := t.p.flags["selector"]
	switch {
	case t.p.has("workload"):
		if len(pos) > 0 || sel != "" {
			return nil, cliError("--workload cannot be used with a pod name or -l.")
		}
		a["workload"] = t.p.flags["workload"]
	case sel != "":
		if len(pos) > 0 {
			return nil, cliError("-l cannot be used with a pod or workload name.")
		}
		a["labelSelector"] = sel
	case len(pos) == 0:
		return nil, cliError("logs needs a pod name, KIND/NAME or -l SELECTOR.")
	case len(pos) > 2:
		return nil, cliError("logs takes a pod name and an optional container name.")
	default:
		first := pos[0]
		if k, n, ok := kindName(first); ok {
			if k == "" || n == "" || len(pos) > 1 {
				return nil, cliError("Use KIND/NAME with one kind and one name.")
			}
			if err := t.target(a, k, n); err != nil {
				return nil, err
			}
		} else if len(pos) == 2 && t.isLogKind(first) {
			// logs deploy NAME
			if err := t.target(a, first, pos[1]); err != nil {
				return nil, err
			}
		} else {
			a["pod"] = first
			if len(pos) == 2 {
				if container != "" && container != pos[1] {
					return nil, cliError("The container is named twice. Use -c or the second word, not both.")
				}
				container = pos[1]
			}
		}
	}
	if t.p.isTrue("all-namespaces") {
		if sel == "" {
			return nil, cliError("-A for logs needs -l SELECTOR.")
		}
		a["allNamespaces"] = true
	}
	if container != "" {
		a["container"] = container
	}
	if t.p.isTrue("previous") {
		a["previous"] = true
	}
	if v := t.p.flags["since"]; v != "" {
		a["since"] = v
	}
	if v, ok := t.p.flags["tail"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, cliError("--tail must be a number.")
		}
		if n > 0 { // kubectl uses -1 for all lines; the tool default applies
			a["tail"] = n
		}
	}
	if v := t.p.flags["match"]; v != "" {
		a["match"] = v
	}
	if t.p.isTrue("full") {
		a["full"] = true
	}
	return []call{{"logs", a}}, nil
}

func (t translator) events() ([]call, error) {
	if len(t.args()) > 0 {
		return nil, cliError("events takes no names. Use --for KIND/NAME.")
	}
	a, err := t.eventArgs("")
	if err != nil {
		return nil, err
	}
	if v := t.p.flags["for"]; v != "" {
		k, n, ok := kindName(v)
		if !ok || k == "" || n == "" {
			return nil, cliError("--for must be KIND/NAME.")
		}
		a["kind"], a["name"] = k, n
	}
	if err := t.outputOK(); err != nil {
		return nil, err
	}
	return []call{{"events", a}}, nil
}

// eventArgs builds the arguments of the events tool from the flags, the
// --types flag and the three field selectors that map to tool arguments.
func (t translator) eventArgs(fieldSelector string) (map[string]any, error) {
	a := t.multi(map[string]any{})
	if v := t.p.flags["since"]; v != "" {
		a["since"] = v
	}
	if v := t.p.flags["limit"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n != 0 {
			a["limit"] = n
		}
	}
	if v := t.p.flags["reason"]; v != "" {
		a["reason"] = v
	}
	if v := t.p.flags["match"]; v != "" {
		a["match"] = v
	}
	warn := t.p.isTrue("warnings")
	if v := t.p.flags["types"]; v != "" {
		var hasW, hasN bool
		for _, x := range strings.Split(v, ",") {
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "warning":
				hasW = true
			case "normal":
				hasN = true
			default:
				return nil, cliError("--types accepts Warning and Normal.")
			}
		}
		if hasN && !hasW {
			return nil, cliError("--types=Normal is not supported. Use --types=Warning, or no filter.")
		}
		warn = warn || (hasW && !hasN)
	}
	if fieldSelector != "" {
		for _, term := range strings.Split(fieldSelector, ",") {
			key, val, ok := strings.Cut(term, "==")
			if !ok {
				key, val, ok = strings.Cut(term, "=")
			}
			if !ok || val == "" || strings.HasSuffix(key, "!") {
				return nil, cliError("--field-selector for events accepts involvedObject.name=, involvedObject.kind=, reason= and type=Warning.")
			}
			switch key {
			case "involvedObject.name":
				a["name"] = val
			case "involvedObject.kind":
				a["kind"] = val
			case "reason":
				a["reason"] = val
			case "type":
				if !strings.EqualFold(val, "Warning") {
					return nil, cliError("--field-selector type=" + val + " is not supported. Use type=Warning.")
				}
				warn = true
			default:
				return nil, cliError("--field-selector " + key + " is not supported for events. Use involvedObject.name=, involvedObject.kind=, reason= or type=Warning.")
			}
		}
	}
	if warn {
		a["warningsOnly"] = true
	}
	return a, nil
}

func (t translator) top() ([]call, error) {
	pos := t.args()
	a := t.multi(map[string]any{})
	if len(pos) > 0 {
		switch strings.ToLower(pos[0]) {
		case "pods", "pod", "po":
			a["resource"] = "pods"
		case "nodes", "node", "no":
			a["resource"] = "nodes"
		default:
			return nil, cliError("top reads pods and nodes.")
		}
	}
	if len(pos) > 1 {
		return nil, cliError("top by name is not supported. Run top without a name, or use -l.")
	}
	if v := t.p.flags["selector"]; v != "" {
		a["labelSelector"] = v
	}
	if v := t.p.flags["sort"]; v != "" {
		a["sortBy"] = v
	} else if v := t.p.flags["sort-by"]; v != "" {
		a["sortBy"] = v
	}
	return []call{{"top", a}}, nil
}

// kubectl runs a kubectl style command. ok is false when the words are not
// kubectl commands, so the original commands see them.
func (e env) kubectl(args []string) (code int, ok bool) {
	if len(args) == 0 || notKubectl[args[0]] {
		return 0, false
	}
	if args[0] != "kubectl" {
		p, err := lex(args)
		if err != nil || len(p.pos) == 0 || !(supported[p.pos[0]] || refused[p.pos[0]]) {
			return 0, false
		}
	}
	pl, err := translate(args)
	if err != nil {
		fmt.Fprintln(e.stderr, err)
		return 1, true
	}
	if pl.help {
		e.usage()
		return 0, true
	}
	r, cl, done, err := e.runner(pl.conn, "cli")
	if err != nil {
		fmt.Fprintf(e.stderr, "%s: %v\n", brand.Name, err)
		return 1, true
	}
	defer done()
	e.warnIdentityOnTerminal(cl)
	for i, c := range pl.calls {
		res := r.Call(context.Background(), c.Tool, c.json())
		if i > 0 {
			fmt.Fprintln(e.stdout)
		}
		fmt.Fprint(e.stdout, res.Text)
		if res.IsError {
			code = 1
		}
	}
	return code, true
}

func (e env) kubectlUsage() {
	n := brand.Name
	fmt.Fprintf(e.stderr, `
The kubectl read commands also work: replace the word kubectl with %[1]s.
  %[1]s get pods -A | get deploy,svc | get pod NAME -o yaml | get events
  %[1]s describe deploy/NAME | describe NAME | describe pod -l app=api | logs POD -c C --tail=100 | logs -l app=api | logs deploy/NAME
  %[1]s get events --field-selector reason=Failed | top pods | top nodes | rollout status deploy/NAME
  %[1]s get pods -A --match WORDS | events --match WORDS | logs POD --match WORDS (words with a comma between them; any one matches)
Commands that change the cluster or open a session are not supported.
`, n)
}
