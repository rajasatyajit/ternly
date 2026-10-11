// Command layout records ternly's screens for the F2 layout work (ADR 030 (d))
// and measures each step: recordings at several sizes and render tiers, the
// information-reach count (the 20 facts of bench/f2/SPEC.md), and the
// performance numbers against ADR 030's budgets.
//
//	go run ./bench/f2/layout -ternly ./ternly -probe tuiprobe -out DIR
//
// It drives ternly through bench/tuiprobe (a pseudo-terminal with a VT
// emulator) against bench/fakeprovider playing a fixed script, in a fresh
// HOME and a fixture repository, so every run sees the same session:
//
//   - turn 1 reads lru.go, edits Resize to evict until the cache fits, and
//     answers; ternly verifies the edit (go build, go vet);
//   - the probes: each key sequence that might show a fact, fewest keys first,
//     each closed with Esc;
//   - turn 2 fails (HTTP 500 with a marked message): the last error.
//
// A fact's count is the fewest keys after which its value is on the screen:
// 0 is the idle screen after turn 1 (the error screen for the last error).
// Values, not labels, are matched: the branch name, the edited line in a
// diff, a token count scoped to the turn. Where the session never reaches a
// state (a plan, sub-agents, an approval prompt) the fact is n/o, and the
// quota is n/a for a keyless provider, as in the survey's matrix.
//
// Everything a run starts is stopped by its HOME (tuiprobe -reap).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rajasatyajit/ternly/bench/leakguard"
	"github.com/rajasatyajit/ternly/internal/gitenv"
)

type size struct{ cols, rows int }

type tier struct {
	name string
	env  []string
}

var tiers = []tier{
	{"unicode", []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LANG=C.UTF-8"}},
	{"ascii-nocolor", []string{"TERM=xterm", "COLORTERM=", "LANG=C", "LC_ALL=C", "NO_COLOR=1"}},
}

// A probe is a key sequence typed from the idle prompt; keys counts it as the
// survey did (a chord is 1, "/diff⏎" is 6).
type probe struct {
	name, seq string
	keys      int
}

var probes = []probe{
	{"ctrl-b", `\x02`, 1}, {"ctrl-o", `\x0f`, 1}, {"ctrl-g", `\x07`, 1}, {"ctrl-e", `\x05`, 1},
	{"why", `/why\r`, 5}, {"diff", `/diff\r`, 6}, {"cost", `/cost\r`, 6},
	{"status", `/status\r`, 8}, {"rewind", `/rewind\r`, 8}, {"verify", `/verify\r`, 8}, {"agents", `/agents\r`, 8},
}

// fact is one of the 20 facts and how its value is recognised on a screen.
type fact struct {
	n    int
	name string
	re   *regexp.Regexp
	na   string // "n/a" or "n/o" when the scripted session can't show it
	snap string // the screen it is looked for at 0 keys (default "mid": idle after turn 1)
}

func rx(s string) *regexp.Regexp { return regexp.MustCompile(s) }

var facts = []fact{
	{1, "current model", rx(`(fake|ollama)/m1`), "", ""},
	{2, "why this model", rx(`(?m)(fake|ollama)/m1[^\n]*(cheapest|only capable|only model|pinned|escalat|failover|because|chosen)|p +time +money +quota +score`), "", ""},
	{3, "cost so far", rx(`\$\d+\.\d+|cost:? ?unknown|\$\?`), "", ""},
	{4, "quota remaining", rx(`quota ?:?(out|✗|x)\b|quota✗|quota ?\d+%|\bq\d+%`), "", "error"}, // the 429 turn: out until the reset
	{5, "context used / window", rx(`ctx ?\d+(\.\d+)?[kM]?/\d+(\.\d+)?[kM]?|ctx ?\d+%`), "", ""},
	{6, "tokens this turn", rx(`(↳|turn|this turn)[^\n]*\d+(\.\d+)?k?\s*(↑|\^|tok|in)|\d+(\.\d+)?k?(↑|\^) ?\d+(\.\d+)?k?(↓|v)[^\n]*(·|\|)[^\n]*\d+(\.\d+)?s`), "", ""},
	{7, "files changed", rx(`\d+ files? changed`), "", ""},
	{8, "the diff of a change", rx(`(?m)^\s*(\d+\s+)?\+.*c\.evictUntilFits\(\)`), "", ""},
	{9, "current plan step", nil, "n/o", ""}, // plan data: deferred (ADR 030)
	{10, "the whole plan", nil, "n/o", ""},
	{11, "running agents", rx(`(?m)(●|\*|○|o) (project:)?(lexer|tests)\b[^\n]*(step|·|\|)|\d+ agents: [^\n]*(lexer|tests)`), "", "agents"},
	{12, "last error", rx(`SCRIPTED-FAILURE`), "", "error"},
	{13, "verify / test status", rx(`(?i)verif(y|ied)[^\n]*(go build|go vet|✓|✗|ok|pass|fail)|verified|verify (✓|✗|ok)`), "", ""},
	{14, "session name / id", rx(`Scripted Title`), "", ""},
	{15, "permission mode", rx(`\byolo\b`), "", ""},
	{16, "pending approval", rx(`(Allow|Edit|Create) [^\n]*\?[\s\S]*why:`), "", "approval"},
	{17, "git branch / dirty", rx(`fix-resize-branch`), "", ""},
	{18, "turn elapsed", rx(`(?m)^[^\n]*(↑|\^)[^\n]*\b(\d+m)?\d+(\.\d+)?s\b|^[^\n]*\b(\d+m)?\d+(\.\d+)?s\b[^\n]*(↑|\^)`), "", ""},
	{19, "undo / checkpoints", rx(`(?i)undo ?\d|\bu\d\b|restores to before turn`), "", ""},
	{20, "workspace / cwd", rx(`proj-lru`), "", ""},
}

func main() {
	ternly := flag.String("ternly", "./ternly", "the ternly binary")
	probeBin := flag.String("probe", "tuiprobe", "the bench/tuiprobe binary")
	fake := flag.String("fake", "", "the bench/fakeprovider binary (default: built from this checkout)")
	out := flag.String("out", "", "output directory")
	sizes := flag.String("sizes", "200x50,120x40,80x24", "terminal sizes")
	only := flag.String("tiers", "unicode,ascii-nocolor", "render tiers")
	perf := flag.Bool("perf", true, "also run the performance probe at 120x40")
	ab := flag.String("idle-ab", "", "only measure idle CPU of these binaries, interleaved: name=binary,name=binary")
	rounds := flag.Int("rounds", 7, "-idle-ab: rounds")
	secs := flag.Int("idle-secs", 20, "-idle-ab: seconds of idle per sample")
	flag.Parse()
	if *out == "" {
		fail("-out is required")
	}
	must(os.MkdirAll(*out, 0o755))
	abs := func(p string) string {
		if lp, err := exec.LookPath(p); err == nil {
			p = lp
		}
		a, err := filepath.Abs(p)
		must(err)
		return a
	}
	*ternly, *probeBin = abs(*ternly), abs(*probeBin)
	if *fake == "" {
		*fake = filepath.Join(*out, "fakeprovider")
		build := exec.Command("go", "build", "-o", *fake, "./bench/fakeprovider")
		build.Stderr = os.Stderr
		must(build.Run())
	}
	*fake = abs(*fake)
	if *ab != "" {
		rep := fmt.Sprintf("# Idle CPU A/B (%s)\n\n", time.Now().Format("2006-01-02 15:04")) + idleAB(*ab, *rounds, *secs, *probeBin, *fake, *out, abs)
		must(os.WriteFile(filepath.Join(*out, "REPORT.md"), []byte(rep), 0o644))
		privacyCheck(*out)
		fmt.Print(rep)
		return
	}

	var report strings.Builder
	fmt.Fprintf(&report, "# Layout recordings (%s)\n\nternly: `%s`\n\n", time.Now().Format("2006-01-02 15:04"), version(*ternly))
	counts := map[string]map[int]string{} // run → fact → cell
	var runs []string
	for _, sz := range strings.Split(*sizes, ",") {
		if sz == "" { // -sizes "": the performance run only
			continue
		}
		var s size
		if _, err := fmt.Sscanf(sz, "%dx%d", &s.cols, &s.rows); err != nil {
			fail("bad size " + sz)
		}
		for _, t := range tiers {
			if !strings.Contains(","+*only+",", ","+t.name+",") {
				continue
			}
			name := fmt.Sprintf("%dx%d-%s", s.cols, s.rows, t.name)
			fmt.Fprintln(os.Stderr, "run", name)
			res := session(*ternly, *probeBin, *fake, filepath.Join(*out, name), s, t)
			counts[name] = reach(res)
			runs = append(runs, name)
			if t.name == "ascii-nocolor" {
				fmt.Fprintf(&report, "- `%s`: %s\n", name, asciiCheck(res))
			}
		}
	}
	report.WriteString("\n## Information reach (keys to see each fact; 0 = on the idle screen)\n\n| # | Fact |")
	for _, r := range runs {
		report.WriteString(" " + r + " |")
	}
	report.WriteString("\n|---|---|" + strings.Repeat("---|", len(runs)) + "\n")
	for _, f := range facts {
		fmt.Fprintf(&report, "| %d | %s |", f.n, f.name)
		for _, r := range runs {
			report.WriteString(" " + counts[r][f.n] + " |")
		}
		report.WriteString("\n")
	}
	for _, line := range []struct {
		label string
		ok    func(string) bool
	}{
		{"at 0 keys", func(c string) bool { return c == "0" }},
		{"at ≤ 1 key", func(c string) bool { return c == "0" || c == "1" }},
		{"reachable at all", func(c string) bool { return c != "∞" && c != "n/o" && c != "n/a" }},
		{"∞ (not in the UI)", func(c string) bool { return c == "∞" }},
	} {
		fmt.Fprintf(&report, "| | **%s** |", line.label)
		for _, r := range runs {
			n := 0
			for _, c := range counts[r] {
				if line.ok(c) {
					n++
				}
			}
			fmt.Fprintf(&report, " %d |", n)
		}
		report.WriteString("\n")
	}
	if *perf {
		report.WriteString("\n" + perfRun(*ternly, *probeBin, *fake, filepath.Join(*out, "perf")))
	}
	must(os.WriteFile(filepath.Join(*out, "REPORT.md"), []byte(report.String()), 0o644))
	privacyCheck(*out)
	fmt.Print(report.String())
}

const lruGo = `package lru

// Cache keeps at most cap items, oldest first.
type Cache struct {
	items []int
	cap   int
}

// Resize changes the capacity, evicting the oldest items that no longer fit.
func (c *Cache) Resize(n int) { c.cap = n; c.evictOne() }

func (c *Cache) evictOne() {
	if len(c.items) > c.cap {
		c.items = c.items[1:]
	}
}

func (c *Cache) evictUntilFits() {
	for len(c.items) > c.cap {
		c.items = c.items[1:]
	}
}
`

// script is the session, turn by turn:
//  1. read lru.go, edit Resize, answer (ternly verifies the edit);
//  2. two subagents at once (lexer, tests), each reading a file slowly, then
//     an answer;
//  3. in ask mode, an edit that waits for a yes (the per-hunk review);
//  4. HTTP 429 with Retry-After: 3600, the quota out for an hour.
//
// The subagents' steps come from their own queues (their markers are in their
// definitions, which become their system prompts).
func script(dir string) string {
	edit, _ := json.Marshal(map[string]string{"path": "lru.go",
		"old_string": "c.cap = n; c.evictOne()", "new_string": "c.cap = n; c.evictUntilFits()"})
	rename, _ := json.Marshal(map[string]string{"path": "lru.go",
		"old_string": "func (c *Cache) evictOne()", "new_string": "func (c *Cache) evictFirst()"})
	quota := map[string]any{"status": 429, "headers": map[string]string{"Retry-After": "3600"},
		"body": `{"error":{"message":"SCRIPTED-FAILURE: rate limit reached for m1; try again in an hour"}}`}
	steps := []any{
		map[string]any{"call": []string{"read_file", `{"path":"lru.go"}`}, "usage": []int{1180, 24}},
		map[string]any{"call": []string{"edit_file", string(edit)}, "usage": []int{1420, 61}},
		map[string]any{"text": "Fixed: Resize now evicts until the cache fits, not just one entry.", "chunks": 8, "delay_ms": 25, "usage": []int{1650, 38}},
		map[string]any{"calls": [][2]string{{"task", `{"agent":"lexer","prompt":"Find every caller of the eviction helpers."}`},
			{"task", `{"agent":"tests","prompt":"Say which tests cover Resize."}`}}, "usage": []int{1900, 70}},
		map[string]any{"text": "Both agents reported back: the callers are fixed and no test covers Resize yet.", "chunks": 4, "delay_ms": 25, "usage": []int{2300, 40}},
		map[string]any{"call": []string{"edit_file", string(rename)}, "usage": []int{2500, 50}},
		map[string]any{"text": "Edited after your yes.", "usage": []int{2600, 12}},
	}
	for range 4 { // turn 4, and any retry
		steps = append(steps, quota)
	}
	agents := map[string]any{
		"LANE-LEXER-7Q": []any{
			map[string]any{"call": []string{"read_file", `{"path":"lru.go"}`}, "delay_ms": 1800, "usage": []int{900, 20}},
			map[string]any{"text": "Callers: Resize uses evictUntilFits.", "delay_ms": 600, "usage": []int{1100, 18}},
		},
		"LANE-TESTS-7Q": []any{
			map[string]any{"call": []string{"read_file", `{"path":"go.mod"}`}, "delay_ms": 2200, "usage": []int{800, 16}},
			map[string]any{"text": "No test covers Resize.", "delay_ms": 600, "usage": []int{950, 14}},
		},
	}
	b, _ := json.Marshal(map[string]any{"steps": steps, "agents": agents})
	p := filepath.Join(dir, "script.json")
	must(os.WriteFile(p, b, 0o644))
	return p
}

// fixture makes HOME and the workspace (proj-lru on branch fix-resize-branch,
// with an uncommitted file) and starts the fake provider.
// The fixture lives in its own directory under /var/tmp (not /tmp: the sandbox
// mounts a fresh one), never under this machine's home, and is removed
// afterwards: screens show ~/proj-lru and /var/tmp/ternly-layout-…, nothing
// personal. dir (the output) gets the script.
func fixture(fake, dir string) (home, ws string, env []string, stop func()) {
	must(os.MkdirAll(dir, 0o755)) // the script goes here
	root, err := os.MkdirTemp("/var/tmp", "ternly-layout-")
	must(err)
	home = filepath.Join(root, "home")
	ws = filepath.Join(home, "proj-lru") // under HOME: the header shows ~/proj-lru
	_ = os.RemoveAll(ws)
	cfg, cache, data := filepath.Join(home, ".config", "ternly"), filepath.Join(home, ".cache", "ternly"), filepath.Join(home, ".local", "share", "ternly")
	for _, d := range []string{cfg, cache, data, ws} {
		must(os.MkdirAll(d, 0o755))
	}
	cmd := exec.Command(fake, "-script", script(dir))
	stdout, err := cmd.StdoutPipe()
	must(err)
	must(cmd.Start())
	buf := make([]byte, 128)
	n, _ := stdout.Read(buf)
	url := strings.TrimSpace(string(buf[:n]))
	// ternly's own Ollama provider, pointed at the fake (OLLAMA_HOST below): its m1 is
	// an Ollama Cloud model, so the quota scenario (429) is Ollama Cloud's
	conf := `{"suggestions":false,"routing":{"background_eval":{"enabled":false}}}`
	must(os.WriteFile(filepath.Join(cfg, "config.json"), []byte(conf), 0o600))
	must(os.WriteFile(filepath.Join(cache, "catalog.json"), []byte(`{"m1":{"Tools":true,"Ctx":131072}}`), 0o600))
	must(os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/lru\n\ngo 1.26\n"), 0o644))
	must(os.WriteFile(filepath.Join(ws, "lru.go"), []byte(lruGo), 0o644))
	// two read-only agent definitions; the markers route their requests to their
	// own queues in the fake provider
	must(os.MkdirAll(filepath.Join(ws, ".claude", "agents"), 0o755))
	for name, marker := range map[string]string{"lexer": "LANE-LEXER-7Q", "tests": "LANE-TESTS-7Q"} {
		def := fmt.Sprintf("---\nname: %s\ndescription: Reads the code and reports on %s.\ntools: Read, Grep\n---\nYou are the %s agent (%s). Read what you need and report briefly.\n", name, name, name, marker)
		must(os.WriteFile(filepath.Join(ws, ".claude", "agents", name+".md"), []byte(def), 0o644))
	}
	git := func(args ...string) {
		c := gitenv.Command(context.Background(), append([]string{"-c", "user.name=f2", "-c", "user.email=f2@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = ws
		out, err := c.CombinedOutput()
		if err != nil {
			fail(fmt.Sprintf("git %v: %v\n%s", args, err, out))
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "lru")
	git("checkout", "-q", "-b", "fix-resize-branch")
	must(os.WriteFile(filepath.Join(ws, "NOTES.md"), []byte("scratch\n"), 0o644)) // dirty
	env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"), "XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"OLLAMA_HOST=" + strings.TrimSuffix(url, "/v1"), "TERNLY_HARNESS=1", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local",
		// nothing of this machine on the screens: system tools only (no CLIs
		// installed under the real home), a neutral user
		"PATH=/usr/bin:/bin", "USER=f2", "LOGNAME=f2"}
	return home, ws, env, func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = os.RemoveAll(root) }
}

type result struct {
	Snaps map[string]string `json:"snaps"`
	Steps []struct {
		Step   string    `json:"step"`
		OK     bool      `json:"ok"`
		Note   string    `json:"note"`
		Ms     float64   `json:"ms"`
		EchoMs []float64 `json:"echo_ms"`
		KeyB   []int     `json:"key_bytes"`
		CPUPct float64   `json:"cpu_percent"`
		RSSKiB int       `json:"rss_kib"`
	} `json:"steps"`
	Tech struct {
		TrueColor int `json:"sgr_truecolor"`
		Color256  int `json:"sgr_256"`
		Color16   int `json:"sgr_16"`
		NonASCII  int `json:"non_ascii_bytes"`
	} `json:"technique"`
}

func runProbe(probeBin, ternly, ws string, env []string, s size, steps []string, dir string) result {
	must(os.MkdirAll(dir, 0o755))
	outJSON, raw := filepath.Join(dir, "probe.json"), filepath.Join(dir, "raw.gz")
	args := []string{"-clean-env", "-cols", fmt.Sprint(s.cols), "-rows", fmt.Sprint(s.rows), "-env", strings.Join(env, ","),
		"-dir", ws, "-steps", strings.Join(steps, "; "), "-out", outJSON, "-raw", raw, "--", ternly, "--mode", "yolo"}
	c := exec.Command(probeBin, args...)
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tuiprobe:", err)
	}
	var r result
	if b, err := os.ReadFile(outJSON); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	for name, text := range r.Snaps {
		_ = os.WriteFile(filepath.Join(dir, "screen-"+name+".txt"), []byte(text), 0o644)
	}
	writeMetrics(outJSON, filepath.Join(dir, "metrics.json"))
	return r
}

func session(ternly, probeBin, fake, dir string, s size, t tier) result {
	must(os.MkdirAll(dir, 0o755))
	home, ws, env, stop := fixture(fake, dir)
	defer stop()
	defer reap(probeBin, home)
	steps := []string{"stable 1500 30000", "snap start",
		`type Resize evicts one entry. find the cause and fix it\r`,
		"until 60000 evicts\\suntil\\sthe\\scache\\sfits", "stable 1500 30000", "snap mid"}
	sort.SliceStable(probes, func(i, j int) bool { return probes[i].keys < probes[j].keys })
	for _, p := range probes {
		steps = append(steps, "type "+p.seq, "stable 700 15000", "snap k-"+p.name)
		if p.keys == 1 { // a panel key toggles: press it again to close what it opened
			steps = append(steps, "type "+p.seq, "sleep 400")
		}
		// Esc on an empty, idle prompt writes nothing, so "stable" would wait out
		// its limit (no output to settle): a short sleep is enough.
		steps = append(steps, `type \e`, "sleep 400")
	}
	// turn 2: two subagents at once; the screen while both run, and after
	steps = append(steps, `type check the cache from two angles\r`, "until 30000 tests", "sleep 700", "snap agents",
		"until 60000 Both\\sagents\\sreported", "stable 1500 30000", "snap agents-done")
	// turn 3: ask mode; an edit waits for a yes (the per-hunk review)
	steps = append(steps, `type /mode ask\r`, "stable 700 10000", `type rename the first-eviction helper\r`,
		"until 30000 why:", "stable 700 10000", "snap approval", "type y", "until 30000 Edited\\safter", "stable 1000 20000",
		`type /mode yolo\r`, "stable 700 10000")
	// turn 4: the provider's rate limit (429, Retry-After an hour): the error and the quota
	steps = append(steps, `type summarise what changed\r`, "until 90000 SCRIPTED-FAILURE", "stable 1500 30000", "snap error")
	return runProbe(probeBin, ternly, ws, append(env, t.env...), s, steps, dir)
}

func reach(r result) map[int]string {
	cells := map[int]string{}
	for _, f := range facts {
		if f.na != "" {
			cells[f.n] = f.na
			continue
		}
		base := f.snap
		if base == "" {
			base = "mid"
		}
		if f.re.MatchString(r.Snaps[base]) {
			cells[f.n] = "0"
			continue
		}
		cells[f.n] = "∞"
		for _, p := range probes { // fewest keys first
			if f.re.MatchString(r.Snaps["k-"+p.name]) {
				cells[f.n] = fmt.Sprint(p.keys)
				break
			}
		}
	}
	return cells
}

// asciiCheck reports what the ASCII / no-colour tier drew: the non-ASCII
// characters on its screens, and the colour sequences and non-ASCII bytes
// tuiprobe counted in the whole output.
func asciiCheck(r result) string {
	non := map[rune]int{}
	for _, s := range r.Snaps {
		for _, c := range s {
			if c > 126 {
				non[c]++
			}
		}
	}
	var glyphs []string
	for c, n := range non {
		glyphs = append(glyphs, fmt.Sprintf("%c×%d", c, n))
	}
	sort.Strings(glyphs)
	if len(glyphs) > 12 {
		glyphs = append(glyphs[:12], "…")
	}
	colour := fmt.Sprintf("colour SGR in the output: %d truecolor, %d 256-colour, %d 16-colour; %d non-ASCII bytes",
		r.Tech.TrueColor, r.Tech.Color256, r.Tech.Color16, r.Tech.NonASCII)
	if len(non) == 0 {
		return "screens are pure ASCII; " + colour
	}
	return fmt.Sprintf("%d distinct non-ASCII glyphs on screen (%s); %s", len(non), strings.Join(glyphs, " "), colour)
}

// perfRun measures startup, keystroke echo, bytes per key, idle CPU and RSS
// at 120x40 in both tiers, against ADR 030's budgets.
func perfRun(ternly, probeBin, fake, dir string) string {
	var b strings.Builder
	b.WriteString("## Performance at 120×40 (budgets: ADR 030)\n\n| Tier | Startup ms (≤ 60) | Echo p50 / p95 ms (target ≤ 1 / ≤ 4; ADR 032: 6.5 at 120 fps) | Bytes per key (≤ 19) | Idle CPU % over 20 s (≤ 0.5) | RSS MiB (≤ 96) |\n|---|---|---|---|---|---|\n")
	for _, t := range tiers {
		d := filepath.Join(dir, t.name)
		home, ws, env, stop := fixture(fake, d)
		r := runProbe(probeBin, ternly, ws, append(env, t.env...), size{120, 40},
			[]string{"stable 1500 30000", "keys abcdefghijklmnopqrstuvwxyzabcdefghijklmn", "idle 20", "rss"}, d)
		reap(probeBin, home)
		stop()
		var start float64
		var echo []float64
		var kb []int
		var cpu float64
		var rss int
		for _, s := range r.Steps {
			switch {
			case strings.HasPrefix(s.Step, "stable"):
				start = s.Ms
			case strings.HasPrefix(s.Step, "keys"):
				echo, kb = s.EchoMs, s.KeyB
			case strings.HasPrefix(s.Step, "idle"):
				cpu = s.CPUPct
			case s.Step == "rss":
				rss = s.RSSKiB
			}
		}
		fmt.Fprintf(&b, "| %s | %.0f | %s | %s | %.2f | %.0f |\n", t.name, start, pct(echo), medianInt(kb), cpu, float64(rss)/1024)
	}
	return b.String()
}

func pct(v []float64) string {
	if len(v) == 0 {
		return "—"
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return fmt.Sprintf("%.1f / %.1f", s[len(s)/2], s[len(s)*95/100])
}

func medianInt(v []int) string {
	if len(v) == 0 {
		return "—"
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return fmt.Sprint(s[len(s)/2])
}

func reap(probeBin, home string) {
	out, _ := exec.Command(probeBin, "-reap", home).CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" && !strings.Contains(s, "nothing") {
		fmt.Fprintln(os.Stderr, "reap:", s)
	}
}

func version(bin string) string {
	out, _ := exec.Command(bin, "--version").Output()
	return strings.TrimSpace(string(out))
}

func must(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "layout:", msg)
	os.Exit(1)
}

// writeMetrics keeps tuiprobe's measurements without what describes this
// machine: the environment, the command line, the screens (saved apart) and
// process ids. metrics.json and the screen-*.txt files are what gets
// committed; probe.json and raw.gz stay local.
func writeMetrics(probeJSON, out string) {
	b, err := os.ReadFile(probeJSON)
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for _, k := range []string{"env", "cmd", "snaps", "reaped", "survivors"} {
		delete(m, k)
	}
	b, _ = json.MarshalIndent(m, "", " ")
	_ = os.WriteFile(out, b, 0o644)
}

// privacyCheck fails the run if a file meant for git names this machine or
// the tools under test (see bench/leakguard): the home directory, the user,
// the host name, the kernel release, the OS/distro, a tool's session ID.
func privacyCheck(dir string) {
	bad, err := privacyFindings(dir)
	if err != nil {
		fail("privacy: " + err.Error())
	}
	if len(bad) > 0 {
		fail("privacy: files meant for git name this machine or a tool session:\n  " + strings.Join(bad, "\n  "))
	}
	fmt.Fprintln(os.Stderr, "privacy: clean (no home path, user, host, kernel, OS/distro or session ID in REPORT.md, metrics.json, screen-*.txt)")
}

// privacyFindings scans the files of a run that are meant for git.
func privacyFindings(dir string) ([]string, error) {
	return leakguard.ForThisMachine().ScanDir(dir, func(p string) bool {
		rel, _ := filepath.Rel(dir, p)
		if r := "/" + filepath.ToSlash(rel); strings.Contains(r, "/home/") || strings.Contains(r, "/proj-lru/") {
			return false // the fixture: local only
		}
		name := filepath.Base(p)
		return name == "REPORT.md" || name == "metrics.json" || strings.HasPrefix(name, "screen-")
		// probe.json, raw.gz and the fake provider's files stay local
	})
}
