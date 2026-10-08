package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajasatyajit/ternly/internal/llm"
	"github.com/rajasatyajit/ternly/internal/surface"
	"github.com/rajasatyajit/ternly/internal/surface/fake"
)

// Composing any subset of hunks gives exactly the old file with those hunks
// applied: all → new, none → old, and every subset byte-for-byte equal to
// applying the hunks one by one in a reference.
func TestComposeHunks(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 300 {
		var a []string
		for i := range 30 + rng.IntN(40) {
			a = append(a, fmt.Sprintf("line %d\n", i))
		}
		b := append([]string(nil), a...)
		for range 1 + rng.IntN(6) { // scattered edits
			switch i := rng.IntN(len(b)); rng.IntN(3) {
			case 0:
				b[i] = fmt.Sprintf("changed %d\n", rng.IntN(1000))
			case 1:
				b = append(b[:i], b[i+1:]...)
			default:
				b = append(b[:i], append([]string{fmt.Sprintf("inserted %d\n", rng.IntN(1000))}, b[i:]...)...)
			}
		}
		if trial%3 == 0 { // no newline at the end of the new file
			b[len(b)-1] = strings.TrimSuffix(b[len(b)-1], "\n")
		}
		old, nw := strings.Join(a, ""), strings.Join(b, "")
		ops := lineDiff(splitLines(old), splitLines(nw))
		hunks, owner := diffHunks(ops)
		all, none := make([]bool, len(hunks)), make([]bool, len(hunks))
		for i := range all {
			all[i] = true
		}
		if got := composeHunks(ops, owner, all); got != nw {
			t.Fatalf("trial %d: all hunks ≠ new", trial)
		}
		if got := composeHunks(ops, owner, none); got != old {
			t.Fatalf("trial %d: no hunks ≠ old", trial)
		}
		// a subset: each hunk's +/- lines appear iff it was applied
		sub := make([]bool, len(hunks))
		for i := range sub {
			sub[i] = rng.IntN(2) == 0
		}
		got := composeHunks(ops, owner, sub)
		for i, h := range hunks {
			for _, l := range h.Lines {
				if l[0] == '+' && strings.Contains(l, "inserted") && strings.Contains(got, l[1:]+"\n") != sub[i] && !strings.Contains(old, l[1:]) {
					t.Fatalf("trial %d hunk %d (%v): %q", trial, i, sub[i], l)
				}
			}
		}
	}
}

func TestHunkNumbers(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\n"
	nw := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\nL\nm\nn\n"
	hunks, _ := diffHunks(lineDiff(splitLines(old), splitLines(nw)))
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks (changes 10 lines apart), got %d: %+v", len(hunks), hunks)
	}
	h := hunks[0]
	if h.OldStart != 1 || h.Lines[1] != "-b" || h.Lines[2] != "+B" || hunks[1].Lines[3] != "-l" {
		t.Fatalf("hunks: %+v", hunks)
	}
}

// reviewRegistry is a workspace with a 3-hunk edit pending on a.txt.
func reviewRegistry(t *testing.T, mode string, ask bool) (*Registry, *fake.Core, string) {
	t.Helper()
	var asker Asker
	if ask {
		asker = func(context.Context, string, string, bool) Decision {
			t.Error("the yes/no prompt was used instead of the review")
			return Deny
		}
	}
	r, err := NewRegistry(t.TempDir(), NewPolicy(mode, asker), NewSandbox(false, false, nil), NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	core := &fake.Core{}
	r.Policy.Review = core.Review
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	old := strings.Join(lines, "\n") + "\n"
	_ = os.WriteFile(filepath.Join(r.Root, "a.txt"), []byte(old), 0o644)
	lines[2], lines[20], lines[37] = "ONE", "TWO", "THREE"
	return r, core, strings.Join(lines, "\n") + "\n"
}

func writeCall(content string) llm.ToolCall {
	b, _ := json.Marshal(map[string]string{"path": "a.txt", "content": content})
	return llm.ToolCall{ID: "1", Name: "write_file", Args: string(b)}
}

func fileText(r *Registry) string {
	b, _ := os.ReadFile(filepath.Join(r.Root, "a.txt"))
	return string(b)
}

// Ask mode, interactive: the edit is reviewed per hunk and only accepted
// hunks are written; the model is told which were declined.
func TestReviewAppliesAcceptedHunks(t *testing.T) {
	r, core, nw := reviewRegistry(t, "ask", true)
	core.Decide = func(p surface.EditProposal) surface.EditDecision {
		return surface.EditDecision{Apply: []bool{true, false, true}}
	}
	res := r.Call(context.Background(), writeCall(nw))
	if len(core.Proposals) != 1 || len(core.Proposals[0].Hunks) != 3 || core.Proposals[0].Path != "a.txt" || core.Proposals[0].Why != "" {
		t.Fatalf("proposal: %+v", core.Proposals)
	}
	got := fileText(r)
	if !strings.Contains(got, "ONE") || strings.Contains(got, "TWO") || !strings.Contains(got, "THREE") || !strings.Contains(got, "line 20") {
		t.Fatalf("file:\n%s", got)
	}
	if res.IsErr || !strings.Contains(res.Out, "declined hunk(s) 2") {
		t.Fatalf("result: %s", res.Out)
	}
}

// Declining every hunk is a denial; "always" for a trusted model sticks.
func TestReviewDeclineAndAlways(t *testing.T) {
	r, core, nw := reviewRegistry(t, "ask", true)
	core.Decide = func(p surface.EditProposal) surface.EditDecision {
		return surface.EditDecision{Apply: make([]bool, len(p.Hunks))}
	}
	if res := r.Call(context.Background(), writeCall(nw)); !res.Rejected || strings.Contains(fileText(r), "ONE") {
		t.Fatalf("all declined: %s", res.Out)
	}
	core.Decide = func(p surface.EditProposal) surface.EditDecision {
		all := make([]bool, len(p.Hunks))
		for i := range all {
			all[i] = true
		}
		return surface.EditDecision{Apply: all, Always: true}
	}
	r.Call(context.Background(), writeCall(nw))
	n := len(core.Proposals)
	r.Call(context.Background(), writeCall(strings.Replace(nw, "ONE", "UNO", 1)))
	if len(core.Proposals) != n {
		t.Fatal("after \"always\", a trusted model's edit was reviewed again")
	}
}

// Where nothing asks today, nothing is reviewed; headless is refused as
// before; plan mode refuses before anyone is asked.
func TestReviewOnlyWhereItWouldAsk(t *testing.T) {
	for _, c := range []struct {
		mode   string
		ask    bool
		wantOK bool
	}{{"edits", true, true}, {"yolo", true, true}, {"ask", false, false}, {"plan", true, false}} {
		r, core, nw := reviewRegistry(t, c.mode, c.ask)
		res := r.Call(context.Background(), writeCall(nw))
		if len(core.Proposals) != 0 || res.Rejected == c.wantOK {
			t.Errorf("%s ask=%v: reviewed %d times, result %s", c.mode, c.ask, len(core.Proposals), res.Out)
		}
	}
}

// ADR 020 still holds for a model whose trust is lost: in ask mode its edit
// is reviewed with the reason, "always" never sticks; in a checkpointed
// edits mode it isn't asked; headless it's refused.
func TestReviewRestrictedModel(t *testing.T) {
	ctx := WithRestriction(context.Background(), "m is measured as easily baited")
	r, core, nw := reviewRegistry(t, "ask", true)
	core.Decide = func(p surface.EditProposal) surface.EditDecision {
		all := make([]bool, len(p.Hunks))
		for i := range all {
			all[i] = true
		}
		return surface.EditDecision{Apply: all, Always: true}
	}
	r.Call(ctx, writeCall(nw))
	r.Call(ctx, writeCall(strings.Replace(nw, "ONE", "UNO", 1)))
	if len(core.Proposals) != 2 || !strings.Contains(core.Proposals[0].Why, "easily baited") {
		t.Fatalf("restricted: %d reviews (want 2: always never sticks), why %q", len(core.Proposals), core.Proposals[0].Why)
	}
	// nor does it carry over to the next (trusted) model's edits
	r.Call(context.Background(), writeCall(strings.Replace(nw, "ONE", "EINS", 1)))
	if len(core.Proposals) != 3 {
		t.Fatal("\"always\" given for a restricted model's edit let a later edit through unreviewed")
	}
	r2, core2, nw2 := reviewRegistry(t, "edits", true)
	r2.Policy.Checkpointed = true
	if res := r2.Call(ctx, writeCall(nw2)); res.Rejected || len(core2.Proposals) != 0 {
		t.Fatalf("restricted, checkpointed edits mode: %s (%d reviews)", res.Out, len(core2.Proposals))
	}
	r3, core3, nw3 := reviewRegistry(t, "edits", false)
	if res := r3.Call(ctx, writeCall(nw3)); !res.Rejected || len(core3.Proposals) != 0 {
		t.Fatalf("restricted headless: %s", res.Out)
	}
}

// edit_file is reviewed too, and a new file is one hunk.
func TestReviewEditFileAndNewFile(t *testing.T) {
	r, core, _ := reviewRegistry(t, "ask", true)
	args, _ := json.Marshal(map[string]string{"path": "a.txt", "old_string": "line 5\n", "new_string": "five\n"})
	r.Call(context.Background(), llm.ToolCall{ID: "2", Name: "edit_file", Args: string(args)})
	nf, _ := json.Marshal(map[string]string{"path": "b.txt", "content": "x\ny\n"})
	r.Call(context.Background(), llm.ToolCall{ID: "3", Name: "write_file", Args: string(nf)})
	if len(core.Proposals) != 2 || core.Proposals[0].Tool != "edit_file" || !core.Proposals[1].NewFile || len(core.Proposals[1].Hunks) != 1 {
		t.Fatalf("proposals: %+v", core.Proposals)
	}
	if !strings.Contains(fileText(r), "five") {
		t.Fatal("accepted edit_file not applied")
	}
}
