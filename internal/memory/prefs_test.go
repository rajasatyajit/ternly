package memory

import (
	"strings"
	"testing"
)

func TestTaskRequirementsAreNotPreferences(t *testing.T) {
	spec := strings.Repeat("Create a package that stores OAuth credentials in the keyring with a file fallback. ", 6) +
		"Never log or return secrets in error messages. Always run the tests with -race. From now on, always use table tests in this repo."
	got, _ := preferences(spec)
	if len(got) != 1 || !strings.HasPrefix(got[0], "From now on") {
		t.Fatalf("%q", got)
	}
	if got, _ := preferences("never use tabs in YAML files"); len(got) != 1 {
		t.Fatalf("short prompt: %q", got)
	}
}
