package memory

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Secret detection. A write that matches is refused (not stored redacted):
// a memory is replayed into future prompts, possibly to other providers.
var (
	reSecretShape = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----` +
		`|\b(sk-(ant-|proj-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,}|glpat-[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.)` +
		`|(?i:://[^/\s:@]+:[^/\s@]{6,}@)`) // credentials in a URL
	reSecretKV = regexp.MustCompile(`(?i)\b[a-z0-9_.-]*(api[_-]?key|access[_-]?key|secret|token|passw(?:or)?d|passwd|pwd|credentials?)["']?\s*[:=]\s*["']?([^\s"',;]{8,})`)
	reBearer   = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{20,})`)
	reLongTok  = regexp.MustCompile(`[A-Za-z0-9+/_=-]{20,}`)
)

// SecretReason reports why s looks like it contains a secret ("" if it doesn't).
func SecretReason(s string) string {
	if m := reSecretShape.FindString(s); m != "" {
		return "it contains a credential (" + shorten(m, 12) + "…)"
	}
	for _, m := range reBearer.FindAllStringSubmatch(s, -1) {
		if digits(m[1]) >= 4 || randomLooking(m[1]) { // not "Bearer RequestTimeout…"
			return "it contains a bearer token"
		}
	}
	for _, m := range reSecretKV.FindAllStringSubmatch(s, -1) {
		v := strings.ToLower(m[2])
		if strings.ContainsAny(v[:1], "$<{%[*") || strings.Contains(v, "redacted") || strings.Contains(v, "xxxx") ||
			strings.HasPrefix(v, "your") || strings.Contains(v, "example") || strings.Contains(v, "placeholder") || strings.HasPrefix(v, "os.getenv") {
			continue // a reference or placeholder, not a value
		}
		if strings.ContainsAny(m[2], "0123456789") || randomLooking(m[2]) {
			return "it assigns a value to " + strings.ToLower(m[1])
		}
	}
	for _, t := range reLongTok.FindAllString(s, -1) {
		if randomLooking(t) {
			return "it contains a random-looking token (" + shorten(t, 6) + "…)"
		}
	}
	return ""
}

// randomLooking: mixed upper, lower and digits, high character entropy, and
// frequent class switches. Identifiers (camelCase: one switch per word), git
// hashes and UUIDs (no upper case) don't qualify.
func randomLooking(t string) bool {
	if len(t) < 20 {
		return false
	}
	var up, lo, dg, switches int
	freq := map[rune]int{}
	prev := 0
	for _, r := range t {
		c := 0
		switch {
		case unicode.IsUpper(r):
			up, c = up+1, 1
		case unicode.IsLower(r):
			lo, c = lo+1, 2
		case unicode.IsDigit(r):
			dg, c = dg+1, 3
		}
		if c != 0 && prev != 0 && c != prev {
			switches++
		}
		if c != 0 {
			prev = c
		}
		freq[r]++
	}
	if up == 0 || lo == 0 || dg < 2 {
		return false
	}
	var h float64
	for _, n := range freq {
		p := float64(n) / float64(len(t))
		h -= p * math.Log2(p)
	}
	return h >= 3.8 && float64(switches)/float64(len(t)) >= 0.3
}

func digits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// clean collapses whitespace and caps the length of a memory text.
func clean(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxRunes {
		s = strings.TrimSpace(string(r[:maxRunes-1])) + "…"
	}
	return s
}

// norm is the exact-duplicate key: lowercase words only.
func norm(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// jaccard of two token sets.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	sa := map[string]bool{}
	for _, t := range a {
		sa[t] = true
	}
	sb := map[string]bool{}
	inter := 0
	for _, t := range b {
		if !sb[t] {
			sb[t] = true
			if sa[t] {
				inter++
			}
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

func overlap(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// ─────────────── explicit preferences in user prompts ───────────────

var (
	rePref      = regexp.MustCompile(`(?i)^(?:(?:in all (?:my )?projects|in every project|everywhere|globally|for all repos|in this (?:project|repo))[, ]+)?(?:please\s+)?(?:always|never|from now on|going forward|in (?:the )?future|prefer|don'?t ever|do not ever|remember (?:that|to)|make sure (?:to )?always|we (?:always|never)|i (?:always |usually )?prefer|i like|i don'?t like)\b`)
	reUserScope = regexp.MustCompile(`(?i)\b(in all (?:my )?projects|in every project|across (?:all )?projects|everywhere|globally|for all repos)\b`)
	reSentence  = regexp.MustCompile(`(?m)[^\n]+?(?:[.!?;]+(?:\s|$)|$)`) // "." inside file names doesn't end a sentence
)

// preferences returns explicit standing instructions in a user prompt
// ("always …", "never …", "prefer …", "from now on …") and their scope.
// Questions are skipped.
func preferences(prompt string) (out []string, scopes []Scope) {
	for _, raw := range reSentence.FindAllString(prompt, -1) {
		if strings.Contains(raw, "?") {
			continue
		}
		s := strings.TrimSpace(strings.TrimRight(raw, ".!;\n "))
		if len(s) < 12 || len(s) > 300 {
			continue
		}
		low := strings.ToLower(s)
		if !rePref.MatchString(s) && !strings.Contains(low, "from now on") && !strings.Contains(low, "going forward") {
			continue
		}
		sc := Project
		if reUserScope.MatchString(s) {
			sc = User
		}
		out = append(out, s)
		scopes = append(scopes, sc)
	}
	return out, scopes
}
