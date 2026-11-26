// Package readonly is the single choke point between the server process and
// the Kubernetes API server. Every request passes Check before a byte leaves
// the process.
//
// Nothing in this package knows about tools or agents. It only answers one
// question: may this HTTP request go to the cluster?
package readonly

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ErrBlocked is the error for every request that Check rejects.
var ErrBlocked = errors.New("request blocked by the read-only transport")

// BlockedError says which rule rejected a request.
type BlockedError struct {
	Reason string
	Method string
	Path   string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("%v: %s %s: %s", ErrBlocked, e.Method, e.Path, e.Reason)
}

// Unwrap lets errors.Is(err, ErrBlocked) match.
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// deniedSegments are path segments that are never sent, compared as whole
// segments, case-insensitive. A name that only contains one of them, such as
// kube-proxy, is allowed.
var deniedSegments = []string{"proxy", "exec", "attach", "portforward", "secrets"}

// allowedQuery is the allowlist of query keys. watch and follow are not on
// it, so a watch or a log follow is rejected.
var allowedQuery = map[string]bool{
	"labelSelector": true,
	"fieldSelector": true,
	"limit":         true,
	"continue":      true,
	"container":     true,
	"previous":      true,
	"sinceSeconds":  true,
	"tailLines":     true,
	"timestamps":    true,
	"limitBytes":    true,
	// client-go adds a server-side request deadline (discovery uses 32s).
	"timeout": true,
}

var (
	versionRE  = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)
	groupRE    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	resourceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	nameRE     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
)

// Target is the only API server that requests may go to.
type Target struct {
	Scheme string // "https" or "http"
	Host   string // host[:port], as in url.URL.Host
	Prefix string // path prefix of the server URL ("" or for example "/k8s/clusters/c-1")
}

// TargetFor returns the Target for an API server URL.
func TargetFor(server *url.URL) (Target, error) {
	if server == nil || server.Host == "" {
		return Target{}, errors.New("API server URL has no host")
	}
	scheme := strings.ToLower(server.Scheme)
	if scheme != "https" && scheme != "http" {
		return Target{}, fmt.Errorf("API server URL scheme %q is not http or https", server.Scheme)
	}
	prefix := strings.TrimRight(server.Path, "/")
	if prefix != "" {
		if path.Clean(prefix) != prefix || !strings.HasPrefix(prefix, "/") {
			return Target{}, fmt.Errorf("API server URL path %q is not clean", server.Path)
		}
		for _, r := range prefix {
			if !pathChar(r) {
				return Target{}, fmt.Errorf("API server URL path %q has a character that is not allowed", server.Path)
			}
		}
	}
	return Target{Scheme: scheme, Host: strings.ToLower(server.Host), Prefix: prefix}, nil
}

// Check returns nil when the request may go to the cluster, and a
// *BlockedError otherwise. It does not read or change the request.
func Check(req *http.Request, t Target) error {
	method := ""
	if req != nil {
		method = req.Method
	}
	block := func(format string, args ...any) error {
		p := ""
		if req != nil && req.URL != nil {
			p = req.URL.EscapedPath()
		}
		return &BlockedError{Reason: fmt.Sprintf(format, args...), Method: method, Path: p}
	}
	if req == nil || req.URL == nil {
		return block("no request URL")
	}

	// 1. GET only, with no body.
	if req.Method != http.MethodGet {
		return block("method is not GET")
	}
	if req.Body != nil && req.Body != http.NoBody {
		return block("request has a body")
	}
	if req.ContentLength != 0 {
		return block("request has a content length")
	}

	// 2. No protocol upgrade (a websocket handshake is a GET). Header names
	// are compared without case: a map key set directly is not canonical.
	for k, vs := range req.Header {
		switch {
		case strings.EqualFold(k, "Upgrade"):
			return block("upgrade header")
		case strings.HasPrefix(strings.ToLower(k), "sec-websocket-"):
			return block("websocket header")
		case strings.EqualFold(k, "Connection"):
			for _, v := range vs {
				for _, tok := range strings.Split(v, ",") {
					if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
						return block("connection upgrade")
					}
				}
			}
		}
	}

	// 3. The configured API server only.
	u := req.URL
	if !strings.EqualFold(u.Scheme, t.Scheme) || !strings.EqualFold(u.Host, t.Host) || t.Host == "" {
		return block("host is not the configured API server")
	}
	if u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" {
		return block("URL has user info, an opaque part or a fragment")
	}

	// 4. No escaped forms of anything.
	if u.RawPath != "" {
		return block("path has escaped characters")
	}

	p := u.Path
	if t.Prefix != "" {
		if p != t.Prefix && !strings.HasPrefix(p, t.Prefix+"/") {
			return block("path is outside the API server prefix")
		}
		p = strings.TrimPrefix(p, t.Prefix)
	}

	// 5. Clean, plain path.
	if p == "" || !strings.HasPrefix(p, "/") {
		return block("empty path")
	}
	for _, r := range p {
		if !pathChar(r) {
			return block("path has a character that is not allowed")
		}
	}
	if path.Clean(p) != p {
		return block("path is not clean")
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return block("path has an empty or dot segment")
		}
	}

	// 6 and 7. Denied segments, then the allowed shapes.
	for _, s := range segs {
		for _, d := range deniedSegments {
			if strings.EqualFold(s, d) {
				return block("path segment %q is denied", s)
			}
		}
	}
	if reason := shape(segs); reason != "" {
		return block("%s", reason)
	}

	// 8. Known query keys only, each once.
	if strings.Contains(u.RawQuery, ";") {
		return block("query has a semicolon")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return block("query does not parse")
	}
	for k, vs := range q {
		if !allowedQuery[k] {
			return block("query key %q is not allowed", k)
		}
		if len(vs) != 1 {
			return block("query key %q is repeated", k)
		}
	}
	return nil
}

func pathChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '-' || r == '.' || r == '_' || r == '/'
}

// shape returns "" when segs is one of the request shapes kray uses:
// discovery, /version, list, get, and the pods/log subresource. Every other
// subresource is rejected, so a new dangerous subresource is closed by
// default.
func shape(segs []string) string {
	switch segs[0] {
	case "version":
		if len(segs) == 1 {
			return ""
		}
		return "unknown /version path"
	case "api":
		if len(segs) == 1 {
			return ""
		}
		if !versionRE.MatchString(segs[1]) {
			return "core API version is not valid"
		}
		return resourcePath(segs[2:])
	case "apis":
		if len(segs) == 1 {
			return ""
		}
		if !groupRE.MatchString(segs[1]) || len(segs[1]) > 253 {
			return "API group is not valid"
		}
		if len(segs) == 2 {
			return ""
		}
		if !versionRE.MatchString(segs[2]) {
			return "API version is not valid"
		}
		return resourcePath(segs[3:])
	}
	return "path is not under /api, /apis or /version"
}

func resourcePath(rest []string) string {
	isName := func(s string) bool { return len(s) <= 253 && nameRE.MatchString(s) }
	isResource := func(s string) bool { return len(s) <= 63 && resourceRE.MatchString(s) }

	if len(rest) == 0 {
		return ""
	}
	if rest[0] == "namespaces" {
		switch len(rest) {
		case 1:
			return ""
		case 2, 3, 4, 5:
			if !isName(rest[1]) {
				return "namespace name is not valid"
			}
			if len(rest) == 2 {
				return ""
			}
			if !isResource(rest[2]) {
				return "resource name is not valid"
			}
			if len(rest) == 3 {
				return ""
			}
			if !isName(rest[3]) {
				return "object name is not valid"
			}
			if len(rest) == 4 {
				return ""
			}
			if rest[2] == "pods" && rest[4] == "log" {
				return ""
			}
			return "subresource is not allowed"
		}
		return "path is too deep"
	}
	switch len(rest) {
	case 1:
		if !isResource(rest[0]) {
			return "resource name is not valid"
		}
		return ""
	case 2:
		if !isResource(rest[0]) {
			return "resource name is not valid"
		}
		if !isName(rest[1]) {
			return "object name is not valid"
		}
		return ""
	}
	return "subresource is not allowed"
}
