// Command fakeprovider is an OpenAI-compatible server for release smoke
// tests (bench/smoke.sh): /models lists "m1", and every chat completion
// streams the same reply. It prints its base URL on stdout.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

func main() {
	reply := "SMOKE-OK"
	if len(os.Args) > 1 {
		reply = os.Args[1]
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("http://%s/v1\n", ln.Addr())
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []any{
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": reply}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}},
			map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2}},
		} {
			b, _ := json.Marshal(c)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}
