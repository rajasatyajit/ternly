//go:build !linux

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
)

func startInPTY(*exec.Cmd, io.Writer, uint16, uint16) (*os.File, error) {
	return nil, errors.New("pty test helper is Linux-only")
}
