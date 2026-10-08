package surface_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/surface"
	"github.com/rajasatyajit/ternly/internal/surface/fake"
)

// The contract package imports nothing from ternly (ADR 021): neither track
// may tie it to its internals.
func TestNoInternalImports(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "surface.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, im := range f.Imports {
		if strings.Contains(im.Path.Value, "rajasatyajit/ternly") {
			t.Errorf("surface imports %s", im.Path.Value)
		}
	}
}

// No field may carry a secret: names that suggest one fail (ADR 021).
func TestNoSecrets(t *testing.T) {
	src, err := os.ReadFile("surface.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		code, _, _ := strings.Cut(line, "//")
		low := strings.ToLower(code)
		for _, w := range []string{"apikey", "api_key", "token ", "secret", "password", "cookie", "bearer"} {
			if strings.Contains(low, w) {
				t.Errorf("a field that may hold a secret: %q", strings.TrimSpace(line))
			}
		}
	}
}

// The fake coalesces notifications and closes the channel with the context.
func TestFakeChanges(t *testing.T) {
	var c fake.Core
	ctx, cancel := context.WithCancel(context.Background())
	ch := c.Changes(ctx)
	c.Set(surface.Snapshot{Meter: surface.Meter{Turns: 1}})
	c.Set(surface.Snapshot{Meter: surface.Meter{Turns: 2}})
	<-ch
	select {
	case <-ch:
		t.Fatal("two notifications pending; want them coalesced")
	default:
	}
	if c.Snapshot().Meter.Turns != 2 {
		t.Fatal("snapshot not the latest")
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("value after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

// The fake reviewer records proposals and accepts every hunk unless told
// otherwise.
func TestFakeReview(t *testing.T) {
	var c fake.Core
	p := surface.EditProposal{Tool: "edit_file", Path: "a.go", Hunks: make([]surface.Hunk, 3)}
	if d := c.Review(context.Background(), p); len(d.Apply) != 3 || !d.Apply[0] || !d.Apply[2] || d.Always {
		t.Fatalf("default decision %+v", d)
	}
	c.Decide = func(p surface.EditProposal) surface.EditDecision {
		return surface.EditDecision{Apply: []bool{true, false, true}}
	}
	if d := c.Review(context.Background(), p); d.Apply[1] {
		t.Fatalf("scripted decision ignored: %+v", d)
	}
	if len(c.Proposals) != 2 || c.Proposals[1].Path != "a.go" {
		t.Fatalf("proposals recorded: %+v", c.Proposals)
	}
}
