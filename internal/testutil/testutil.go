// Package testutil holds helpers shared by ternly's tests.
package testutil

import (
	"os"
	"os/exec"
	"testing"
)

// Require skips a test whose prerequisite is missing, unless
// TERNLY_REQUIRE_SANDBOX=1 (CI): there, sandbox and security tests must fail
// rather than silently skip.
func Require(t testing.TB, what string, usable bool) {
	t.Helper()
	if usable {
		return
	}
	if os.Getenv("TERNLY_REQUIRE_SANDBOX") == "1" {
		t.Fatalf("%s is required (TERNLY_REQUIRE_SANDBOX=1) but not usable here", what)
	}
	t.Skipf("%s not usable here", what)
}

// Have reports whether bin is on PATH.
func Have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
