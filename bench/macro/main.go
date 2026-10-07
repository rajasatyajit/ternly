// Command macro measures whole-process numbers for bench/baseline.json
// (ADR 017): binary size, start-up (-version), and one scripted headless turn
// against bench/fakeprovider (wall time and peak RSS), each N times, in a
// throwaway HOME and workspace. Linux (peak RSS comes from rusage).
//
//	macro -bin ternly -fake fakeprovider [-n 15]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type stat struct {
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	N      int     `json:"n"`
}

func summarize(xs []float64) stat {
	s := slices.Clone(xs)
	slices.Sort(s)
	return stat{Median: s[len(s)/2], Min: s[0], Max: s[len(s)-1], N: len(s)}
}

func main() {
	bin := flag.String("bin", "", "ternly binary (static, stripped)")
	fake := flag.String("fake", "", "bench/fakeprovider binary")
	n := flag.Int("n", 15, "runs per measurement")
	flag.Parse()
	if err := run(*bin, *fake, *n); err != nil {
		fmt.Fprintln(os.Stderr, "macro:", err)
		os.Exit(1)
	}
}

func run(bin, fake string, n int) error {
	fi, err := os.Stat(bin)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "ternly-macro-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	home, ws := filepath.Join(work, "home"), filepath.Join(work, "ws")
	for _, d := range []string{filepath.Join(home, ".config", "ternly"), filepath.Join(home, ".cache", "ternly"), ws} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	fp := exec.Command(fake, "MACRO-OK")
	out, _ := fp.StdoutPipe()
	if err := fp.Start(); err != nil {
		return err
	}
	defer func() { _ = fp.Process.Kill(); _ = fp.Wait() }()
	url, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		return fmt.Errorf("fakeprovider: %w", err)
	}
	cfg := fmt.Sprintf(`{"no_local":true,"suggestions":false,"memory":false,"providers":[{"id":"fake","kind":"openai","base_url":%q,"key_env":"FAKE_KEY"}]}`, strings.TrimSpace(url))
	if err := os.WriteFile(filepath.Join(home, ".config", "ternly", "config.json"), []byte(cfg), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, ".cache", "ternly", "catalog.json"), []byte(`{"m1":{"Tools":true,"Ctx":100000}}`), 0o600); err != nil {
		return err
	}
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME=", "XDG_CACHE_HOME=", "XDG_DATA_HOME=", "XDG_STATE_HOME=", "FAKE_KEY=k")
	measure := func(want string, args ...string) (ms, rssMB float64, err error) {
		c := exec.Command(bin, args...)
		c.Env, c.Dir = env, ws
		t0 := time.Now()
		b, err := c.CombinedOutput()
		ms = float64(time.Since(t0).Microseconds()) / 1000
		if err != nil || !strings.Contains(string(b), want) {
			return 0, 0, fmt.Errorf("%v: %v\n%s", args, err, b)
		}
		return ms, float64(c.ProcessState.SysUsage().(*syscall.Rusage).Maxrss) / 1024, nil
	}
	var verMS, verRSS, turnMS, turnRSS []float64
	for range n {
		ms, rss, err := measure("ternly", "--version")
		if err != nil {
			return err
		}
		verMS, verRSS = append(verMS, ms), append(verRSS, rss)
		ms, rss, err = measure("MACRO-OK", "--model", "fake/m1", "-C", ws, "--new", "-p", "say the word")
		if err != nil {
			return err
		}
		turnMS, turnRSS = append(turnMS, ms), append(turnRSS, rss)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"binary_bytes":                fi.Size(),
		"startup_version_ms":          summarize(verMS),
		"startup_version_peak_rss_mb": summarize(verRSS),
		"headless_turn_ms":            summarize(turnMS),
		"headless_turn_peak_rss_mb":   summarize(turnRSS),
	})
}
