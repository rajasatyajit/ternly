package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// idleAB measures idle CPU of several ternly binaries in the same conditions,
// interleaved: each round runs every binary once per tier, in an order that
// rotates from round to round, so drift in the machine's load falls on all
// of them alike. Each sample is ternly at 120×40 on its start screen: ready,
// 5 s to settle (discarded), then secs of idle. The report gives each
// binary's median, min and max per tier, and for every binary after the
// first its median paired difference from the first (same round, same tier).
// After the idle, 42 keys are typed: each key's echo time, all rounds pooled.
// An entry name=binary@fps sets ternly's frame rate (ui.fps) for it.
//
//	layout -idle-ab base=./ternly-a,head=./ternly-b -rounds 7 -out DIR
//	layout -idle-ab p120=./ternly@120,p60=./ternly@60,p30=./ternly@30 -out DIR
func idleAB(spec string, rounds, secs int, probeBin, fake, out string, abs func(string) string) string {
	type bin struct {
		name, path string
		fps        int // ui.fps for this entry (name=binary@fps); 0: the machine chooses
	}
	var bins []bin
	for _, kv := range strings.Split(spec, ",") {
		name, path, ok := strings.Cut(kv, "=")
		if !ok || name == "" || path == "" {
			fail("-idle-ab wants name=binary,name=binary: " + kv)
		}
		fps := 0
		if p, n, ok := strings.Cut(path, "@"); ok {
			if _, err := fmt.Sscan(n, &fps); err != nil || fps < 1 || fps > 120 {
				fail("-idle-ab: bad frame rate in " + kv)
			}
			path = p
		}
		bins = append(bins, bin{name, abs(path), fps})
	}
	cpu := map[string]map[string][]float64{}  // binary → tier → per round
	echo := map[string]map[string][]float64{} // binary → tier → every key's echo, all rounds
	load := []string{}
	for r := range rounds {
		for i := range bins {
			b := bins[(i+r)%len(bins)]
			for _, t := range perfTiers() {
				d := filepath.Join(out, "idle", fmt.Sprintf("r%d-%s-%s", r+1, b.name, t.name))
				home, ws, env, stop := fixtureWith(fake, d, script, b.fps)
				la, _ := os.ReadFile("/proc/loadavg")
				res := runProbe(probeBin, b.path, ws, append(env, t.env...), size{120, 40},
					[]string{"stable 1500 30000", "idle 5", fmt.Sprintf("idle %d", secs), "keys abcdefghijklmnopqrstuvwxyzabcdefghijklmn"}, d)
				reap(probeBin, home)
				stop()
				v := -1.0
				if cpu[b.name] == nil {
					cpu[b.name], echo[b.name] = map[string][]float64{}, map[string][]float64{}
				}
				for _, st := range res.Steps {
					switch {
					case st.Step == fmt.Sprintf("idle %d", secs):
						v = st.CPUPct
					case strings.HasPrefix(st.Step, "keys"):
						echo[b.name][t.name] = append(echo[b.name][t.name], st.EchoMs...)
					}
				}
				cpu[b.name][t.name] = append(cpu[b.name][t.name], v)
				load = append(load, strings.Join(strings.Fields(string(la))[:3], " "))
				fmt.Fprintf(os.Stderr, "round %d %s %s: %.2f%%\n", r+1, b.name, t.name, v)
			}
		}
	}

	var s strings.Builder
	fmt.Fprintf(&s, "## Idle CPU, interleaved (%d rounds, %d s each, 120×40, start screen; budget ≤ 0.5 %%, ADR 030)\n\n", rounds, secs)
	s.WriteString("| Binary | fps | Tier | Idle median % | Min % | Max % | Echo p50 / p95 ms | Idle samples |\n|---|---|---|---|---|---|---|---|\n")
	for _, b := range bins {
		for _, t := range perfTiers() {
			v := cpu[b.name][t.name]
			lo, md, hi := stats(v)
			fps := "machine"
			if b.fps > 0 {
				fps = fmt.Sprint(b.fps)
			}
			fmt.Fprintf(&s, "| %s | %s | %s | %.2f | %.2f | %.2f | %s | %s |\n", b.name, fps, t.name, md, lo, hi, pct(echo[b.name][t.name]), joinF(v))
		}
	}
	if len(bins) > 1 {
		s.WriteString("\n| Paired difference | Tier | Median Δ points | Min | Max |\n|---|---|---|---|---|\n")
		a := bins[0]
		for _, b := range bins[1:] {
			for _, t := range perfTiers() {
				var d []float64
				for i, x := range cpu[b.name][t.name] {
					d = append(d, x-cpu[a.name][t.name][i])
				}
				lo, md, hi := stats(d)
				fmt.Fprintf(&s, "| %s − %s | %s | %+.2f | %+.2f | %+.2f |\n", b.name, a.name, t.name, md, lo, hi)
			}
		}
	}
	fmt.Fprintf(&s, "\nLoad average (1, 5, 15 min) before each sample: %s.\n", strings.Join(load, "; "))
	return s.String()
}

func stats(v []float64) (lo, md, hi float64) {
	if len(v) == 0 {
		return
	}
	w := append([]float64(nil), v...)
	sort.Float64s(w)
	n := len(w)
	md = w[n/2]
	if n%2 == 0 {
		md = (w[n/2-1] + w[n/2]) / 2
	}
	return w[0], md, w[n-1]
}

func joinF(v []float64) string {
	p := make([]string, len(v))
	for i, x := range v {
		p[i] = fmt.Sprintf("%.2f", x)
	}
	return strings.Join(p, " ")
}
