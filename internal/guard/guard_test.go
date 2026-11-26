package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/groundcover-com/kray/internal/brand"
)

const module = "github.com/groundcover-com/kray"

type file struct {
	pkg     string // module-relative package dir, for example "internal/tools"
	name    string
	ast     *ast.File
	imports map[string]string // local name -> import path
}

// loadFiles parses main.go and every non-test file under internal/.
func loadFiles(t *testing.T) ([]file, *token.FileSet) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []file
	add := func(p string) {
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		ff := file{pkg: filepath.ToSlash(rel), name: filepath.Base(p), ast: f, imports: map[string]string{}}
		for _, is := range f.Imports {
			ip, _ := strconv.Unquote(is.Path.Value)
			local := path.Base(ip)
			if is.Name != nil {
				local = is.Name.Name
			}
			ff.imports[local] = ip
		}
		out = append(out, ff)
	}
	add(filepath.Join(root, "main.go"))
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		add(p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 5 {
		t.Fatalf("found only %d files; the guard is not looking at the code", len(out))
	}
	return out, fset
}

func importPaths(f file) []string {
	var ps []string
	for _, ip := range f.imports {
		ps = append(ps, ip)
	}
	return ps
}

// No process execution, no profiling endpoints.
func TestNoExecImports(t *testing.T) {
	files, _ := loadFiles(t)
	forbidden := map[string]bool{
		"os/exec": true, "syscall": true, "golang.org/x/sys/unix": true, "golang.org/x/sys/windows": true,
		"net/http/pprof": true, "expvar": true, "plugin": true, "net/rpc": true, "net/smtp": true,
	}
	for _, f := range files {
		for _, ip := range importPaths(f) {
			if forbidden[ip] {
				t.Errorf("%s/%s imports %s", f.pkg, f.name, ip)
			}
		}
	}
}

type use struct{ pkgPath, sel string }

// selectorUses lists pkg.Sel references and pkg.Type{} literals in a file.
func selectorUses(f file, fset *token.FileSet, fn func(u use, pos token.Position)) {
	ast.Inspect(f.ast, func(n ast.Node) bool {
		if _, ok := n.(*ast.ImportSpec); ok {
			return false
		}
		if se, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := se.X.(*ast.Ident); ok {
				if ip, ok := f.imports[id.Name]; ok {
					fn(use{ip, se.Sel.Name}, fset.Position(se.Pos()))
				}
			}
		}
		return true
	})
}

// No listener, no own HTTP client, no HTTP MCP
// transport, no process start.
func TestNoListenersOrOwnNetwork(t *testing.T) {
	files, fset := loadFiles(t)
	forbidden := map[use]bool{}
	for _, s := range []string{"Listen", "ListenTCP", "ListenUDP", "ListenUnix", "ListenUnixgram", "ListenPacket", "ListenIP", "ListenMulticastUDP", "Dial", "DialTCP", "DialUDP", "DialUnix", "DialIP", "DialTimeout", "FileListener"} {
		forbidden[use{"net", s}] = true
	}
	for _, s := range []string{"ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS", "Server", "Client", "Transport", "Get", "Post", "Head", "PostForm", "DefaultClient", "DefaultTransport", "FileServer", "NewServeMux", "Handle", "HandleFunc"} {
		forbidden[use{"net/http", s}] = true
	}
	for _, s := range []string{"NewStreamableHTTPHandler", "NewSSEHandler", "StreamableServerTransport", "SSEServerTransport", "StreamableClientTransport", "SSEClientTransport", "CommandTransport"} {
		forbidden[use{"github.com/modelcontextprotocol/go-sdk/mcp", s}] = true
	}
	forbidden[use{"os", "StartProcess"}] = true
	for _, f := range files {
		selectorUses(f, fset, func(u use, pos token.Position) {
			if forbidden[u] {
				t.Errorf("%s: uses %s.%s", pos, u.pkgPath, u.sel)
			}
		})
	}
}

func inPkgs(pkg string, allowed ...string) bool {
	for _, a := range allowed {
		if pkg == a {
			return true
		}
	}
	return false
}

// Only readonly (and setup) build a rest.Config; only kube, readonly and
// setup build clients.
func TestOneConfigBuilder(t *testing.T) {
	files, fset := loadFiles(t)
	const (
		readonly = "internal/kube/readonly"
		kube     = "internal/kube"
		setup    = "internal/setup"
	)
	for _, f := range files {
		for _, ip := range importPaths(f) {
			if ip == "k8s.io/client-go/tools/clientcmd" && !inPkgs(f.pkg, readonly, setup) {
				t.Errorf("%s/%s imports clientcmd; only %s and %s may", f.pkg, f.name, readonly, setup)
			}
		}
		selectorUses(f, fset, func(u use, pos token.Position) {
			switch {
			case u.pkgPath == "k8s.io/client-go/rest" && (u.sel == "InClusterConfig" || u.sel == "Config" || u.sel == "AnonymousClientConfig"):
				if !inPkgs(f.pkg, readonly, setup) {
					t.Errorf("%s: rest.%s outside readonly and setup", pos, u.sel)
				}
			case u.pkgPath == "k8s.io/client-go/rest" && strings.Contains(u.sel, "For"),
				u.pkgPath == "k8s.io/client-go/kubernetes" && strings.HasPrefix(u.sel, "NewFor"),
				u.pkgPath == "k8s.io/client-go/dynamic" && strings.HasPrefix(u.sel, "NewFor"),
				u.pkgPath == "k8s.io/client-go/metadata" && strings.HasPrefix(u.sel, "NewFor"),
				u.pkgPath == "k8s.io/client-go/discovery" && strings.Contains(u.sel, "ForConfig"):
				if !inPkgs(f.pkg, readonly, kube, setup) {
					t.Errorf("%s: client constructor %s.%s outside kube, readonly and setup", pos, path.Base(u.pkgPath), u.sel)
				}
			case u.pkgPath == module+"/"+readonly && u.sel == "LoadForSetup":
				if f.pkg != setup {
					t.Errorf("%s: readonly.LoadForSetup outside internal/setup", pos)
				}
			}
		})
	}
}

// The MCP server, the tools and the cluster reader never depend on the
// setup code, which is the only code with non-GET clients.
func TestServerDoesNotImportSetup(t *testing.T) {
	files, _ := loadFiles(t)
	deps := map[string]map[string]bool{}
	for _, f := range files {
		if deps[f.pkg] == nil {
			deps[f.pkg] = map[string]bool{}
		}
		for _, ip := range importPaths(f) {
			if strings.HasPrefix(ip, module+"/") {
				deps[f.pkg][strings.TrimPrefix(ip, module+"/")] = true
			}
		}
	}
	for _, root := range []string{"internal/mcpserver", "internal/tools", "internal/kube", "internal/kube/readonly", "internal/redact", "internal/shape", "internal/audit"} {
		seen := map[string]bool{}
		var walk func(p string, chain []string)
		walk = func(p string, chain []string) {
			if seen[p] {
				return
			}
			seen[p] = true
			if p == "internal/setup" {
				t.Errorf("%s depends on internal/setup via %s", root, strings.Join(chain, " -> "))
				return
			}
			var next []string
			for d := range deps[p] {
				next = append(next, d)
			}
			sort.Strings(next)
			for _, d := range next {
				walk(d, append(chain, d))
			}
		}
		walk(root, []string{root})
	}
}

// Rename point and "tool output never mentions any product": no string
// literal outside internal/brand contains the product name or the company.
func TestNoProductNameInLiterals(t *testing.T) {
	files, fset := loadFiles(t)
	name := strings.ToLower(brand.Name)
	for _, f := range files {
		if f.pkg == "internal/brand" {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			if _, ok := n.(*ast.ImportSpec); ok {
				return false
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s := strings.ToLower(lit.Value)
			if strings.Contains(s, name) || strings.Contains(s, "groundcover") {
				t.Errorf("%s: string literal %s names the product; use brand.Name", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}
