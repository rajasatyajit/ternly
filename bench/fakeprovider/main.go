// Command fakeprovider is an OpenAI-compatible server for tests and
// recordings. It prints its base URL on stdout.
//
//	fakeprovider [reply]          every chat completion streams reply (default
//	                              "SMOKE-OK"); release smoke tests (bench/smoke.sh)
//	fakeprovider -script s.json   plays a script, one step per completion (the
//	                              layout recordings, bench/f2/layout)
//
// A script is {"steps":[…]}; each step is one of
//
//	{"text":"…", "chunks":8, "delay_ms":30, "usage":[in,out]}  streamed text
//	{"call":["edit_file","{…json args…}"], "usage":[in,out]}    one tool call
//	{"status":429, "body":"{…}"}                                 an HTTP error
//
// Steps play in order; after the last, every completion answers "done.".
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
	Text    string    `json:"text"`
	Chunks  int       `json:"chunks"`
	DelayMS int       `json:"delay_ms"`
	Call    [2]string `json:"call"`
	Status  int       `json:"status"`
	Body    string    `json:"body"`
	Usage   [2]int    `json:"usage"`
}

func main() {
	script := flag.String("script", "", "play this script instead of one fixed reply")
	flag.Parse()
	steps := []step{{Text: "SMOKE-OK", Usage: [2]int{10, 2}}}
	if flag.NArg() > 0 {
		steps[0].Text = flag.Arg(0)
	}
	repeat := true // the fixed reply answers every completion
	if *script != "" {
		b, err := os.ReadFile(*script)
		var s struct{ Steps []step }
		if err == nil {
			err = json.Unmarshal(b, &s)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeprovider:", err)
			os.Exit(1)
		}
		steps, repeat = s.Steps, false
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("http://%s/v1\n", ln.Addr())
	var mu sync.Mutex
	n := 0
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s := step{Text: "done.", Usage: [2]int{10, 2}}
		if strings.Contains(string(body), "Title this coding-session") {
			s = step{Text: "Scripted Title"}
		} else {
			mu.Lock()
			switch {
			case repeat:
				s = steps[0]
			case n < len(steps):
				s = steps[n]
			}
			n++
			mu.Unlock()
		}
		serve(w, s)
	}))
}

func serve(w http.ResponseWriter, s step) {
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
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call1", "function": map[string]any{"name": s.Call[0], "arguments": s.Call[1]}}}}}}})
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
