package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The qwen netguard run from M8's dogfooding, replayed through the real
// binary (ADR 015): the model writes an empty internal/netguard/go.mod (to
// make the directory) and then its final, non-compiling netguard.go
// (testdata/dogfood, verbatim), and says it's done. The project's check,
// `go build ./... && go vet ./...`, passes — ./... stops at the nested
// go.mod. M8's binary printed "✓ verified"; now the package is built in its
// own module and the turn fails verification.
func TestReplayQwenStrayGoMod(t *testing.T) {
	src, err := os.ReadFile("testdata/dogfood/qwen-netguard.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"path": "internal/netguard/netguard.go", "content": string(src)})
	f := newProvider(t,
		step{call: [2]string{"write_file", `{"path":"internal/netguard/go.mod","content":""}`}},
		step{call: [2]string{"write_file", string(body)}},
		step{text: "Done: internal/netguard is implemented and verified."})
	home := testHome(t, f.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module github.com/rajasatyajit/ternly\n\ngo 1.22\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	out, _ := ternly(t, home, "-model", "fake/m1", "-mode", "edits", "-C", ws, "-p", "create internal/netguard").CombinedOutput()
	s := string(out)
	if strings.Contains(s, "✓ verified") {
		t.Fatalf("claimed verified:\n%s", s)
	}
	if !strings.Contains(s, "✗ verification failed") {
		t.Fatalf("no failed verification:\n%s", s)
	}
	_, users := f.seen()
	if !strings.Contains(strings.Join(users, "\n"), "internal/netguard") {
		t.Fatalf("the model wasn't shown the failure: %q", users)
	}
	t.Log(firstOutputLine(s[strings.Index(s, "✗ verification failed"):]))
}
