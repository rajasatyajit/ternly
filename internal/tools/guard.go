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

// The advisory injection flag combines a few principled signals rather than
// enumerating phrasings, so it generalises to wording it was never tuned on.
// It is advisory (cheap to evade, some false positives); the permission policy
// is what actually stops harmful actions.
//
// Detection is a single pass over the lowercased text with a first-byte
// dispatch table (buildSignals), so cost is linear and fast even on MB-sized
// outputs where trigger words are dense: Go's regexp scans non-matching text at
// only a few MB/s, so running several alternations over a whole large input is
// too slow, whereas the literal scan runs at GB/s. The two signals that need a
// bounded gap (the classic "ignore the previous instructions", and moving a
// secret to a destination) are confirmed with a regex that runs only once their
// cheap component signals are all present, which is rare in ordinary text.
type signal uint32

const (
	sigStrong   signal = 1 << iota // injection on its own
	sigVerb                        // ignore/override-style verb (classic, needs a noun)
	sigNoun                        // instruction/rule-style noun (classic)
	sigAI                          // addresses an AI agent as the actor
	sigImper                       // a dangerous imperative
	sigOverride                    // an authority/override claim
	sigConceal                     // concealment from the user
	sigPersist                     // a request to persist a rule
	sigMove                        // a send/upload/print-style verb
	sigSecret                      // a secret file or token location
	sigDest                        // an exfiltration destination
)

// strongNeedles etc. are lowercase literals; a hit sets the matching signal.
// They stay literals (no gaps or optional groups) so the scan is a plain
// substring match. Short verb stems are substrings on purpose ("run" matches
// "running"); the loose ones only ever flag in combination with a specific
// signal, so they do not cause false positives on their own.
var signalNeedles = func() map[signal][]string {
	return map[signal][]string{
		sigStrong: {
			// chat-template / fake conversation-role markers
			"<|im_start|>", "<|im_end|>", "<|system|>", "<|assistant|>", "<|user|>", "<|end|>",
			"[inst]", "[/inst]", "<start_of_turn>", "<end_of_turn>", "<|start_header_id|>", "<|end_header_id|>",
			"<user_input>", "</user_input>", "<system>", "</system>", "[system]",
			// explicit injection markers
			"new instructions:", "you are now a", "you are now an", "you are now in", "you are now the",
			"system prompt update", "# system prompt", "## system prompt", "### system prompt",
			"<<<untrusted", "<<< untrusted", "<<<end",
			"system override", "safety=off", "safety =off", "safety= off", "safety = off",
			"confirmations=off", "confirmations off", "confirmation=off",
			// system-prompt / instruction replacement
			"as your system prompt", "as the system prompt", "as your new system prompt",
			"your system prompt is now", "replaces your system prompt", "replace your system prompt",
			"replaced by the contents", "real instructions are", "your real instructions", "your new instructions",
			"your real task", "your new task", "your real goal", "your new goal", "treat the text", "treat the contents",
			// jailbreak personas
			"do anything now", "you are dan", "unrestricted ai", "unrestricted agent", "unrestricted model",
			"unfiltered ai", "unfiltered model", "unfiltered agent", "jailbroken ai", "with no restrictions",
			"without any rules", "no content policy", "act as an unfiltered", "act as an unrestricted",
			"act as a jailbroken", "model that can do anything", "developer mode activated",
			"developer mode enabled", "developer mode with no", "respond as an unfiltered",
			"ignore your anthropic", "ignore anthropic", "ignore content policy", "ignore every safety",
			"ignore all safety", "safety checks are disabled", "disable all safety",
			// non-English "ignore the previous/system instructions/rules" equivalents
			"ignora las instrucciones", "ignora las reglas", "ignorar las instrucciones", "ignorar las reglas",
			"olvida tus instrucciones", "olvida las instrucciones", "olvida tus reglas", "olvida las reglas",
			"ignore as instruções", "esqueça as regras", "esqueça as instruções", "esqueça tuas regras",
			"ignoriere alle", "ignoriere deine", "vergiss die bisherigen", "vergiss die vorherigen",
			"ignorieren sie die regeln", "ignorieren sie die systemanweisungen",
			"ignorez les instructions", "ignorez les règles", "oubliez les consignes",
			"oubliez toutes les consignes", "oubliez les instructions", "ignorer les règles",
			"无视系统提示", "忽略之前", "忽略系统", "忽略所有", "指示をすべて無視", "指示を無視", "システムプロンプトを無視",
		},
		sigVerb: {"ignore", "disregard", "forget", "override", "overwrite", "bypass", "disable", "drop"},
		sigNoun: {"instruction", "prompt", "rule", "directive", "guideline", "guardrail", "restriction", "polic", "constraint"},
		sigAI: {
			"claude,", "claude:", "chatgpt,", "chatgpt:", "gemini,", "gemini:", "copilot,", "copilot:",
			"assistant,", "assistant:", "bot,", "bot:", "bot!",
			"dear ai", "dear assistant", "hey claude", "hey agent", "hey ai", "hey, claude", "hey, agent",
			"note to the ai", "note to the llm", "note to the model", "note to the assistant", "note to the agent",
			"note for the ai", "note for the agent", "to the ai", "to the llm",
			"to the language model", "to the assistant", "to the agent", "to any ai", "to any llm",
			"to any automated agent", "if you're an ai", "if you are an ai", "if you're an llm", "if you are an llm",
			"if you are a coding agent", "if you are an automated", "as an ai", "as a language model",
			"ai agents", "ai agent", "ai models", "ai model", "ai reading this", "llm reading this",
			"model reading this", "agent reading this", "ai processing this", "model processing this",
			"ai parsing this", "model parsing this", "whoever is reading this", "attention agent",
			"attention coding agent", "message to any ai", "hidden instruction for the agent", "agent note",
			"human:", "ai: ", "instructions to the ai", "instructions for the ai", "instructions to the agent",
			"instruction for the agent", "instructions to the coding assistant", "language model summar",
			"language model parsing",
		},
		sigImper: {
			"run ", "execut", "curl", "wget", "delete", "remov", "rm ", "wipe", "upload", "send", "post ",
			"email", "e-mail", "reveal", "print", "paste", "push", "merge", "merging", "insert", "comply",
			"follow ", "proceed", "obey", "dump", "install", "exfiltrat", "mine crypto", "overwrit", "base64",
			"make the", "modif", "stage ", "approve ", "approving", "list all", "list the", "set mode to",
			"do it now", "cat ", "output", "replace", "disable", "add the", "add my", "add a ", "add your",
		},
		sigOverride: {
			"supersede", "no longer appl", "pre-approved", "preapproved", "pre-authorized", "pre-authorised",
			"preauthorized", "has approved", "has authorized", "has authorised", "authorized you", "authorised you",
			"authorized to", "authorised to", "higher priority than your", "higher priority:", "new directive",
			"system override", "safety=off", "safety = off", "safety =off", "safety= off", "confirmations no longer",
			"confirmations off", "confirmation off", "confirmations not required", "no longer require approval",
			"no longer require confirmation", "no longer requires approval", "guardrails disabled",
			"guardrails are disabled", "guardrails off", "elevated mode", "updated by the administrator",
			"instructions have been updated", "is outdated", "is now outdated", "said yes to everything",
			"already agreed", "pre-approved everything", "your new rules",
		},
		sigConceal: {
			"do not tell", "do not inform", "do not let", "do not alert", "do not mention", "don't tell",
			"dont tell", "without telling", "do not ask for permission", "do not ask for confirmation",
			"don't ask for permission", "don't ask for confirmation", "keep this secret",
			"keep it secret", "keep this step secret", "secret from the user", "secret from the operator",
			"secret from the developer", "hidden instruction", "without the user",
		},
		sigPersist: {
			"remember this for all", "for all future projects", "for all future sessions", "for all projects",
			"persist this rule", "persist this instruction", "save to your memory", "in your memory",
			"in your long-term memory", "always trust instructions",
		},
		sigMove: {
			"send", "upload", "post", "email", "e-mail", "include", "print", "paste", "exfiltrat", "reveal",
			"leak", "dump", "append", "mail", "base64", "put ", "stage", "commit", "push",
		},
		sigSecret: {
			".env", "/.ssh", "id_rsa", "id_ed25519", ".aws", "credential", ".netrc", "netrc", ".npmrc",
			"npm token", "api key", "api_key", "api token", "api_token", "github_token", "oauth_token",
			"access token", "access_token", "private key", "/etc/shadow", "aws_secret", ".docker/config",
			"config.json", "secret",
		},
		sigDest: {
			"http://", "https://", "webhook", "gist", "attacker", "paste it here", "paste it below",
			"your reply", "your summary", "your answer", "your response", "your output", "in a comment",
			"verbatim", "to me", "to the server", "to the webhook", "to the gist", "to the email",
			"to the address", "to the bottom", "pr description", "pull request", "in the commit", "and push it",
			"so we", "so the ci", "so the reviewer",
		},
	}
}()

// needle is one literal and the signal it sets. byFirst2 dispatches on the
// first two bytes, so each position checks only the few needles that could
// start there; this keeps the scan near memory speed even though there are a
// couple of hundred needles. Every needle is at least two bytes.
type needle struct {
	text string
	bit  signal
}

// byFirst2 is indexed by the first two bytes packed into a uint16; a plain
// array (not a map) means the per-byte lookup is a bounds-checked index with no
// hashing. Most entries are nil, so the hot path ranges over an empty slice.
var byFirst2 = func() *[1 << 16][]needle {
	var t [1 << 16][]needle
	for bit, list := range signalNeedles {
		for _, s := range list {
			k := uint16(s[0])<<8 | uint16(s[1])
			t[k] = append(t[k], needle{s, bit})
		}
	}
	return &t
}()

// buildSignals scans low once and returns the set of signals present. A needle
// whose signal is already set is skipped, so the scan speeds up as it goes.
func buildSignals(low string) signal {
	var got signal
	for i := 0; i+1 < len(low); i++ {
		if low[i] == '@' { // lone one-byte destination marker (e.g. attacker@host)
			got |= sigDest
		}
		for _, n := range byFirst2[uint16(low[i])<<8|uint16(low[i+1])] {
			if got&n.bit == 0 && strings.HasPrefix(low[i:], n.text) {
				got |= n.bit
			}
		}
	}
	return got
}

// reClassic confirms the classic "<verb> ... the previous/system ...
// instructions/rules" pattern; reExfil* confirm that a secret and a move verb
// sit close together with a destination. Each runs only when buildSignals
// already found its cheap component signals, so these regexes never scan a
// large benign input.
var (
	reClassic = regexp.MustCompile(`(ignore|ignored|disregard|forget|override|overwrite|replace|bypass|disable|drop)[^\n]{0,40}(previous|prior|earlier|above|preceding|system|your)[^\n]{0,34}(instruction|prompt|rule|directive|guideline|guardrail|restriction|polic|constraint)`)
	reExfil   = regexp.MustCompile(`(send|upload|post|email|e-mail|include|print|paste|exfiltrat|reveal|leak|dump|append|mail|base64|put|stage|commit|push)[^\n]{0,60}(\.env|/\.ssh|id_rsa|id_ed25519|\.aws|credential|\.netrc|netrc|\.npmrc|npm token|api[_ ]?key|api[_ ]?token|github_token|oauth_token|access[_ ]?token|private key|/etc/shadow|aws_secret|\.docker/config|config\.json|secret)` +
		`|(\.env|/\.ssh|id_rsa|id_ed25519|\.aws|credential|\.netrc|\.npmrc|github_token|oauth_token|/etc/shadow|private key|\.docker/config)[^\n]{0,60}(send|upload|post|email|include|paste|exfiltrat|reveal|leak|dump|stage|push)`)
	reExfilDest = regexp.MustCompile(`https?://|webhook|gist|attacker|@[a-z0-9.-]+\.[a-z]{2,}|paste it (here|below)|your (reply|summary|answer|response|output)|in a comment|verbatim| to me|to the (server|webhook|gist|email|address|bottom)|pr description|pull request|in the commit|and push it|so (we|the ci|the reviewer)`)
)

func suspicious(s string) bool {
	low := strings.ToLower(s)
	// Spacing/punctuation obfuscation of "ignore (all) previous instructions":
	// collapse to letters only and look for the contiguous phrase. Such attacks
	// are short, so the (allocating) collapse is skipped on large outputs.
	if len(low) < 4096 {
		if sq := lettersOnly(low); strings.Contains(sq, "ignoreprevious") || strings.Contains(sq, "ignoreallprevious") ||
			strings.Contains(sq, "disregardprevious") || strings.Contains(sq, "forgeteverythingabove") {
			return true
		}
	}
	// A prompt injection is local: the address, imperative and override claim sit
	// in the same instruction, not scattered across an unrelated file. So signals
	// must combine within a single line; this keeps the false-positive rate on
	// real source trees low while staying linear overall.
	for len(low) > 0 {
		line := low
		if i := strings.IndexByte(low, '\n'); i >= 0 {
			line, low = low[:i], low[i+1:]
		} else {
			low = ""
		}
		if lineSuspicious(line) {
			return true
		}
	}
	return false
}

func lineSuspicious(line string) bool {
	g := buildSignals(line)
	if g&sigStrong != 0 {
		return true
	}
	// Classic override of earlier instructions: confirm proximity once the verb
	// and noun are both present.
	if g&sigVerb != 0 && g&sigNoun != 0 && reClassic.MatchString(line) {
		return true
	}
	// Exfiltration: a move verb, a secret location and a destination, confirmed
	// by proximity so a line that merely mentions all three stays quiet.
	if g&sigMove != 0 && g&sigSecret != 0 && g&sigDest != 0 && reExfil.MatchString(line) && reExfilDest.MatchString(line) {
		return true
	}
	// The ambiguous signals flag only in combination, so benign text that merely
	// mentions one of them (an install step, a "system role", an SDK example)
	// stays quiet.
	imp := g&sigImper != 0
	return g&sigAI != 0 && imp ||
		g&sigOverride != 0 && (imp || g&sigConceal != 0) ||
		g&sigConceal != 0 && imp ||
		g&sigPersist != 0 && imp
}

// lettersOnly returns the ASCII letters of s with everything else removed, so
// spaced ("i g n o r e") or punctuated ("i.g.n.o.r.e") text can be matched as
// a contiguous phrase. It allocates once and is linear in len(s).
func lettersOnly(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'a' && c <= 'z' {
			b = append(b, c)
		}
	}
	return string(b)
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
