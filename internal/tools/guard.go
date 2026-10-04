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
