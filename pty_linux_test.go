package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// startInPTY runs c on a new pseudo-terminal of the given size, copying the
// terminal's output to out; it returns the master side for typing.
func startInPTY(c *exec.Cmd, out io.Writer, cols, rows uint16) (*os.File, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil { // unlock the slave
		m.Close()
		return nil, err
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, err
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, err
	}
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	c.Stdin, c.Stdout, c.Stderr = s, s, s
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := c.Start(); err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	s.Close() // the child holds its own copy
	go func() { _, _ = io.Copy(out, m) }()
	return m, nil
}
