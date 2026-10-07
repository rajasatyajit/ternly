package graph

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #1: name-matched calls are listed apart from confirmed ones, and a
// call on a receiver of another type is never presented as a caller.

// callersOut runs the callers tool on a workspace of files.
func callersOut(t *testing.T, files map[string]string, symbol string, unverified *bool) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	svc := NewService(root, t.TempDir(), "k", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); svc.Wait() })
	svc.Start(ctx)
	if _, _, err := svc.Graph(ctx, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, tl := range Tools(svc) {
		if tl.Spec.Name == "callers" {
			b, _ := json.Marshal(map[string]any{"symbol": symbol, "include_unverified": unverified})
			out, err := tl.Run(ctx, b)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatal("no callers tool")
	return ""
}

// sections splits callers output into confirmed / possible / not lines.
func sections(out string) map[string][]string {
	got := map[string][]string{}
	cur := ""
	for l := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.Contains(l, " callers (typed"):
			cur = "sure"
		case strings.Contains(l, "possible callers"):
			cur = "maybe"
		case strings.Contains(l, "not callers"):
			cur = "not"
		case cur != "" && strings.Contains(l, ":") && !strings.HasPrefix(l, "none"):
			got[cur] = append(got[cur], strings.Fields(l)[0])
		}
	}
	return got
}

// The e2e check graph-callsites-python's workspace, verbatim.
var pyCallsites = map[string]string{
	"app/__init__.py":  "",
	"app/store.py":     "class Store:\n    def flush(self):\n        return 0\n\n\nclass Buffer:\n    def flush(self):\n        return 1\n",
	"app/run.py":       "from app.store import Store, Buffer\n\n\ndef run(s: Store, b: Buffer):\n    b.flush()\n    return s.flush()\n",
	"app/stop.py":      "from app.store import Store\n\n\ndef stop(s: Store):\n    s.flush()\n",
	"server/server.py": "from app.store import Store\n\n\nclass Server:\n    def __init__(self):\n        self.db = Store()\n\n    def close(self):\n        self.db.flush()\n        return self.db.flush()\n",
}

func TestCallersByReceiverPython(t *testing.T) {
	out := callersOut(t, pyCallsites, "app/store.Store.flush", nil)
	got := sections(out)
	if strings.Join(got["sure"], " ") != "app/run.py:6 app/stop.py:5 server/server.py:9 server/server.py:10" {
		t.Errorf("confirmed: %v\n%s", got["sure"], out)
	}
	if strings.Join(got["not"], " ") != "app/run.py:5" || len(got["maybe"]) != 0 {
		t.Errorf("not: %v maybe: %v\n%s", got["not"], got["maybe"], out)
	}
	if !strings.Contains(out, "→ Buffer.flush (def run(s: Store, b: Buffer):)") || !strings.Contains(out, "receiver is Store: self.db = Store()") {
		t.Errorf("evidence missing:\n%s", out)
	}
}

// No evidence: the call stays a possible caller, and can be left out.
func TestCallersUnknownReceiver(t *testing.T) {
	files := map[string]string{
		"app/store.py": "class Store:\n    def flush(self):\n        return 0\n\n\nclass Buffer:\n    def flush(self):\n        return 1\n",
		"app/use.py":   "def use(x):\n    x.flush()\n",
	}
	out := callersOut(t, files, "app/store.Store.flush", nil)
	if got := sections(out); strings.Join(got["maybe"], " ") != "app/use.py:2" || len(got["sure"]) != 0 {
		t.Fatalf("%v\n%s", got, out)
	}
	off := false
	out = callersOut(t, files, "app/store.Store.flush", &off)
	if !strings.Contains(out, "1 possible callers omitted") || strings.Contains(out, "app/use.py:2") {
		t.Fatalf("include_unverified=false:\n%s", out)
	}
}

func TestCallersByReceiverOtherLanguages(t *testing.T) {
	for name, c := range map[string]struct {
		files  map[string]string
		symbol string
		sure   string
		not    string
	}{
		"typescript": {map[string]string{
			"web/store.ts": "export class Store {\n  flush(): number { return 0; }\n}\nexport class Buffer {\n  flush(): number { return 1; }\n}\n",
			"web/run.ts":   "import { Store, Buffer } from './store';\nexport function run(s: Store, b: Buffer): number {\n  b.flush();\n  const t = new Store();\n  t.flush();\n  return s.flush();\n}\n",
		}, "web/store.Store.flush", "web/run.ts:5 web/run.ts:6", "web/run.ts:3"},
		"java": {map[string]string{
			"j/Store.java":  "class Store {\n  int flush() { return 0; }\n}\n",
			"j/Buffer.java": "class Buffer {\n  int flush() { return 1; }\n}\n",
			"j/Run.java":    "class Run {\n  int run(Store s, Buffer b) {\n    b.flush();\n    return s.flush();\n  }\n}\n",
		}, "Store.flush", "j/Run.java:4", "j/Run.java:3"},
	} {
		t.Run(name, func(t *testing.T) {
			out := callersOut(t, c.files, c.symbol, nil)
			got := sections(out)
			if strings.Join(got["sure"], " ") != c.sure || strings.Join(got["not"], " ") != c.not {
				t.Fatalf("sure %v not %v maybe %v\n%s", got["sure"], got["not"], got["maybe"], out)
			}
		})
	}
}

// Rust impl methods are graphed as plain functions (src/lib.flush; no owning
// type yet, issue #10), so there is no receiver type to check: every call
// stays a possible caller, never a confirmed one.
func TestCallersRustStayPossible(t *testing.T) {
	files := map[string]string{
		"src/lib.rs": "pub struct Store {}\nimpl Store {\n    pub fn flush(&self) -> u8 { 0 }\n}\npub struct Buffer {}\nimpl Buffer {\n    pub fn flush(&self) -> u8 { 1 }\n}\npub fn run(s: &Store, b: &mut Buffer) -> u8 {\n    b.flush();\n    s.flush()\n}\n",
	}
	out := callersOut(t, files, "src/lib.flush", nil)
	if !strings.Contains(out, "ambiguous") && !strings.Contains(out, "possible callers") {
		t.Fatalf("%s", out)
	}
	if got := sections(out); len(got["sure"]) != 0 {
		t.Fatalf("a name-only Rust call was confirmed: %v\n%s", got, out)
	}
}

func TestReceiverAt(t *testing.T) {
	for line, want := range map[string]string{
		"    b.flush()":              "b",
		"    return self.db.flush()": "self.db",
		"  this.store . flush();":    "this.store",
		"    flush()":                "",
		"    get().flush()":          "",
		"    x.flush(); y.flush()":   "x", // nearest to column 0
	} {
		if got := receiverAt(line, "flush", 0); got != want {
			t.Errorf("%q → %q, want %q", line, got, want)
		}
	}
}
