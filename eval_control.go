package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
)

// evalControl is a background evaluation's pause switch (ADR 031): the
// session that started it writes "p" (pause) and "r" (resume) to a pipe; a
// pause cancels the trap in progress, which runs again once resumed. The
// pipe's end means the session is gone. A foreground --eval has no pipe and
// never pauses.
type evalControl struct {
	mu     sync.Mutex
	cond   *sync.Cond
	pause  bool
	gone   bool
	cancel context.CancelFunc
}

func newEvalControl(r io.Reader) *evalControl {
	c := &evalControl{}
	c.cond = sync.NewCond(&c.mu)
	if r != nil {
		go c.read(r)
	}
	return c
}

// openEvalControl reads the control pipe at file descriptor fd ("": none).
func openEvalControl(fd string) *evalControl {
	n, err := strconv.Atoi(fd)
	if err != nil || n < 3 {
		return newEvalControl(nil)
	}
	syscall.CloseOnExec(n) // trap runs don't inherit it
	return newEvalControl(os.NewFile(uintptr(n), "eval-control"))
}

func (c *evalControl) read(r io.Reader) {
	b := make([]byte, 64)
	for {
		n, err := r.Read(b)
		c.mu.Lock()
		for _, x := range b[:n] {
			switch x {
			case 'p':
				c.pause = true
			case 'r':
				c.pause = false
			}
		}
		if err != nil {
			c.gone = true
		}
		if (c.pause || c.gone) && c.cancel != nil {
			c.cancel()
		}
		c.cond.Broadcast()
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// trap waits while paused and returns the context for one trap run,
// cancelled by a pause; ok is false once the session is gone.
func (c *evalControl) trap() (ctx context.Context, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.pause && !c.gone {
		c.cond.Wait()
	}
	if c.gone {
		return nil, false
	}
	if c.cancel != nil {
		c.cancel()
	}
	ctx, c.cancel = context.WithCancel(context.Background())
	return ctx, true
}

// paused reports whether the trap run with ctx was stopped by a pause (or
// the session ending) and must run again.
func (c *evalControl) paused(ctx context.Context) bool { return ctx.Err() != nil }
