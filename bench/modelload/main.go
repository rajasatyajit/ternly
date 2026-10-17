// Command modelload measures how fast a local Ollama model loads and runs
// under different settings (cold load, warm reuse, server-side options), from
// the timings Ollama reports with every response (load, prompt and generation
// durations). Used to choose how ternly keeps large local models in memory.
//
//	go run ./bench/modelload -model qwen3.8:latest -endpoints http://127.0.0.1:11434,http://127.0.0.1:11435 -runs 3
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type resp struct {
	TotalDuration      int64 `json:"total_duration"`
	LoadDuration       int64 `json:"load_duration"`
	PromptEvalCount    int   `json:"prompt_eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int   `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
	Error              string
}

func call(endpoint string, body map[string]any) (resp, error) {
	b, _ := json.Marshal(body)
	c := http.Client{Timeout: 30 * time.Minute}
	r, err := c.Post(endpoint+"/api/generate", "application/json", bytes.NewReader(b))
	if err != nil {
		return resp{}, err
	}
	defer r.Body.Close()
	var out resp
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return resp{}, err
	}
	if out.Error != "" {
		return out, fmt.Errorf("%s", out.Error)
	}
	return out, nil
}

func secs(ns int64) float64 { return float64(ns) / 1e9 }

// llamacpp measures llama-server: "load" is the time from start to /health
// ok (a cold start, page cache dropped by the kernel only if it chooses to);
// then one request per run, with the same prompt as for Ollama.
func llamacpp(bin, gguf string, extra []string, ctx, runs int) {
	prompt := strings.Repeat("The function returns the value of the key from the cache, updating its recency. ", 220) + "\nSummarise the above in one sentence."
	fmt.Printf("%-28s %-6s %8s %10s %10s %10s %10s\n", "llama-server "+strings.Join(extra, " "), "state", "total s", "load s", "prompt t/s", "gen t/s", "prompt tok")
	var loads []float64
	var warm [][4]float64
	for i := 0; i < runs; i++ {
		args := append([]string{"-m", gguf, "--port", "11436", "-c", fmt.Sprint(ctx), "-fa", "on", "-ctk", "q8_0", "-ctv", "q8_0"}, extra...)
		c := exec.Command(bin, args...)
		var serr bytes.Buffer
		c.Stdout, c.Stderr = nil, &serr
		t0 := time.Now()
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		exited := make(chan struct{})
		go func() { _ = c.Wait(); close(exited) }()
		ok := false
	wait:
		for time.Since(t0) < 10*time.Minute {
			select {
			case <-exited: // it failed to start: say why, don't wait out the timeout
				break wait
			default:
			}
			if r, err := http.Get("http://127.0.0.1:11436/health"); err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					ok = true
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ok {
			_ = c.Process.Kill()
			lines := strings.Split(strings.TrimSpace(serr.String()), "\n")
			fmt.Fprintf(os.Stderr, "llama-server did not become healthy:\n%s\n", strings.Join(lines[max(0, len(lines)-5):], "\n"))
			os.Exit(1)
		}
		loads = append(loads, time.Since(t0).Seconds())
		for j := 0; j < 2; j++ { // the first request after start, then a warm one
			b, _ := json.Marshal(map[string]any{"prompt": fmt.Sprintf("[%d %d] ", i, j) + prompt, "n_predict": 64, "temperature": 0, "seed": 1, "cache_prompt": false})
			t1 := time.Now()
			r, err := http.Post("http://127.0.0.1:11436/completion", "application/json", bytes.NewReader(b))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			var out struct {
				Timings struct {
					PromptN     int     `json:"prompt_n"`
					PromptMS    float64 `json:"prompt_ms"`
					PredictedN  int     `json:"predicted_n"`
					PredictedMS float64 `json:"predicted_ms"`
				} `json:"timings"`
			}
			_ = json.NewDecoder(r.Body).Decode(&out)
			r.Body.Close()
			tm := out.Timings
			if j == 1 {
				warm = append(warm, [4]float64{time.Since(t1).Seconds(), float64(tm.PromptN) / max(tm.PromptMS/1000, 1e-9), float64(tm.PredictedN) / max(tm.PredictedMS/1000, 1e-9), float64(tm.PromptN)})
			}
		}
		_ = c.Process.Signal(os.Interrupt)
		<-exited
	}
	col := func(j int) float64 {
		var v []float64
		for _, r := range warm {
			v = append(v, r[j])
		}
		return median(v)
	}
	fmt.Printf("%-28s %-6s %8s %10.1f %10s %10s %10s  (n=%d)\n", "", "start", "", median(loads), "", "", "", len(loads))
	if len(warm) == 0 {
		fmt.Printf("%-28s %-6s  no data (every request failed)\n", "", "warm")
		return
	}
	fmt.Printf("%-28s %-6s %8.1f %10s %10.0f %10.1f %10.0f  (n=%d)\n", "", "warm", col(0), "-", col(1), col(2), col(3), len(warm))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func main() {
	model := flag.String("model", "", "model to measure")
	endpoints := flag.String("endpoints", "http://127.0.0.1:11434", "comma-separated Ollama endpoints (each may run with different server settings)")
	runs := flag.Int("runs", 3, "cold and warm runs per endpoint")
	ctx := flag.Int("ctx", 8192, "num_ctx")
	gpu := flag.Int("num-gpu", -1, "layers on the GPU (-1: Ollama decides)")
	server := flag.String("llama-server", "", "llama.cpp llama-server binary: measure it instead of Ollama")
	gguf := flag.String("gguf", "", "with -llama-server: the GGUF file (Ollama's blob can be used directly)")
	sargs := flag.String("server-args", "", "with -llama-server: extra arguments, space-separated (e.g. \"--n-cpu-moe 30\")")
	flag.Parse()
	if *server != "" {
		llamacpp(*server, *gguf, strings.Fields(*sargs), *ctx, *runs)
		return
	}
	if *model == "" {
		fmt.Fprintln(os.Stderr, "-model is required")
		os.Exit(2)
	}
	// An agent-sized prompt: ~3k tokens of context, a short answer.
	prompt := strings.Repeat("The function returns the value of the key from the cache, updating its recency. ", 220) + "\nSummarise the above in one sentence."
	opts := map[string]any{"num_ctx": *ctx, "num_predict": 64, "temperature": 0, "seed": 1}
	if *gpu >= 0 {
		opts["num_gpu"] = *gpu
	}
	fmt.Printf("%-28s %-6s %8s %10s %10s %10s %10s\n", "endpoint", "state", "total s", "load s", "prompt t/s", "gen t/s", "prompt tok")
	for _, ep := range strings.Split(*endpoints, ",") {
		var cold, warm [][5]float64
		for i := 0; i < *runs; i++ {
			_, _ = call(ep, map[string]any{"model": *model, "keep_alive": 0}) // unload
			for _, state := range []string{"cold", "warm"} {
				r, err := call(ep, map[string]any{"model": *model, "prompt": fmt.Sprintf("[%d %s] ", i, state) + prompt, "stream": false, "options": opts, "keep_alive": "10m"})
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s %s: %v\n", ep, state, err)
					continue
				}
				row := [5]float64{secs(r.TotalDuration), secs(r.LoadDuration), float64(r.PromptEvalCount) / max(secs(r.PromptEvalDuration), 1e-9), float64(r.EvalCount) / max(secs(r.EvalDuration), 1e-9), float64(r.PromptEvalCount)}
				if state == "cold" {
					cold = append(cold, row)
				} else {
					warm = append(warm, row)
				}
			}
		}
		for _, x := range []struct {
			name string
			rows [][5]float64
		}{{"cold", cold}, {"warm", warm}} {
			col := func(j int) float64 {
				var v []float64
				for _, r := range x.rows {
					v = append(v, r[j])
				}
				return median(v)
			}
			if len(x.rows) == 0 { // every request failed: say so, never print zeros as if measured
				fmt.Printf("%-28s %-6s  no data (every request failed)\n", ep, x.name)
				continue
			}
			fmt.Printf("%-28s %-6s %8.1f %10.1f %10.0f %10.1f %10.0f  (n=%d)\n", ep, x.name, col(0), col(1), col(2), col(3), col(4), len(x.rows))
		}
	}
	_ = os.Stdout.Sync()
}
