package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteAppendsOneLinePerEntry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "audit.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := l.Write(Entry{Time: time.Now(), Tool: "logs", Args: map[string]any{"pod": "api"}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()
	if err := l.Write(Entry{}); err == nil {
		t.Fatal("write after close must fail")
	}
	f, _ := os.Open(p)
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var e map[string]any
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %d: %v", n, err)
		}
		for _, k := range []string{"time", "tool", "args", "context", "namespace", "result_bytes", "redactions", "duration_ms", "error", "blocked_requests"} {
			if _, ok := e[k]; !ok {
				t.Fatalf("line %d has no %q", n, k)
			}
		}
		n++
	}
	if n != 3 {
		t.Fatalf("%d lines", n)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
		dst, _ := os.Stat(filepath.Dir(p))
		if dst.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", dst.Mode().Perm())
		}
	}
}

func TestOpenRefusesOpenPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("opened a world-readable audit log")
	}
}

func TestOpenFailsWhenUnwritable(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("empty path accepted")
	}
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "is-a-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "d")); err == nil {
		t.Fatal("opened a directory as the audit log")
	}
}
