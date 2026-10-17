// Command fakeprovider is an OpenAI-compatible server for tests and
// recordings. It prints its base URL on stdout. It also answers Ollama's
// /api/tags, /api/show and /api/ps, as an Ollama with one cloud model, m1.
//
//	fakeprovider [reply]          every chat completion streams reply (default
//	                              "SMOKE-OK"); release smoke tests (bench/smoke.sh)
//	fakeprovider -script s.json   plays a script, one step per completion (the
//	                              layout recordings, bench/f2/layout)
//
// A script is {"steps":[…]}; each step is one of
//
//	{"text":"…", "chunks":8, "delay_ms":30, "usage":[in,out]}  streamed text
//	{"call":["edit_file","{…json args…}"], "delay_ms":500}       one tool call (after delay_ms)
//	{"status":429, "body":"{…}"}                                 an HTTP error
//
// Any step may add "headers" (e.g. {"Retry-After":"3600"}). Steps play in
// order; after the last, every completion answers "done.". "agents" gives
// subagents their own queues, {"MARKER": [steps…]}: a request whose body
// contains MARKER (put it in the agent definition, which becomes the
// subagent's system prompt) takes its next step from that queue, so
// subagents running at once don't take each other's steps.
// The auto-title request ("Title this coding-session") is answered aside.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type step struct {
	Text    string            `json:"text"`
	Chunks  int               `json:"chunks"`
	DelayMS int               `json:"delay_ms"`
	Call    [2]string         `json:"call"`
	Calls   [][2]string       `json:"calls"` // several tool calls in one answer (they may run in parallel)
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Usage   [2]int            `json:"usage"`
}

func main() {
	script := flag.String("script", "", "play this script instead of one fixed reply")
	flag.Parse()
	steps := []step{{Text: "SMOKE-OK", Usage: [2]int{10, 2}}}
	if flag.NArg() > 0 {
		steps[0].Text = flag.Arg(0)
	}
	repeat := true // the fixed reply answers every completion
	var agents map[string][]step
	if *script != "" {
		b, err := os.ReadFile(*script)
		var s struct {
			Steps  []step
			Agents map[string][]step
		}
		if err == nil {
			err = json.Unmarshal(b, &s)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeprovider:", err)
			os.Exit(1)
		}
		steps, repeat, agents = s.Steps, false, s.Agents
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("http://%s/v1\n", ln.Addr())
	var mu sync.Mutex
	n := 0
	agentN := map[string]int{}
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		// Ollama's own API, for a run that points OLLAMA_HOST here: m1 is an
		// Ollama Cloud model (remote_host), with tools and a 128k context, so a
		// 429 exhausts its quota as Ollama Cloud's would.
		case strings.HasSuffix(r.URL.Path, "/api/tags"):
			fmt.Fprint(w, `{"models":[{"name":"m1","remote_model":"m1","remote_host":"https://ollama.com:443","details":{"parameter_size":"7B"}}]}`)
			return
		case strings.HasSuffix(r.URL.Path, "/api/show"):
			fmt.Fprint(w, `{"capabilities":["completion","tools"],"model_info":{"general.architecture":"llama","llama.context_length":131072}}`)
			return
		case strings.HasSuffix(r.URL.Path, "/api/ps"):
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s := step{Text: "done.", Usage: [2]int{10, 2}}
		if strings.Contains(string(body), "Title this coding-session") {
			s = step{Text: "Scripted Title"}
		} else {
			mu.Lock()
			queue := ""
			for marker := range agents {
				if strings.Contains(string(body), marker) {
					queue = marker
				}
			}
			switch {
			case queue != "":
				if q, i := agents[queue], agentN[queue]; i < len(q) {
					s = q[i]
				}
				agentN[queue]++
			case repeat:
				s = steps[0]
			case n < len(steps):
				s = steps[n]
			}
			if queue == "" {
				n++
			}
			mu.Unlock()
		}
		if strings.HasSuffix(r.URL.Path, "/api/chat") && s.Status == 0 {
			for k, v := range s.Headers {
				w.Header().Set(k, v)
			}
			serveNative(w, s)
			return
		}
		serve(w, s)
	}))
}

func serve(w http.ResponseWriter, s step) {
	for k, v := range s.Headers {
		w.Header().Set(k, v)
	}
	if s.Status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.Status)
		fmt.Fprint(w, s.Body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	fin := "stop"
	if s.Call[0] != "" {
		s.Calls = append([][2]string{s.Call}, s.Calls...)
	}
	if len(s.Calls) > 0 {
		time.Sleep(time.Duration(s.DelayMS) * time.Millisecond) // a slow model: a running subagent stays on screen
		var calls []any
		for i, c := range s.Calls {
			calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("call%d", i+1), "function": map[string]any{"name": c[0], "arguments": c[1]}})
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}}}})
		fin = "tool_calls"
	} else {
		for _, c := range split(s.Text, max(1, s.Chunks)) {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}})
			time.Sleep(time.Duration(s.DelayMS) * time.Millisecond)
		}
	}
	send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": fin}}})
	if s.Usage != [2]int{} {
		send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": s.Usage[0], "completion_tokens": s.Usage[1]}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// serveNative plays s in Ollama's native /api/chat stream (NDJSON), which
// ternly uses for an Ollama provider: one JSON object a line, the last with
// done, done_reason and the token counts.
func serveNative(w http.ResponseWriter, s step) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "%s\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if s.Call[0] != "" {
		s.Calls = append([][2]string{s.Call}, s.Calls...)
	}
	if len(s.Calls) > 0 {
		time.Sleep(time.Duration(s.DelayMS) * time.Millisecond)
		var calls []any
		for i, c := range s.Calls {
			args := json.RawMessage(c[1])
			if !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			calls = append(calls, map[string]any{"id": fmt.Sprintf("call%d", i+1), "function": map[string]any{"name": c[0], "arguments": args}})
		}
		send(map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": calls}, "done": false})
	} else {
		for _, c := range split(s.Text, max(1, s.Chunks)) {
			send(map[string]any{"message": map[string]any{"role": "assistant", "content": c}, "done": false})
			time.Sleep(time.Duration(s.DelayMS) * time.Millisecond)
		}
	}
	send(map[string]any{"message": map[string]any{"role": "assistant", "content": ""}, "done": true, "done_reason": "stop",
		"prompt_eval_count": s.Usage[0], "eval_count": s.Usage[1]})
}

// split cuts s into n pieces of about equal length, on rune boundaries.
func split(s string, n int) []string {
	r := []rune(s)
	var out []string
	for i := 0; i < n; i++ {
		a, b := len(r)*i/n, len(r)*(i+1)/n
		if a < b {
			out = append(out, string(r[a:b]))
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
