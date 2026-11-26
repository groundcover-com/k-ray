package harness

import (
	"encoding/json"
	"runtime"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type planned struct {
	tool    string
	args    map[string]any
	hostile bool
}

// HostileKinds are kind or resource values that must never lead to a read.
var HostileKinds = []string{"secrets", "secret", "Secret", "SECRETS", "secrets.v1", "v1/secrets", "pods/exec", "pods/attach", "pods/portforward", "nodes/proxy", "services/proxy", "serviceaccounts/token", "*"}

// HostileNames are object names shaped like flags, paths or URLs.
var HostileNames = []string{
	"--server=https://evil.example", "-sURL", "--kubeconfig=/etc/passwd", "--token=x", "-o=json",
	"../secrets/db-credentials", "x/../../secrets", "%2e%2e%2fsecrets", "db-credentials", "a;rm -rf /",
	"$(id)", "`id`", "pods/x/exec", "nodes/n/proxy", "x\ny", strings.Repeat("a", 300),
}

// HostileSelectors are odd label selectors.
var HostileSelectors = []string{"a=b,c!=d", "x in (y)", "!!!", "a=" + strings.Repeat("b", 5000), "a=b\nwatch=true", "a=b&watch=true", "metadata.name=x"}

// HostileNumbers are huge or negative limits.
var HostileNumbers = []any{-1, 0, 1e12, 9223372036854775807}

// HostileNamespaces are odd namespace values.
var HostileNamespaces = []string{"kube-system", "", "*", "../", "default,kube-system"}

func isLinux() bool { return runtime.GOOS == "linux" }

// showWords is a text search (the match argument) for the words around the
// planted secrets: any of them.
const showWords = "password,token,login,key,PRIVATE,rejected"

// showsText reports whether a search over the object can show free text:
// pods and their logs, events and custom resources.
func showsText(o Object) bool {
	return o.KindName == "Pod" || o.KindName == "Event" || o.KindName == "Widget" || o.KindName == ""
}

// dedupe drops a call that is the same as an earlier one (for example a
// tool without a kind argument gets one base call for each object).
func dedupe(in []planned) []planned {
	seen := map[string]bool{}
	var out []planned
	for _, p := range in {
		b, _ := json.Marshal(p.args)
		k := p.tool + string(b)
		if p.hostile {
			k += "!"
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}

type schema struct {
	Properties map[string]struct {
		Type string `json:"type"`
		Enum []any  `json:"enum"`
	} `json:"properties"`
	Required []string `json:"required"`
}

func readSchema(t *mcp.Tool) schema {
	var s schema
	b, _ := json.Marshal(t.InputSchema)
	_ = json.Unmarshal(b, &s)
	return s
}

type role int

const (
	roleOther role = iota
	roleKind
	roleName
	rolePod
	roleNamespace
	roleContainer
	roleSelector
	roleSince
	roleNumber
	roleBool
)

func roleOf(prop, typ string) role {
	p := strings.ToLower(prop)
	switch {
	case typ == "boolean":
		return roleBool
	case typ == "integer" || typ == "number":
		return roleNumber
	case p == "kind" || p == "resource" || p == "resourcetype" || p == "type" || p == "resource_type" || p == "kinds":
		return roleKind
	case p == "pod" || p == "podname" || p == "pod_name":
		return rolePod
	case p == "name" || p == "resourcename" || p == "resource_name" || p == "objectname":
		return roleName
	case p == "namespace" || p == "ns":
		return roleNamespace
	case p == "container" || p == "containername":
		return roleContainer
	case strings.Contains(p, "selector"):
		return roleSelector
	case p == "since" || p == "sincetime" || p == "duration":
		return roleSince
	}
	return roleOther
}

// Plan builds the calls for every tool from its input schema: one normal
// call per fixture object, then one hostile substitution per argument.
func Plan(tools []*mcp.Tool, f Fixture) []planned {
	var out []planned
	for _, t := range tools {
		s := readSchema(t)
		roles := map[string]role{}
		for p, def := range s.Properties {
			roles[p] = roleOf(p, def.Type)
		}
		has := func(r role) bool {
			for _, x := range roles {
				if x == r {
					return true
				}
			}
			return false
		}
		base := func(o Object) map[string]any {
			args := map[string]any{}
			for p, r := range roles {
				switch r {
				case roleKind:
					args[p] = o.Kind
				case roleName:
					args[p] = o.Name
				case rolePod:
					args[p] = o.Name
				case roleNamespace:
					if o.Namespace != "" {
						args[p] = o.Namespace
					}
				case roleContainer:
					if o.Container != "" {
						args[p] = o.Container
					}
				}
			}
			for _, req := range s.Required {
				if _, ok := args[req]; !ok {
					if def := s.Properties[req]; len(def.Enum) > 0 {
						args[req] = def.Enum[0]
					}
				}
			}
			return args
		}
		var objs []Object
		for _, o := range f.Objects {
			if has(rolePod) && !has(roleKind) && o.Kind != "pods" {
				continue
			}
			objs = append(objs, o)
			if has(roleKind) && o.KindName != "" {
				o2 := o
				o2.Kind = o.KindName
				objs = append(objs, o2)
			}
		}
		if len(objs) == 0 {
			objs = []Object{{Namespace: f.Namespace}}
		}
		for _, o := range objs {
			b := base(o)
			out = append(out, planned{tool: t.Name, args: b})
			// Calls that would show a planted secret if redaction failed:
			// the larger output, the healthy rows, text search, the
			// previous instance of a container, and a workload's pods.
			with := func(kv ...any) {
				a := map[string]any{}
				for k, v := range b {
					a[k] = v
				}
				for i := 0; i < len(kv); i += 2 {
					a[kv[i].(string)] = kv[i+1]
				}
				out = append(out, planned{tool: t.Name, args: a})
			}
			if _, ok := s.Properties["full"]; ok {
				with("full", true)
			}
			if _, ok := s.Properties["all"]; ok {
				with("all", true)
			}
			if _, ok := s.Properties["match"]; ok && showsText(o) {
				with("match", showWords)
				if _, ok := s.Properties["full"]; ok {
					with("match", showWords, "full", true)
				}
			}
			if _, ok := s.Properties["previous"]; ok && has(rolePod) && o.Kind == "pods" {
				with("previous", true)
				if _, ok := s.Properties["full"]; ok {
					with("previous", true, "full", true)
				}
			}
		}
		// The pods of a workload, by the workload's name.
		if _, ok := s.Properties["workload"]; ok {
			for _, o := range f.Objects {
				if o.KindName != "Deployment" {
					continue
				}
				for _, full := range []bool{false, true} {
					a := map[string]any{"workload": "deployment/" + o.Name}
					if o.Namespace != "" {
						if _, ok := s.Properties["namespace"]; ok {
							a["namespace"] = o.Namespace
						}
					}
					if full {
						if _, ok := s.Properties["full"]; !ok {
							continue
						}
						a["full"] = true
					}
					out = append(out, planned{tool: t.Name, args: a})
				}
			}
		}
		// Hostile substitutions on top of a normal call.
		normal := base(objs[0])
		sub := func(p string, v any) {
			a := map[string]any{}
			for k, x := range normal {
				a[k] = x
			}
			a[p] = v
			out = append(out, planned{tool: t.Name, args: a, hostile: true})
		}
		for p, r := range roles {
			switch r {
			case roleKind:
				for _, v := range HostileKinds {
					sub(p, v)
					// Also ask for the planted Secret by name.
					a := map[string]any{}
					for k, x := range normal {
						a[k] = x
					}
					a[p] = v
					for q, rq := range roles {
						if rq == roleName {
							a[q] = "db-credentials"
						}
					}
					out = append(out, planned{tool: t.Name, args: a, hostile: true})
				}
			case roleName, rolePod, roleContainer:
				for _, v := range HostileNames {
					sub(p, v)
				}
			case roleNamespace:
				for _, v := range HostileNamespaces {
					sub(p, v)
				}
			case roleSelector:
				for _, v := range HostileSelectors {
					sub(p, v)
				}
			case roleNumber:
				for _, v := range HostileNumbers {
					sub(p, v)
				}
			case roleSince:
				for _, v := range []string{"1000000h", "-1h", "forever"} {
					sub(p, v)
				}
			}
		}
		// Unknown arguments that try to change the target.
		for _, extra := range []string{"context", "kubeconfig", "server", "as", "token"} {
			if _, ok := s.Properties[extra]; ok {
				continue
			}
			sub(extra, "https://evil.example")
		}
	}
	return dedupe(out)
}
