package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cell is one matrix result file: <harness>.<tag>.json, either a Result or a
// recorded outcome ({"outcome":"timeout"|"no-result"}).
type cell struct {
	Harness, Tag string
	Outcome      string // "" for a measurement
	Res          Result
}

func loadCells(dir string) ([]cell, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []cell
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), ".json")
		h, tag, ok := strings.Cut(base, ".")
		if !ok || h == "selfcheck" { // selfcheck.<harness>.json is the sandbox check, not a cell
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var o struct {
			Outcome string `json:"outcome"`
		}
		_ = json.Unmarshal(b, &o)
		c := cell{Harness: h, Tag: tag, Outcome: o.Outcome}
		if o.Outcome == "" {
			if err := json.Unmarshal(b, &c.Res); err != nil {
				return nil, fmt.Errorf("%s: %v", p, err)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// pct is the p-th percentile (nearest rank) of xs; NaN for none.
func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

func f(x float64, prec int) string {
	if math.IsNaN(x) {
		return "n/a"
	}
	return fmt.Sprintf("%.*f", prec, x)
}

// stepsOf returns the steps whose first word is kind.
func stepsOf(r Result, kind string) []StepResult {
	var out []StepResult
	for _, s := range r.Steps {
		if strings.Fields(s.Step)[0] == kind {
			out = append(out, s)
		}
	}
	return out
}

// interaction gathers ready time, per-key echo and bytes, idle CPU and RSS over cells.
type interaction struct {
	ready, echo, keyBytes, cpu, rss []float64
	n, failed                       int
	notes                           []string
}

func (in *interaction) add(c cell) {
	if c.Outcome != "" {
		in.failed++
		in.notes = append(in.notes, c.Tag+": "+c.Outcome)
		return
	}
	in.n++
	for _, s := range stepsOf(c.Res, "until") {
		if s.OK {
			in.ready = append(in.ready, s.MS)
		} else {
			in.notes = append(in.notes, c.Tag+": "+s.Note)
		}
	}
	for _, s := range stepsOf(c.Res, "keys") {
		for i, e := range s.EchoMS {
			if e >= 0 {
				in.echo = append(in.echo, e)
			}
			if i < len(s.KeyBytes) {
				in.keyBytes = append(in.keyBytes, float64(s.KeyBytes[i]))
			}
		}
	}
	for _, s := range stepsOf(c.Res, "idle") {
		in.cpu = append(in.cpu, s.CPUPercent)
	}
	for _, s := range stepsOf(c.Res, "rss") {
		if s.RSSKiB > 0 {
			in.rss = append(in.rss, float64(s.RSSKiB)/1024)
		}
	}
}

func colourUse(t Technique) string {
	var u []string
	if t.TrueColor > 0 {
		u = append(u, "24-bit")
	}
	if t.Color256 > 0 {
		u = append(u, "256")
	}
	if t.Color16 > 0 {
		u = append(u, "16")
	}
	if len(u) == 0 {
		return "none"
	}
	return strings.Join(u, "+")
}

func summarize(w io.Writer, dir string) error {
	cells, err := loadCells(dir)
	if err != nil {
		return err
	}
	by := map[string]map[string]cell{}
	var harnesses []string
	for _, c := range cells {
		if by[c.Harness] == nil {
			by[c.Harness] = map[string]cell{}
			harnesses = append(harnesses, c.Harness)
		}
		by[c.Harness][c.Tag] = c
	}
	sort.Strings(harnesses)

	fmt.Fprintf(w, "### Interaction at 120×40, truecolor (runs r1–r3; medians, echo p95 over every key)\n\n")
	fmt.Fprintf(w, "| harness | runs ok | ready ms | key echo ms (p50 / p95) | bytes per key (p50) | idle CPU %% | RSS MiB | notes |\n|---|---|---|---|---|---|---|---|\n")
	for _, h := range harnesses {
		var in interaction
		for _, r := range []string{"r1", "r2", "r3"} {
			if c, ok := by[h]["base-120x40-truecolor-"+r]; ok {
				in.add(c)
			}
		}
		fmt.Fprintf(w, "| %s | %d/%d | %s | %s / %s | %s | %s | %s | %s |\n", h, in.n, in.n+in.failed,
			f(pct(in.ready, 50), 0), f(pct(in.echo, 50), 1), f(pct(in.echo, 95), 1), f(pct(in.keyBytes, 50), 0),
			f(pct(in.cpu, 50), 2), f(pct(in.rss, 50), 0), strings.Join(in.notes, "; "))
	}

	fmt.Fprintf(w, "\n### Colour and glyph tiers at 120×40 (one run each)\n\nCell: echo p50 ms · bytes/key p50 · colours used in the output · non-ASCII bytes · full clears.\n\n")
	tiers := []string{"truecolor-r1", "256", "16", "nocolor", "truecolor+ascii", "dumb"}
	fmt.Fprintf(w, "| harness | %s |\n|---|%s\n", strings.Join(tiers, " | "), strings.Repeat("---|", len(tiers)))
	for _, h := range harnesses {
		row := []string{h}
		for _, t := range tiers {
			c, ok := by[h]["base-120x40-"+t]
			switch {
			case !ok:
				row = append(row, "not run")
			case c.Outcome != "":
				row = append(row, c.Outcome)
			default:
				var in interaction
				in.add(c)
				ready := "ready"
				if len(in.ready) == 0 {
					ready = "not ready"
				}
				row = append(row, fmt.Sprintf("%s · %s ms · %s B · %s · %d · %d", ready, f(pct(in.echo, 50), 1), f(pct(in.keyBytes, 50), 0),
					colourUse(c.Res.Tech), c.Res.Tech.NonASCII, c.Res.Tech.FullClears))
			}
		}
		fmt.Fprintf(w, "| %s |\n", strings.Join(row, " | "))
	}

	fmt.Fprintf(w, "\n### Sizes and live resize (truecolor)\n\n| harness | 80×24: ready / echo p50 / bytes per key | 250×70: ready / echo p50 / bytes per key | resize 120×40→80×24: ms / bytes | →250×70: ms / bytes |\n|---|---|---|---|---|\n")
	for _, h := range harnesses {
		size := func(tag string) string {
			c, ok := by[h][tag]
			if !ok {
				return "not run"
			}
			if c.Outcome != "" {
				return c.Outcome
			}
			var in interaction
			in.add(c)
			return fmt.Sprintf("%s ms / %s ms / %s B", f(pct(in.ready, 50), 0), f(pct(in.echo, 50), 1), f(pct(in.keyBytes, 50), 0))
		}
		rs := []string{"not run", "not run"}
		if c, ok := by[h]["resize-120x40"]; ok {
			if c.Outcome != "" {
				rs = []string{c.Outcome, c.Outcome}
			} else {
				for i, s := range stepsOf(c.Res, "resize") {
					if i < 2 {
						ok := ""
						if !s.OK {
							ok = " (no redraw)"
						}
						rs[i] = fmt.Sprintf("%s ms / %d B%s", f(s.MS, 0), s.Bytes, ok)
					}
				}
			}
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", h, size("base-80x24-truecolor"), size("base-250x70-truecolor"), rs[0], rs[1])
	}

	fmt.Fprintf(w, "\n### Streaming S1 (\"Explain what simplelru/lru.go does…\"; medians over 3 runs)\n\n| harness | size | runs ok | bytes | gzip bytes | frames | fps | bytes per frame (p50) | answer ms |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, h := range harnesses {
		for _, sz := range []string{"120x40", "80x24"} {
			var bytes, gz, frames, fps, perFrame, ms []float64
			ok, all := 0, 0
			for _, r := range []string{"r1", "r2", "r3"} {
				c, has := by[h]["stream-"+sz+"-"+r]
				if !has {
					continue
				}
				all++
				if c.Outcome != "" {
					continue
				}
				for _, s := range stepsOf(c.Res, "stream") {
					if !s.OK || s.Bytes == 0 {
						continue
					}
					ok++
					bytes = append(bytes, float64(s.Bytes))
					gz = append(gz, float64(s.GzipBytes))
					frames = append(frames, float64(s.Frames))
					fps = append(fps, s.FPS)
					var fb []float64
					for _, b := range s.FrameBytes {
						fb = append(fb, float64(b))
					}
					perFrame = append(perFrame, pct(fb, 50))
					ms = append(ms, s.MS)
				}
			}
			if all == 0 {
				continue
			}
			fmt.Fprintf(w, "| %s | %s | %d/%d | %s | %s | %s | %s | %s | %s |\n", h, sz, ok, all, f(pct(bytes, 50), 0), f(pct(gz, 50), 0),
				f(pct(frames, 50), 0), f(pct(fps, 50), 1), f(pct(perFrame, 50), 0), f(pct(ms, 50), 0))
		}
	}

	fmt.Fprintf(w, "\n### Long session (10 turns at 120×40): RSS and output growth\n\n| harness | turns answered | RSS MiB start → end | total bytes | gzip bytes |\n|---|---|---|---|---|\n")
	for _, h := range harnesses {
		c, ok := by[h]["long-120x40"]
		if !ok {
			continue
		}
		if c.Outcome != "" {
			fmt.Fprintf(w, "| %s | %s | | | |\n", h, c.Outcome)
			continue
		}
		answered := 0
		for _, s := range stepsOf(c.Res, "stream") {
			if s.OK && s.Bytes > 0 {
				answered++
			}
		}
		rss := stepsOf(c.Res, "rss")
		first, last := math.NaN(), math.NaN()
		if len(rss) > 0 {
			first, last = float64(rss[0].RSSKiB)/1024, float64(rss[len(rss)-1].RSSKiB)/1024
		}
		fmt.Fprintf(w, "| %s | %d/10 | %s → %s | %d | %d |\n", h, answered, f(first, 0), f(last, 0), c.Res.Bytes, c.Res.Gzip)
	}

	fmt.Fprintf(w, "\n### Rendering technique (from base-120x40-truecolor-r1's whole output)\n\n| harness | alt screen | sync updates (DECSET 2026) | full clears (ED 2) | kitty keyboard | mouse | bracketed paste | leftovers reaped |\n|---|---|---|---|---|---|---|---|\n")
	for _, h := range harnesses {
		c, ok := by[h]["base-120x40-truecolor-r1"]
		if !ok || c.Outcome != "" {
			continue
		}
		t := c.Res.Tech
		fmt.Fprintf(w, "| %s | %v | %d | %d | %v | %v | %v | %d |\n", h, t.AltScreen, t.SyncOutput, t.FullClears, t.KittyKbd, t.MouseReport, t.BracketPaste, len(c.Res.Reaped))
	}
	return nil
}
