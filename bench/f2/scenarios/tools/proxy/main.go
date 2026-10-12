// proxy forwards 127.0.0.1:11435 to Ollama on 127.0.0.1:11434, so a scenario
// can take the provider away mid-session (stop this process) and bring it back,
// without touching the Ollama service.
//
// When either side closes, both close. A harness killed mid-request (a cell's
// timeout, its cleanup) must end its request at Ollama too: before this, the
// upstream connection stayed open, Ollama went on generating for a client
// that was gone, and such requests piled up until they held every slot (the
// final F2 run's S5 hung for all three harnesses; ADR 030).
package main

import (
	"io"
	"log"
	"net"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:11435")
	if err != nil {
		log.Fatal(err)
	}
	serve(ln, "127.0.0.1:11434")
}

func serve(ln net.Listener, upstream string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go forward(c, upstream)
	}
}

// forward copies both ways until either side ends, then closes both.
func forward(c net.Conn, upstream string) {
	u, err := net.Dial("tcp", upstream)
	if err != nil {
		c.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(u, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
	<-done
	c.Close()
	u.Close()
	<-done
}
