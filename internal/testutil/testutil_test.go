package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRequireFailsWhenSandboxRequired(t *testing.T) {
	if os.Getenv("TESTUTIL_CHILD") == "1" {
		Require(t, "imaginary-sandbox", false)
		return
	}
	for env, wantFail := range map[string]bool{"TERNLY_REQUIRE_SANDBOX=1": true, "TERNLY_REQUIRE_SANDBOX=": false} {
		c := exec.Command(os.Args[0], "-test.run=^TestRequireFailsWhenSandboxRequired$", "-test.v")
		c.Env = append(os.Environ(), "TESTUTIL_CHILD=1", env)
		out, err := c.CombinedOutput()
		if failed := err != nil; failed != wantFail {
			t.Errorf("%s: failed=%v want %v\n%s", env, failed, wantFail, out)
		}
		if !wantFail && !strings.Contains(string(out), "SKIP") {
			t.Errorf("%s: expected a skip\n%s", env, out)
		}
	}
}
