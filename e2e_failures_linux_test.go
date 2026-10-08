//go:build e2e

package main

import (
	"testing"

	"github.com/rajasatyajit/ternly/internal/e2ejudge"
)

// ADR 028: a check counts its failed runs by kind (no model needed).
func TestFailureKindsCounted(t *testing.T) {
	c := &checkResult{Runs: 5}
	for _, f := range []string{"", e2ejudge.FailFormat, e2ejudge.FailWrong, e2ejudge.FailWrong, e2ejudge.FailOther} {
		r := runResult{Failure: f}
		if f != "" {
			r.Err = "x"
		}
		c.add(r)
	}
	c.finish()
	if c.Passes != 1 || c.Format != 1 || c.Wrong != 2 {
		t.Fatalf("passes %d, format %d, wrong %d", c.Passes, c.Format, c.Wrong)
	}
}
