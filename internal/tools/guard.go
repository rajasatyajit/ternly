package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Framer delimits tool output as untrusted data. The nonce is random per
// session, so content can't forge the end marker; it is stable within the
// session, so the conversation prefix stays byte-identical for prompt caching.
type Framer struct{ nonce string }

func NewFramer() *Framer {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return &Framer{nonce: hex.EncodeToString(b)}
}

// reInjection flags text that tries to act as instructions. It is advisory
// (cheap to evade, some false positives); the permission policy is what
// actually stops harmful actions.
var reInjection = regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b[^\n]{0,40}\b(previous|prior|above|earlier|preceding|system|developer)\b[^\n]{0,30}\b(instructions?|prompts?|rules|directives|guidelines)\b` +
	`|\byou are now (a|an|in|the)\b|\bnew instructions\s*:|<\|im_start\|>|<\|(system|assistant)\|>|\[/?INST\]` +
	`|<<<\s*(UNTRUSTED|END)\b|\b(AI|LLM|assistant|agent)s?\s*(reading|processing) this\b|\bdo not (tell|inform|alert) the user\b`)

// injTriggers start every reInjection alternative; the regex only runs on a
// window around them, which keeps flagging ~linear and cheap on large outputs.
var injTriggers = []string{"ignore", "disregard", "forget", "override", "you are now", "new instructions",
	"<|", "[inst]", "[/inst]", "<<<", " this", "do not tell", "do not inform", "do not alert"}

func suspicious(s string) bool {
	low := strings.ToLower(s)
	for _, w := range injTriggers {
		for off := 0; ; {
			i := strings.Index(low[off:], w)
			if i < 0 {
				break
			}
			i += off
			if reInjection.MatchString(low[max(0, i-48):min(len(low), i+len(w)+128)]) {
				return true
			}
			off = i + len(w)
		}
	}
	return false
}

// Wrap frames s; flagged reports a suspected prompt injection.
func (f *Framer) Wrap(tool, s string) (out string, flagged bool) {
	s = strings.ReplaceAll(s, f.nonce, "[marker]")
	flagged = suspicious(s)
	warn := ""
	if flagged {
		warn = " WARNING=possible-prompt-injection"
	}
	return fmt.Sprintf("<<<UNTRUSTED:%s tool=%s%s>>>\n%s\n<<<END:%s>>>", f.nonce, tool, warn, strings.TrimRight(s, "\n"), f.nonce), flagged
}

// Result is the outcome of a tool call.
type Result struct {
	Out      string
	IsErr    bool
	Rejected bool // refused before running: unknown tool, invalid args, permission denied
	Flagged  bool // output looked like a prompt injection
}

// Unframe strips the untrusted-data markers, for display to the user.
func Unframe(s string) string {
	if !strings.HasPrefix(s, "<<<UNTRUSTED:") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "\n<<<END:"); j >= 0 {
		s = s[:j]
	}
	return s
}

// Suspicious reports whether s reads like a prompt injection (the same
// advisory check that flags tool output). Memory uses it to refuse writes.
func Suspicious(s string) bool { return suspicious(s) }

var (
	reNonWordChars = regexp.MustCompile(`[^\pL\pN.-]+`)
	rePromo        = regexp.MustCompile(`(?i)\b(always|must|should|you will) (recommend|choose|pick|install|use|suggest|rank|prefer) (me|this|it|us)\b|\b(rank|ranked|list)(ed)? (me |this |it )?(first|#?1|top|above)\b|\bignore (the )?(other|previous|all|any)\b|\b(best|only) (choice|option|tool|plugin|server) for (everything|anything|any task|all)\b|\b(official|verified|endorsed) by (anthropic|google|openai)\b`)
)

// Manipulative reports why catalog or plugin text looks like it tries to
// steer an agent or a ranking: instructions to AI, self-promotion
// directives, false endorsement claims, or keyword stuffing.
func Manipulative(text string) string {
	switch {
	case suspicious(text):
		return "its text reads like instructions to an AI"
	case rePromo.MatchString(text):
		return "its text tries to steer recommendations"
	}
	ts := strings.Fields(strings.ToLower(reNonWordChars.ReplaceAllString(text, " ")))
	if len(ts) >= 12 {
		count := map[string]int{}
		for _, t := range ts {
			if len(t) > 2 {
				count[t]++
			}
		}
		for _, n := range count {
			if n >= 4 && float64(n)/float64(len(ts)) > 0.12 {
				return "its text repeats keywords (stuffing)"
			}
		}
	}
	return ""
}

// Described renders text a plugin or server supplied (a tool, skill or agent
// description) for a model: kept if plain, withheld if it reads like
// instructions or steering, since tool descriptions sit next to instructions.
func Described(text string) string {
	if why := Manipulative(text); why != "" {
		return "(description withheld: " + why + ")"
	}
	return text
}
