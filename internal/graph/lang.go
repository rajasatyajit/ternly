package graph

import (
	"embed"
	"sync"

	"github.com/odvcencio/gotreesitter"
	grammarruntime "github.com/odvcencio/gotreesitter/grammars/runtime"
)

// The non-Go languages the graph parses by default (ADR 013): their parse
// tables and tags queries are embedded here, and only these — importing
// gotreesitter's grammars package would embed all 206 grammars (+18 MB).
// Build with -tags ternly_all_grammars for every other language it has.
//
//go:embed grammars/*.bin grammars/*.scm
var grammarFiles embed.FS

var builtinLangs = map[string]bool{"python": true, "typescript": true, "tsx": true, "javascript": true, "rust": true, "java": true}

var registerOnce sync.Once

func registerBuiltins() {
	registerOnce.Do(func() {
		for name := range builtinLangs {
			name := name
			grammarruntime.RegisterBlob(name+".bin", func() []byte { b, _ := grammarFiles.ReadFile("grammars/" + name + ".bin"); return b })
		}
		grammarruntime.RegisterPythonSupport() // external scanners (Java needs none)
		grammarruntime.RegisterTypescriptSupport()
		grammarruntime.RegisterTsxSupport()
		grammarruntime.RegisterJavascriptSupport()
		grammarruntime.RegisterRustSupport()
	})
}

// language returns a grammar and its tags query, or nil.
func language(name string) (*gotreesitter.Language, string) {
	if builtinLangs[name] {
		registerBuiltins()
		q, _ := grammarFiles.ReadFile("grammars/" + name + ".scm")
		return grammarruntime.Language(name + ".bin"), string(q)
	}
	if extraLanguage != nil {
		return extraLanguage(name)
	}
	return nil, ""
}

// extraLanguage and extraExt are set by lang_all.go (-tags ternly_all_grammars).
var (
	extraLanguage func(name string) (*gotreesitter.Language, string)
	extraExt      func(path string) string
)
