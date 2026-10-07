// Package e2ejudge scores the real-model e2e checks' answers (bench/run.sh
// e2e) and re-scores saved ones (bench/rescore).
//
// The class of bug it closes: judges that searched prose. "app/run.py:5 is
// excluded" failed a correct answer; "the agent claimed 23 lines, but I
// can't trust it" passed a non-answer. A check now asks for its answer as a
// final ```json block and scores only that block; prose never decides.
package e2ejudge

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Instruction is appended to a check's prompt: the shape of the answer, as
// a JSON example.
func Instruction(example string) string {
	return "\n\nEnd your reply with your final answer as a JSON object in a ```json code block, exactly in this shape: " + example
}

// ErrNoAnswer: the reply has no final ```json block (or it doesn't parse).
var ErrNoAnswer = errors.New("no structured answer (a final ```json block)")

var reBlock = regexp.MustCompile("(?s)```json\\s*\\n(.*?)```")

// FinalJSON decodes the last ```json block of the reply into v. Unknown
// fields are refused, so a wrong shape fails instead of reading as empty.
func FinalJSON(reply string, v any) error {
	ms := reBlock.FindAllStringSubmatch(reply, -1)
	if len(ms) == 0 {
		return ErrNoAnswer
	}
	d := json.NewDecoder(strings.NewReader(ms[len(ms)-1][1]))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrNoAnswer, err)
	}
	return nil
}

// CallSites judges a list of file:line call sites: every wanted one present,
// none of the wrong ones.
func CallSites(got, want, wrong []string) (missing, extra []string) {
	norm := make([]string, len(got))
	for i, g := range got {
		norm[i] = strings.TrimPrefix(strings.TrimSpace(g), "./")
	}
	for _, w := range want {
		if !slices.Contains(norm, w) {
			missing = append(missing, w)
		}
	}
	for _, w := range wrong {
		if slices.Contains(norm, w) {
			extra = append(extra, w)
		}
	}
	return missing, extra
}

// SameFile reports whether an answered path names the wanted workspace file
// (store/shared.go; ./store/shared.go), not another with the same base name.
func SameFile(got, want string) bool {
	return path.Clean(strings.TrimPrefix(strings.TrimSpace(got), "./")) == want
}

// ─────────────── re-scoring transcripts saved before structured answers ───────────────

// ProseCallSites judges a call-site answer saved before structured answers
// (bench/rescore): every wanted file:line appears, and a wrong one counts as
// included only on a line that doesn't say it was left out. The judge it
// replaces counted any mention, so "app/run.py:5 is excluded (it calls
// Buffer.flush)" failed correct answers (ADR 018, "A judge bug").
func ProseCallSites(answer string, want, wrong []string) (missing, extra []string) {
	for _, w := range want {
		if !strings.Contains(answer, w) {
			missing = append(missing, w)
		}
	}
	for _, w := range wrong {
		at := regexp.MustCompile(regexp.QuoteMeta(w) + `\b`)
		for _, line := range strings.Split(answer, "\n") {
			if at.MatchString(line) && !reLeftOut.MatchString(line) {
				extra = append(extra, w)
				break
			}
		}
	}
	return missing, extra
}

// reLeftOut: the line says the site was left out ("Buffer" alone doesn't: a
// list line annotated "# Buffer" still includes it).
var reLeftOut = regexp.MustCompile(`(?i)\bexclud|\bomit|\bleft out\b|\bskip`)
