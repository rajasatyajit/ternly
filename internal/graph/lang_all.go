//go:build ternly_all_grammars

package graph

import (
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// With -tags ternly_all_grammars the graph also parses every other language
// gotreesitter ships (the binary grows by about 18 MB).
func init() {
	extraLanguage = func(name string) (*gotreesitter.Language, string) {
		e := grammars.DetectLanguageByName(name)
		if e == nil {
			return nil, ""
		}
		return e.Language(), grammars.ResolveTagsQuery(*e)
	}
	extraExt = func(path string) string {
		if e := grammars.DetectLanguage(path); e != nil && e.Name != "go" {
			return e.Name
		}
		return ""
	}
}
