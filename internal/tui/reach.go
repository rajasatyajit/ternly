package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/editdiff"
	"github.com/rajasatyajit/ternly/internal/gitenv"
)

// Information reach (ADR 030, level 1 and the header): what a person wants to
// know about the work is on screen without a key: why this model, the change
// an edit made, a receipt for every turn, an error with its cause, the git
// branch. Everything here that comes from the model, the workspace or git is
// outside text and is shown as text (untrusted, ADR 023).

const maxInlineDiff = 6 // diff lines shown under an edit; Ctrl+O shows the rest

// editDiff is the change an edit_file call asks for, as '-'/'+' lines (no
// context); nil when the arguments aren't an edit.
func editDiff(tool, args string) (path string, lines []string) {
	if tool != "edit_file" {
		return "", nil
	}
	var a struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
		return "", nil
	}
	hunks, _ := editdiff.Hunks(editdiff.LineDiff(editdiff.SplitLines(a.OldString), editdiff.SplitLines(a.NewString)))
	for _, h := range hunks {
		for _, l := range h.Lines {
			if l != "" && l[0] != ' ' {
				lines = append(lines, strings.TrimRight(l, "\r\n"))
			}
		}
	}
	return a.Path, lines
}

// writePath is the file a write_file call creates or replaces.
func writePath(tool, args string) string {
	if tool != "write_file" {
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	return a.Path
}

// renderDiff is an edit's diff lines under its tool line: at most
// maxInlineDiff unless expanded, each cut to the transcript.
func (m *Model) renderDiff(lines []string) string {
	var b strings.Builder
	show := lines
	if !m.expanded && len(show) > maxInlineDiff {
		show = show[:maxInlineDiff]
	}
	for _, l := range show {
		st := sOK
		if strings.HasPrefix(l, "-") {
			st = sErr
		}
		b.WriteString("\n" + st.Render(fitLines("    "+l[:1]+" "+untrusted(l[1:]), m.cw())))
	}
	if n := len(lines) - len(show); n > 0 {
		b.WriteString("\n" + sDim.Render(fmt.Sprintf("    … +%d lines · ctrl+o expands", n)))
	}
	return b.String()
}

// routeCard is the transcript's line for the model a turn runs on: the model,
// its tier and price, and why it was chosen.
func (m *Model) routeCard(md *discover.Model, reason string) string {
	g := m.glyph()
	why := "chosen as the cheapest capable model: " + reason
	switch {
	case reason == "failover":
		why = "failover: the previous model failed"
	case strings.HasPrefix(reason, "escalated"):
		why = reason
	}
	return sDim.Render(fmt.Sprintf("  %s %s  %s · %s · %s", g.model, untrusted(md.Key()), tierBadge(md.Tier), discover.Price(md), untrusted(why)))
}

// receipt is a finished turn's line: tokens, cost, time, verification and
// the files it changed.
func (m *Model) receipt() string {
	g := m.glyph()
	parts := []string{}
	if in, out, ok := m.turnTokens(); ok {
		parts = append(parts, fmt.Sprintf("turn %s%s %s%s", kfmt(in), g.in, kfmt(out), g.out))
	}
	switch spent := m.ledger.Cost - m.turnCost0; {
	case m.model != nil && spent == 0 && discover.Price(m.model) == "?":
		parts = append(parts, "cost unknown")
	default:
		parts = append(parts, fmt.Sprintf("$%.4f", spent))
	}
	parts = append(parts, durShort(m.turnTook))
	switch m.turnVerify {
	case agent.VerdictVerified:
		parts = append(parts, "verified "+g.ok)
	case agent.VerdictFailed:
		parts = append(parts, "verify failed "+g.fail)
	case agent.VerdictUnverified:
		parts = append(parts, "unverified "+g.unknown)
	}
	if n := len(m.turnFiles); n > 0 {
		files := make([]string, 0, n)
		for f := range m.turnFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		word := "files"
		if n == 1 {
			word = "file"
		}
		parts = append(parts, fmt.Sprintf("%d %s changed (%s)", n, word, untrusted(strings.Join(files, ", "))))
	}
	arrow := "↳"
	if m.App.Access.ASCII {
		arrow = "->"
	}
	return sDim.Render("  " + arrow + " " + strings.Join(parts, g.sep))
}

// errorCause is ternly's one-line reading of an error, and the next step.
func errorCause(text string) (cause, next string) {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "429") || strings.Contains(t, "rate limit") || strings.Contains(t, "quota") || strings.Contains(t, "usage limit"):
		return "the provider's rate limit or quota ran out", "wait for the reset, or /model to pick another"
	case strings.Contains(t, "401") || strings.Contains(t, "403") || strings.Contains(t, "unauthorized") || strings.Contains(t, "api key"):
		return "the provider refused the credentials", "check the key or sign in again (/status shows connections)"
	case strings.Contains(t, "context length") || strings.Contains(t, "too many tokens") || strings.Contains(t, "maximum context"):
		return "the conversation no longer fits the model's context", "/compact, or /model for a larger window"
	case strings.Contains(t, "deadline") || strings.Contains(t, "timeout") || strings.Contains(t, "timed out"):
		return "the provider took too long", "ask again; /model to pick a faster one"
	case strings.Contains(t, "connection refused") || strings.Contains(t, "no such host") || strings.Contains(t, "unreachable"):
		return "the provider can't be reached", "check that it's running (/status)"
	case strings.Contains(t, " 50") || strings.Contains(t, "500") || strings.Contains(t, "502") || strings.Contains(t, "503") || strings.Contains(t, "overloaded"):
		return "the provider failed on its side", "ask again; /model to pick another"
	}
	return "the turn failed", "ask again; /why shows the ranking"
}

// errorBox is an error, framed: ternly's cause, the message, the next step.
func (m *Model) errorBox(b *block) string {
	g := m.glyph()
	cause, next := errorCause(b.text)
	border := lipgloss.RoundedBorder()
	if m.App.Access.ASCII {
		border = lipgloss.ASCIIBorder()
	}
	w := max(20, m.cw()-4)
	body := sErr.Bold(true).Render(g.fail+" "+cause) + "\n" +
		lipgloss.NewStyle().Width(w-4).Render(untrusted(b.text)) + "\n" +
		sDim.Render("next: "+next)
	return lipgloss.NewStyle().Border(border).BorderForeground(cRed).Padding(0, 1).Width(w).MarginLeft(2).Render(body)
}

// gitMsg is the workspace's branch and whether tracked files changed.
type gitMsg struct {
	branch string
	dirty  bool
}

// gitInfo reads the branch and the dirty state, off the UI goroutine, within
// two seconds; nothing outside a repository. Untracked files don't count
// (-uno keeps it fast in large trees).
func gitInfo(root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := gitenv.Command(ctx, "-C", root, "rev-parse", "--abbrev-ref", "HEAD")
		out, err := cmd.Output()
		if err != nil {
			return gitMsg{}
		}
		msg := gitMsg{branch: strings.TrimSpace(string(out))}
		if st, err := gitenv.Command(ctx, "-C", root, "status", "--porcelain", "-uno").Output(); err == nil {
			msg.dirty = len(strings.TrimSpace(string(st))) > 0
		}
		return msg
	}
}
