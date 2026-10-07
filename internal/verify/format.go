package verify

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Formatting (v0.1.1 review): unformatted code never ends ✓ verified. After
// the coverage checks pass, the changed files are run through the language's
// standard formatter in check mode, and a file it would change fails the
// turn like a build error, so the model is told to fix it:
//   - Go: gofmt -l (part of every Go toolchain);
//   - Rust: rustfmt --check with the crate's edition; a missing rustfmt is a
//     gap, since formatting can't be shown;
//   - Python, JavaScript/TypeScript: no standard formatter exists, so only a
//     formatter the project configures is run (ruff or black; prettier from
//     the project's node_modules), when installed;
//   - Java and the rest: none.
//
// Only the changed files are judged: rustfmt also visits a file's `mod`
// children, and unformatted code the turn didn't touch isn't its fault.

func (c *checker) format(ctx context.Context, changed map[string][]string) {
	covered := map[string]bool{} // only compiled files: a gap stays one gap
	for _, f := range c.r.Covered {
		covered[f] = true
	}
	by := map[string][]string{}
	for l, fs := range changed {
		for _, f := range fs {
			if covered[f] {
				by[l] = append(by[l], f)
			}
		}
	}
	if fs := by["go"]; len(fs) > 0 {
		c.gofmt(ctx, fs)
	}
	if fs := by["rust"]; len(fs) > 0 && c.r.Failed == "" {
		c.rustfmt(ctx, fs)
	}
	if fs := by["python"]; len(fs) > 0 && c.r.Failed == "" {
		c.pyfmt(ctx, fs)
	}
	if fs := append(append([]string(nil), by["js"]...), by["ts"]...); len(fs) > 0 && c.r.Failed == "" {
		c.prettier(ctx, fs)
	}
}

// formatFailed records files a formatter would change.
func (c *checker) formatFailed(cmd string, bad []string, fix string) {
	c.r.Ran = append(c.r.Ran, cmd)
	c.r.Failed = fmt.Sprintf("$ %s\nnot formatted: %s\n%s", cmd, strings.Join(bad, ", "), fix)
}

func (c *checker) gofmt(ctx context.Context, files []string) {
	cmd := "gofmt -l " + quoteAll(files)
	out, code, err := c.run(ctx, cmd, false)
	switch {
	case errors.Is(err, ErrNotRun):
		c.gaps(files, err.Error())
		return
	case code == 127:
		c.gaps(files, "gofmt isn't available: formatting unchecked")
		return
	case err != nil || code != 0: // a parse error: the build check reports those first
		c.r.Ran = append(c.r.Ran, cmd)
		if c.r.Failed == "" {
			c.r.Failed = "$ " + cmd + "\n" + strings.TrimSpace(out)
		}
		return
	}
	c.r.Ran = append(c.r.Ran, cmd)
	var bad []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			bad = append(bad, c.rel(l))
		}
	}
	if len(bad) > 0 {
		c.formatFailed(cmd, bad, "Run gofmt -w on them.")
		return
	}
	c.r.Summary = append(c.r.Summary, fmt.Sprintf("gofmt: %d file(s)", len(files)))
}

var (
	reEdition  = regexp.MustCompile(`(?m)^\s*edition\s*=\s*"(\d{4})"`)
	reDiffFile = regexp.MustCompile(`(?m)^Diff in (.+?)(?::\d+| at line \d+)?:\s*$`) // rustfmt ≥ 1.5 "path:line:", older "path at line N:"
)

func (c *checker) rustfmt(ctx context.Context, files []string) {
	crates, _ := c.group(files, "no Cargo.toml above it", "Cargo.toml")
	n := 0
	for _, p := range sortedKeys(crates) {
		edition := "2015" // rustfmt's default, as cargo's
		if b, err := c.ws.ReadFile(path.Join(p, "Cargo.toml")); err == nil {
			if m := reEdition.FindSubmatch(b); m != nil {
				edition = string(m[1])
			}
		}
		cmd := "rustfmt --check --color never --edition " + edition + " " + quoteAll(crates[p])
		out, code, err := c.run(ctx, cmd, false)
		switch {
		case errors.Is(err, ErrNotRun):
			c.gaps(crates[p], err.Error())
			continue
		case code == 127:
			c.gaps(crates[p], "rustfmt isn't installed (rustup component add rustfmt): formatting unchecked")
			continue
		case err == nil && code == 0:
			c.r.Ran = append(c.r.Ran, cmd)
			n += len(crates[p])
			continue
		}
		changed := map[string]bool{}
		for _, f := range crates[p] {
			changed[f] = true
		}
		var bad []string
		for _, m := range reDiffFile.FindAllStringSubmatch(out, -1) {
			if f := c.rel(m[1]); changed[f] && !slices.Contains(bad, f) {
				bad = append(bad, f)
			}
		}
		switch {
		case len(bad) > 0:
			c.formatFailed(cmd, bad, "Run rustfmt --edition "+edition+" on them.")
			return
		case !reDiffFile.MatchString(out): // not a formatting diff: rustfmt couldn't parse it
			c.r.Ran = append(c.r.Ran, cmd)
			if c.r.Failed == "" {
				c.r.Failed = "$ " + cmd + "\n" + strings.TrimSpace(out)
			}
			return
		}
		c.r.Ran = append(c.r.Ran, cmd) // only unchanged `mod` children differ
		n += len(crates[p])
	}
	if n > 0 {
		c.r.Summary = append(c.r.Summary, fmt.Sprintf("rustfmt: %d file(s)", n))
	}
}

// pyfmt runs the formatter a Python project configures (ruff, else black).
func (c *checker) pyfmt(ctx context.Context, files []string) {
	cfg := ""
	if b, err := c.ws.ReadFile("pyproject.toml"); err == nil {
		cfg = string(b)
	}
	var cmd, name string
	switch {
	case c.ws.Exists("ruff.toml") || c.ws.Exists(".ruff.toml") || strings.Contains(cfg, "[tool.ruff"):
		cmd, name = "ruff format --check "+quoteAll(files), "ruff format"
	case strings.Contains(cfg, "[tool.black"):
		cmd, name = "black --check "+quoteAll(files), "black"
	default:
		return // no standard Python formatter, and the project names none
	}
	c.configured(ctx, cmd, name, files, false)
}

var prettierConfigs = []string{".prettierrc", ".prettierrc.json", ".prettierrc.yaml", ".prettierrc.yml", ".prettierrc.json5",
	".prettierrc.js", ".prettierrc.cjs", ".prettierrc.mjs", ".prettierrc.toml", "prettier.config.js", "prettier.config.cjs", "prettier.config.mjs"}

// prettier runs the project's own prettier (repository code), when the
// project configures it.
func (c *checker) prettier(ctx context.Context, files []string) {
	configured := false
	for _, f := range prettierConfigs {
		configured = configured || c.ws.Exists(f)
	}
	if b, err := c.ws.ReadFile("package.json"); err == nil && strings.Contains(string(b), `"prettier"`) {
		configured = true
	}
	if !configured || !c.ws.Exists("node_modules/.bin/prettier") {
		return
	}
	c.configured(ctx, quote("./node_modules/.bin/prettier")+" --check "+quoteAll(files), "prettier", files, true)
}

// configured runs a project-configured formatter: exit 1 fails the turn; a
// formatter that isn't installed or isn't allowed to run leaves formatting
// unchecked (there's no standard one to hold the project to).
func (c *checker) configured(ctx context.Context, cmd, name string, files []string, repoCode bool) {
	out, code, err := c.run(ctx, cmd, repoCode)
	switch {
	case errors.Is(err, ErrNotRun) || code == 127:
		c.r.Summary = append(c.r.Summary, name+": not run, formatting unchecked")
		return
	case err == nil && code == 0:
		c.r.Ran = append(c.r.Ran, cmd)
		c.r.Summary = append(c.r.Summary, fmt.Sprintf("%s: %d file(s)", name, len(files)))
		return
	}
	c.r.Ran = append(c.r.Ran, cmd)
	if c.r.Failed == "" {
		c.r.Failed = "$ " + cmd + "\n" + strings.TrimSpace(out) + "\nRun " + name + " on them."
	}
}
