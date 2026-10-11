package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/bgeval"
	"github.com/rajasatyajit/ternly/internal/discover"
)

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(must(os.ReadFile(p)), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The answer is saved under routing.background_eval.enabled; every other
// setting is kept, whatever form "routing" had.
func TestSetBackgroundEvalKeepsConfig(t *testing.T) {
	for name, in := range map[string]string{
		"missing file":      "",
		"no routing":        `{"mode":"edits","providers":[{"id":"x"}]}`,
		"string routing":    `{"mode":"edits","routing":"v1"}`,
		"object routing":    `{"routing":{"version":"v2","time_value_usd_per_hour":20}}`,
		"other eval limits": `{"routing":{"background_eval":{"runs_per_week":4}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "ternly", "config.json")
			if in != "" {
				_ = os.MkdirAll(filepath.Dir(p), 0o700)
				_ = os.WriteFile(p, []byte(in), 0o600)
			}
			if err := setBackgroundEval(p, true); err != nil {
				t.Fatal(err)
			}
			var fc fileConfig
			if err := json.Unmarshal(must(os.ReadFile(p)), &fc); err != nil {
				t.Fatal(err)
			}
			if e := fc.Routing.BackgroundEval.Enabled; e == nil || !*e {
				t.Fatalf("enabled %v", e)
			}
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v", fi.Mode())
			}
			got := readJSON(t, p)
			switch name {
			case "no routing":
				if got["mode"] != "edits" || got["providers"] == nil {
					t.Fatalf("lost settings: %v", got)
				}
			case "string routing":
				if fc.Routing.Version != "v1" || got["mode"] != "edits" {
					t.Fatalf("routing %+v", fc.Routing)
				}
			case "object routing":
				if fc.Routing.Version != "v2" || fc.Routing.TimeValue == nil || *fc.Routing.TimeValue != 20 {
					t.Fatalf("routing %+v", fc.Routing)
				}
			case "other eval limits":
				if r := fc.Routing.BackgroundEval.RunsPerWeek; r == nil || *r != 4 {
					t.Fatalf("runs_per_week lost: %+v", fc.Routing.BackgroundEval)
				}
			}
		})
	}
	// a broken config isn't overwritten
	p := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(p, []byte(`{"mode":`), 0o600)
	if err := setBackgroundEval(p, false); err == nil || string(must(os.ReadFile(p))) != `{"mode":` {
		t.Fatalf("broken config: %v", err)
	}
	// a symlinked config stays a link
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles.json")
	_ = os.WriteFile(real, []byte(`{}`), 0o600)
	link := filepath.Join(dir, "config.json")
	_ = os.Symlink(real, link)
	if err := setBackgroundEval(link, false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 || !strings.Contains(string(must(os.ReadFile(real))), `"enabled": false`) {
		t.Fatal("the link was replaced")
	}
}

func TestAskBackgroundEval(t *testing.T) {
	for in, want := range map[string]struct{ on, saved bool }{
		"y\n": {true, true}, "Yes\n": {true, true}, "\n": {false, true}, "n\n": {false, true}, "maybe\n": {false, true},
		"": {false, false}, // EOF: no answer, nothing saved, asked again next time
	} {
		p := filepath.Join(t.TempDir(), "config.json")
		var out strings.Builder
		on, saved, err := askBackgroundEval(p, strings.NewReader(in+"rest for the UI"), &out)
		if err != nil || on != want.on || saved != want.saved {
			t.Errorf("%q: on %v saved %v err %v", in, on, saved, err)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("question %q", out.String())
		}
		if _, err := os.Stat(p); (err == nil) != want.saved {
			t.Errorf("%q: config written %v", in, err == nil)
		}
	}
	// only the answer's line is read: what follows stays for the UI
	r := strings.NewReader("y\nnext")
	_, _, _ = askBackgroundEval(filepath.Join(t.TempDir(), "c.json"), r, io.Discard)
	if rest, _ := io.ReadAll(r); string(rest) != "next" {
		t.Fatalf("read past the answer: %q left", rest)
	}
}

// Asked only with no explicit value, on a terminal, outside harnesses, CI
// and evaluations; the owner's explicit value is never asked about.
func TestShouldAsk(t *testing.T) {
	f := false
	none := func(string) string { return "" }
	if !shouldAsk(fileConfig{}, none, true) {
		t.Fatal("not asked on a terminal with no value set")
	}
	set := fileConfig{}
	set.Routing.BackgroundEval.Enabled = &f
	if shouldAsk(set, none, true) {
		t.Fatal("asked with an explicit value")
	}
	for _, k := range []string{"TERNLY_HARNESS", "TERNLY_BACKGROUND_EVAL", "TERNLY_EVAL", "CI"} {
		if shouldAsk(fileConfig{}, func(e string) string { return map[bool]string{true: "1"}[e == k] }, true) {
			t.Fatalf("asked with %s set", k)
		}
	}
	if shouldAsk(fileConfig{}, none, false) {
		t.Fatal("asked without a terminal (headless)")
	}
	r, w, _ := os.Pipe() // a pipe isn't a terminal
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) || isTerminal(nil) {
		t.Fatal("a pipe counted as a terminal")
	}
}

// The quota gate: a mark in the log or routing's own exhaustion means no
// room; Ollama's balance is asked (and cached) only for its cloud models.
func TestQuotaGate(t *testing.T) {
	log := discover.OpenQuotaLog(filepath.Join(t.TempDir(), "quota.json"))
	r := discover.NewRouter()
	ollama := &discover.Provider{ID: "ollama", Kind: "ollama", Local: true}
	cloud := &discover.Model{ProvID: "ollama", ID: "glm-5.3:cloud", Cloud: true, Provider: ollama}
	local := &discover.Model{ProvID: "ollama", ID: "qwen3.6", Provider: ollama}
	r.SetModels([]*discover.Model{cloud, local})
	asked := 0
	used := 5.0
	gate := quotaGate(log, r, func(context.Context) (*discover.Quota, error) {
		asked++
		return &discover.Quota{Used: used, Limit: 20, Unit: "USD", ResetsAt: time.Now().Add(72 * time.Hour), Source: "test"}, nil
	})
	if ok, why := gate(cloud); !ok {
		t.Fatalf("room in the balance: %s", why)
	}
	if ok, _ := gate(local); !ok {
		t.Fatal("a local model has no quota")
	}
	used = 20
	if ok, _ := gate(cloud); !ok || asked != 1 {
		t.Fatalf("balance not cached: asked %d", asked)
	}
	gate = quotaGate(log, r, func(context.Context) (*discover.Quota, error) {
		return &discover.Quota{Used: 20, Limit: 20, ResetsAt: time.Now().Add(72 * time.Hour), Source: "test"}, nil
	})
	if ok, why := gate(cloud); ok || !strings.Contains(why, "allowance spent") {
		t.Fatalf("spent balance: %v %q", ok, why)
	}
	if ok, _ := log.Exhausted("ollama:cloud", time.Now().Add(48*time.Hour)); !ok {
		t.Fatal("a spent balance wasn't recorded until its reset")
	}
	// no balance (no key): the log decides; a 429 recorded by any process counts
	log2 := discover.OpenQuotaLog(filepath.Join(t.TempDir(), "quota.json"))
	gate = quotaGate(log2, r, nil)
	if ok, _ := gate(cloud); !ok {
		t.Fatal("no hit, no balance: room")
	}
	_ = log2.Mark("ollama:cloud", cloud.Key(), time.Now(), time.Now().Add(time.Hour), "429")
	if ok, why := gate(cloud); ok || !strings.Contains(why, "quota exhausted") {
		t.Fatalf("after a 429: %v %q", ok, why)
	}
	// routing's own exhaustion (this session's 429)
	r2 := discover.NewRouter()
	r2.SetModels([]*discover.Model{cloud})
	r2.MarkExhausted(cloud.Key(), time.Now().Add(time.Hour), false)
	if ok, why := quotaGate(discover.OpenQuotaLog(""), r2, nil)(cloud); ok || !strings.Contains(why, "rate limited until ") {
		t.Fatalf("routing has it exhausted (Retry-After): %v %q", ok, why)
	}
	r2.MarkExhausted(cloud.Key(), time.Now().Add(time.Hour), true)
	if ok, why := quotaGate(discover.OpenQuotaLog(""), r2, nil)(cloud); ok || !strings.Contains(why, "retried in about 1h, estimated") || strings.Contains(why, "until") {
		t.Fatalf("routing has it exhausted (estimated): %v %q", ok, why)
	}
}

type fakeUI struct{ updates int }

func (f *fakeUI) Init() tea.Cmd                       { return nil }
func (f *fakeUI) Update(tea.Msg) (tea.Model, tea.Cmd) { f.updates++; return f, nil }
func (f *fakeUI) View() tea.View                      { return tea.NewView("") }

// Key presses and pastes reach the idle gate; other messages don't.
func TestActivityTouches(t *testing.T) {
	touched := 0
	var m tea.Model = activity{Model: &fakeUI{}, touch: func() { touched++ }}
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'a'}, tea.PasteMsg{Content: "x"}, tea.WindowSizeMsg{Width: 80}} {
		m, _ = m.Update(msg)
	}
	if touched != 2 || m.(activity).Model.(*fakeUI).updates != 3 {
		t.Fatalf("touched %d", touched)
	}
}

// The driver gets "p" and "r" on its fd 3; exit code 75 is a quota stop.
func TestRunPausable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "control")
	cmd := exec.Command("sh", "-c", `cat <&3 > "$0"`, out)
	pause := make(chan bool)
	done := make(chan error, 1)
	go func() { done <- runPausable(cmd, pause) }()
	pause <- true
	pause <- false
	time.Sleep(50 * time.Millisecond)
	_ = cmd.Process.Kill() // cat would wait for more
	<-done
	if got := string(must(os.ReadFile(out))); got != "pr" {
		t.Fatalf("control bytes %q", got)
	}
	if err := runPausable(exec.Command("sh", "-c", "exit 75"), nil); !errors.Is(err, bgeval.ErrQuota) {
		t.Fatalf("exit 75: %v", err)
	}
	if err := runPausable(exec.Command("sh", "-c", "exit 1"), nil); err == nil || errors.Is(err, bgeval.ErrQuota) {
		t.Fatalf("exit 1: %v", err)
	}
}

// The driver's control: a pause cancels the trap in progress; the next trap
// waits for resume; the pipe's end means the session is gone.
func TestEvalControl(t *testing.T) {
	r, w := io.Pipe()
	c := newEvalControl(r)
	ctx, ok := c.trap()
	if !ok {
		t.Fatal("gone at start")
	}
	_, _ = w.Write([]byte("p"))
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("pause didn't stop the trap")
	}
	if !c.paused(ctx) {
		t.Fatal("not reported as paused")
	}
	got := make(chan bool, 1)
	go func() { _, ok := c.trap(); got <- ok }()
	select {
	case <-got:
		t.Fatal("a trap started while paused")
	case <-time.After(30 * time.Millisecond):
	}
	_, _ = w.Write([]byte("r"))
	if !<-got {
		t.Fatal("not resumed")
	}
	ctx, _ = c.trap()
	w.Close()
	<-ctx.Done()
	if _, ok := c.trap(); ok {
		t.Fatal("went on after the session was gone")
	}
	// a foreground --eval never pauses
	f := newEvalControl(nil)
	ctx, ok = f.trap()
	if !ok || f.paused(ctx) {
		t.Fatal("foreground control")
	}
}
