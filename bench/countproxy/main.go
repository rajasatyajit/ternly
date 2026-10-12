// Command countproxy is an HTTPS (CONNECT) proxy that counts the bytes each
// host sends and receives, for measuring what a program downloads (ADR 031:
// the catalog's bytes before and after conditional requests). Point the
// program at it with HTTPS_PROXY=http://ADDR; Go programs never proxy
// loopback, so a local Ollama isn't counted. The counts are rewritten to
// -out (JSON) every second and at exit.
//
//	countproxy -listen 127.0.0.1:18080 -out bytes.json
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type counts struct {
	Down, Up    atomic.Int64
	Connections atomic.Int64
}

var (
	mu    sync.Mutex
	hosts = map[string]*counts{}
)

func host(h string) *counts {
	mu.Lock()
	defer mu.Unlock()
	c := hosts[h]
	if c == nil {
		c = &counts{}
		hosts[h] = c
	}
	return c
}

func write(path string) {
	type row struct {
		Host            string
		Down, Up, Conns int64
	}
	mu.Lock()
	var rows []row
	var total int64
	for h, c := range hosts {
		rows = append(rows, row{h, c.Down.Load(), c.Up.Load(), c.Connections.Load()})
		total += c.Down.Load()
	}
	mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Host < rows[j].Host })
	b, _ := json.MarshalIndent(map[string]any{"down_total": total, "hosts": rows}, "", "  ")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "address")
	out := flag.String("out", "countproxy.json", "counts file")
	flag.Parse()
	go func() {
		for range time.Tick(time.Second) {
			write(*out)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	go func() { <-sig; write(*out); os.Exit(0) }()
	log.Fatal(http.ListenAndServe(*listen, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		up, err := net.DialTimeout("tcp", r.Host, 30*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		client, _, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		c := host(r.Host)
		c.Connections.Add(1)
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(counted{up, &c.Up}, client); done <- struct{}{} }()
		go func() { _, _ = io.Copy(counted{client, &c.Down}, up); done <- struct{}{} }()
		<-done
		client.Close()
		up.Close()
		<-done
	})))
}

// counted counts as it goes: a kept-alive connection shows its bytes
// before it closes.
type counted struct {
	w io.Writer
	n *atomic.Int64
}

func (c counted) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(int64(n))
	return n, err
}
