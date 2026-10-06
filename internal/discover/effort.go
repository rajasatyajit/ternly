package discover

import (
	"regexp"
	"strings"
	"sync"
)

// EffortRule says how a model family's reasoning levels behave (ADR 016).
// Verified: the levels are documented or measured to mean what they say
// (more reasoning at each step up). Map translates the level routing wants
// into the label sent. Rules from the user's config come first.
type EffortRule struct {
	Match    string            `json:"match"`    // regexp on the model ID
	Verified bool              `json:"verified"` // low < medium < high, documented or measured
	Map      map[string]string `json:"map"`      // wanted level → label sent ("" = send none)
	Why      string            `json:"why"`
}

// builtinEffort is the evidence so far.
var builtinEffort = []EffortRule{
	{Match: `(?i)glm-5`, Map: map[string]string{"low": "low", "medium": "high", "high": "high"},
		Why: "ADR 015: on glm-5.3 through Ollama, medium ≈ the default and high reasons less than medium; low and high finished 3 of 4 netguard runs, off and medium 0 of 4"},
	{Match: `(?i)(^|/)(o[1-9]([-.]|$)|o[1-9]-mini|gpt-5|gpt-oss)`, Verified: true, Why: "OpenAI documents low/medium/high for its reasoning models"},
	{Match: `(?i)gemini-(2\.5|3)`, Verified: true, Why: "Gemini maps reasoning_effort onto thinking budgets"},
}

var effortRe sync.Map // pattern → *regexp.Regexp

func (r EffortRule) matches(id string) bool {
	v, ok := effortRe.Load(r.Match)
	if !ok {
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return false
		}
		v, _ = effortRe.LoadOrStore(r.Match, re)
	}
	return v.(*regexp.Regexp).MatchString(id)
}

// EffortLabel is the label to send for the level routing wants, and a note
// for the user when it differs. Anthropic budgets are tokens (verified by
// construction). For a model whose level semantics aren't verified, medium
// is never sent — on the one model measured it behaved like no budget at
// all — and a hard turn gets high, under the reasoning watchdog.
func (m *Model) EffortLabel(want string, user []EffortRule) (label, note string) {
	if want == "" || !m.Reasoning {
		return "", ""
	}
	id := m.ID
	if m.Base != "" {
		id += " " + m.Base
	}
	for _, rules := range [][]EffortRule{user, builtinEffort} {
		for _, r := range rules {
			if !r.matches(id) {
				continue
			}
			if l, ok := r.Map[want]; ok {
				return l, mappedNote(want, l, r.Why)
			}
			if r.Verified {
				return want, ""
			}
			return unverified(want)
		}
	}
	if m.Provider != nil && m.Provider.Kind == "anthropic" {
		return want, ""
	}
	return unverified(want)
}

func unverified(want string) (string, string) {
	if want == "medium" {
		return "high", mappedNote(want, "high", "this model's reasoning levels aren't verified, and medium isn't sent to such models")
	}
	return want, ""
}

func mappedNote(want, label, why string) string {
	if label == want {
		return ""
	}
	if label == "" {
		label = "none"
	}
	why, _, _ = strings.Cut(why, ";")
	return "asked " + want + ", sent " + label + " (" + why + ")"
}
