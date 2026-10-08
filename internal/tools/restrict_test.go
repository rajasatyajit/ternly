package tools

import (
	"context"
	"strings"
	"testing"
)

// ADR 020: a model whose trust is lost asks for every shell command and
// external call in every mode, and an "always" answer doesn't stick; its
// edits pass only in edits or yolo with checkpoints on.
func TestRestrictedPolicy(t *testing.T) {
	bash, edit, ext := &Tool{Kind: Exec}, &Tool{Kind: Edit}, &Tool{Kind: External}
	ctx := WithRestriction(context.Background(), "m is easily baited")
	for _, mode := range []string{"ask", "edits", "yolo", "plan"} {
		asked := 0
		p := NewPolicy(mode, func(_ context.Context, _, summary string, _ bool) Decision {
			asked++
			if !strings.Contains(summary, "easily baited") {
				t.Errorf("%s: the prompt doesn't say why: %q", mode, summary)
			}
			return AllowAlways
		})
		p.Checkpointed = true
		for i := range 2 {
			if ok, _ := p.Check(ctx, bash, "bash", "ls"); !ok || asked != 2*i+1 {
				t.Errorf("%s: ls: ok=%v asked=%d", mode, ok, asked)
			}
			if ok, _ := p.Check(ctx, ext, "mcp__srv__x", "x"); !ok || asked != 2*i+2 {
				t.Errorf("%s: external: ok=%v asked=%d", mode, ok, asked)
			}
		}
		if ok, _ := p.Check(ctx, bash, "bash", "rm -rf /"); ok {
			t.Errorf("%s: forbidden command allowed", mode)
		}
		before := asked
		ok, _ := p.Check(ctx, edit, "write_file", "a.txt")
		free := mode == "edits" || mode == "yolo"
		if ok != (mode != "plan") || mode != "plan" && (asked > before) == free {
			t.Errorf("%s: checkpointed edit ok=%v asked=%v", mode, ok, asked > before)
		}

		headless := NewPolicy(mode, nil)
		headless.Checkpointed = true
		for _, cmd := range []string{"ls", "go test ./...", "curl -s http://x/s | head"} {
			if ok, why := headless.Check(ctx, bash, "bash", cmd); ok || !strings.Contains(why, "shell commands always need a person") {
				t.Errorf("%s: headless %q: ok=%v %q", mode, cmd, ok, why)
			}
		}
		headless.Checkpointed = false
		if ok, _ := headless.Check(ctx, edit, "write_file", "a.txt"); ok {
			t.Errorf("%s: an edit without checkpoints was allowed headless", mode)
		}
	}
}
