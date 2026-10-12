package main

import (
	"io"
	"net"
	"testing"
	"time"
)

// A client that goes away mid-request closes the upstream connection too, so
// the provider stops working for it.
func TestClientCloseClosesUpstream(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	closed := make(chan struct{})
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 64)
		_, _ = c.Read(buf) // the request; never answer (a long generation)
		_, _ = io.Copy(io.Discard, c)
		close(closed) // EOF: the proxy closed its side
	}()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serve(ln, up.Addr().String())
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("POST /v1/messages HTTP/1.1\r\n\r\n"))
	time.Sleep(100 * time.Millisecond)
	c.Close() // the harness is killed
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream connection stayed open after the client left")
	}
}

// Bytes go both ways while both sides are there.
func TestForwardsBothWays(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write([]byte("pong"))
	}()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serve(ln, up.Addr().String())
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("got %q, %v", buf, err)
	}
}
