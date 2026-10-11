// proxy forwards 127.0.0.1:11435 to Ollama on 127.0.0.1:11434, so a scenario
// can take the provider away mid-session (stop this process) and bring it back,
// without touching the Ollama service.
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
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func(c net.Conn) {
			defer c.Close()
			u, err := net.Dial("tcp", "127.0.0.1:11434")
			if err != nil {
				return
			}
			defer u.Close()
			go func() { _, _ = io.Copy(u, c) }()
			_, _ = io.Copy(c, u)
		}(c)
	}
}
