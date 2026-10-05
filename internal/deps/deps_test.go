package deps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAddedAndCommands(t *testing.T) {
	before := "module app\n\ngo 1.22\n\nrequire (\n\tgithub.com/a/b v1.0.0\n)\n"
	after := "module app\n\ngo 1.22\n\nrequire (\n\tgithub.com/a/b v1.0.0\n\tgithub.com/google/uuid v9.4.0\n)\n"
	if got := Added("go.mod", []byte(before), []byte(after)); !reflect.DeepEqual(got, []Dep{{"go", "github.com/google/uuid", "v9.4.0"}}) {
		t.Errorf("go.mod: %v", got)
	}
	if got := Added("package.json", []byte(`{"dependencies":{"a":"1.0.0"}}`), []byte(`{"dependencies":{"a":"1.0.0","left-padx":"^2.0.0"},"devDependencies":{"x":"3.1.4"}}`)); len(got) != 2 {
		t.Errorf("package.json: %v", got)
	}
	if got := Added("requirements-dev.txt", nil, []byte("requests==2.99.0\n# c\nflask>=2\n")); !reflect.DeepEqual(got, []Dep{{"pypi", "requests", "2.99.0"}}) {
		t.Errorf("requirements: %v", got)
	}
	if got := Added("Cargo.toml", nil, []byte("[package]\nname = \"x\"\nversion = \"0.1.0\"\n\n[dependencies]\nserde = \"1.0.999\"\ntokio = { version = \"1\", features = [\"full\"] }\n")); len(got) != 2 || got[0].Name != "serde" || got[0].Version != "1.0.999" {
		t.Errorf("Cargo.toml: %v", got)
	}
	for cmd, want := range map[string][]Dep{
		"go get github.com/google/uuid@v9.4.0 && go build ./...": {{"go", "github.com/google/uuid", "v9.4.0"}},
		"npm install --save left-padx@1.2.3 @scope/pkg":          {{"npm", "left-padx", "1.2.3"}, {"npm", "@scope/pkg", ""}},
		"pip install requests==2.99.0 ./local":                   {{"pypi", "requests", "2.99.0"}},
		"cargo add serde@1.0.999":                                {{"cargo", "serde", "1.0.999"}},
		"go build ./...":                                         nil,
	} {
		if got := FromCommand(cmd); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", cmd, got, want)
		}
	}
}

// Registries: a missing version of an existing package and a missing package
// are reported; an unreachable registry says nothing.
func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/github.com/google/uuid/@latest", "/left-pad/latest", "/github.com/!burnt!sushi/toml/@v/v1.4.0.info":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &Checker{GoProxy: srv.URL, NPM: srv.URL}
	probs := c.Check(context.Background(), []Dep{
		{"go", "github.com/google/uuid", "v9.4.0"},
		{"go", "github.com/BurntSushi/toml", "v1.4.0"},
		{"npm", "left-padx-pro", ""},
	})
	if len(probs) != 2 || !strings.Contains(probs[0], "version v9.4.0 doesn't exist (the package does)") || !strings.Contains(probs[1], "no such package") {
		t.Fatalf("%q", probs)
	}
	off := &Checker{GoProxy: "http://127.0.0.1:1"}
	if p := off.Check(context.Background(), []Dep{{"go", "x.y/z", "v1.0.0"}}); len(p) != 0 {
		t.Fatalf("unreachable registry reported %q", p)
	}
}
