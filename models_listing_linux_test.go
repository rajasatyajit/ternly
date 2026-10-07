package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ternly --models keeps the model key as the last field of every row: the
// e2e preflight (and users' scripts) read it from the end. Routing v2's
// explanation column once broke that.
func TestModelsListingKeyLast(t *testing.T) {
	home := testHome(t, "http://127.0.0.1:1")
	_ = os.WriteFile(filepath.Join(home, ".config", "ternly", "config.json"), []byte(`{"suggestions":false}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".cache", "ternly", "catalog.json"), []byte(`{"offline-test":{}}`), 0o600)
	c := ternly(t, home, "--models")
	c.Env = append(c.Env, "OLLAMA_HOST="+fakeOllama(t, `{"models":[]}`))
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rows := 0
	for _, l := range strings.Split(string(out), "\n") {
		fs := strings.Fields(l)
		if len(fs) < 6 || len(fs[0]) != 2 || fs[0][0] != 'T' {
			continue
		}
		rows++
		if !strings.HasPrefix(fs[len(fs)-1], "ollama/") {
			t.Fatalf("last field %q isn't a model key: %q", fs[len(fs)-1], l)
		}
	}
	if rows < 20 || !strings.Contains(string(out), "routing v2") {
		t.Fatalf("%d rows:\n%s", rows, out)
	}
}
