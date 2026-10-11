# ADR 032 — Keystroke echo without a Bubble Tea fork

Status: accepted (the owner's decision of 2026-10-11: "Upstream + no fork").

Context: F2 item (a) (ADR 030). The survey measured ternly's keystroke echo at 13.1 ms median, the
slowest of the harnesses. The first implementation vendored Bubble Tea v2.0.10 with a patch
(`third_party/bubbletea` and a `go.mod` `replace`) and reached 1.3 ms. The owner reviewed it and
ruled out a `replace`: `go install github.com/rajasatyajit/ternly@latest` refuses a module that
has one. The fix had to go upstream or be published under its own module path.

## Why not a fork under its own module path

A copy of Bubble Tea published as, say, `github.com/rajasatyajit/bubbletea/v2` would be a different
package to the compiler. ternly also uses Bubbles' `textarea`, `viewport` and `cursor`, which import
`charm.land/bubbletea/v2`. Bubble Tea's `KeyPressMsg`, `WindowSizeMsg`, `Cmd` and the other message
types are defined there as named types, not as aliases of a shared module. The forked program would
deliver its own `KeyPressMsg`, and `textarea.Update`'s type switch on Bubble Tea's would never match.
A working fork would therefore mean publishing and maintaining a second fork, of Bubbles, with its
import paths rewritten. govulncheck matches advisories by module path, so it would see neither fork.

## Decision

1. **No fork.** ternly builds with upstream `charm.land/bubbletea/v2`. `go install` works, and
   govulncheck and Dependabot cover Bubble Tea as they cover any module.
2. **120 fps** (`tui.FPS`, applied through `tui.ProgramOptions()` in `main.go` and in the guard test).
   It's the most Bubble Tea allows. A key waits at most one frame, 8.3 ms instead of 16.7 ms.
3. **The real fix goes upstream.** It is split into two changes so each can be reviewed alone:
   - charmbracelet/bubbletea#1857, the issue: the measurements and the cause;
   - #1858 (draft): render user input at once instead of on the next tick, with a test that fails on
     `main` (1.0 s at 1 fps) and passes with the change;
   - #1859 (draft): diff only the lines a frame changed. Its property test plays both renderers into
     a terminal emulator over 15,000 random frames and requires the same screen. A keystroke frame
     at 120×40 costs 348.6 µs instead of 477.7 µs.
4. **Adoption is tracked in ternly issue #37.** When a release contains #1858: upgrade, measure,
   return `tui.FPS` to the default if the frame rate no longer sets the echo, and lower the budget.
   If upstream declines, the next step is a new ADR choosing between an own renderer
   (`tea.WithoutRenderer`) and published forks of Bubble Tea and Bubbles.

## Status against the target (owner's review, 2026-10-11)

- **ADR 030's echo target (< 2 ms) is not met.** ternly echoes in 6.5 ms at 120 fps. The target
  stays at < 2 ms: it is not loosened to fit the workaround. Meeting it waits on upstream #1858,
  tracked in issue #37. `TestKeystrokeEcho`'s 8 ms budget is a regression guard for 120 fps, not
  the target.
- **Idle CPU: there is no runtime frame-rate control.** Dropping to a low frame rate after a few
  seconds idle would bring idle CPU well under 0.5%, but Bubble Tea doesn't allow it. In v2.0.10
  and v2.1.0 (upstream `main`, f9df43c), the frame rate is set once by `WithFPS` and kept in an
  unexported field. The renderer's ticker is unexported too, and `RestoreTerminal` restarts the
  renderer at the same rate. So ternly accepts 0.45–0.55% idle until #1858 lands; with #1858 the
  frame rate no longer sets the echo, and ternly can go back to 60 fps or lower. A runtime
  frame-rate option upstream would need a new upstream proposal. Nothing is posted upstream
  without the owner's approval.

## Measurements (Ryzen 7 6800H, 2026-10-11)

| | Bubble Tea default (60 fps) | **ternly now (120 fps)** | the withdrawn patch |
|---|---|---|---|
| Echo through a pty (`bench/tuiprobe`, 120×40 truecolor, 3 × 40 keys) | 14.5–14.7 ms median | **6.4–6.6 ms** median, p95 7.0–7.4 | 1.3 ms (p95 2.7) |
| Echo in-process (`TestKeystrokeEcho`, 120 keys at random phase) | 9.1–11.0 ms | **6.0–7.0 ms** | 1.4 ms |
| Idle CPU of the TUI (`TestTUIIdleCPU`, 20 s) | 0.25–0.35% | **0.45–0.55%** | as 60 fps |
| Idle CPU of a bare program with a 120×40 view, 60 s | 0.24–0.31% | 0.35–0.43% | — |

- **The cost of 120 fps is idle CPU**, about 0.2 points for ternly. That's because Bubble Tea's
  ticker wakes even when nothing changes. It brings the UI's own idle cost to the edge of item (b)'s
  0.5% target. Upstream PRs #1796 and #1832 stop the ticker while idle. #1858 would let ternly go
  back to 60 fps, or below.
- **The guard.** `TestKeystrokeEcho` runs the real program with ternly's options and requires a
  median under 8 ms. That passes at 120 fps (6.0–7.0 ms) and fails at 60 (9.1–11.0 ms), so falling
  back to the default frame rate fails CI (the "TUI budgets" step). Keys are sent at a random phase
  over 50 ms, a whole number of frames at either rate. A fixed gap locks onto the ticker and
  under-reads the wait: a 5 ms gap read 3.0 ms at 120 fps.

## Found on the way

- **An upstream regression, and ternly has it.** Since ultraviolet `006e29f` ("draw grapheme clusters
  wider than 4 columns", charmbracelet/ultraviolet#150, 2026-08-11), every frame re-sends unchanged
  lines that contain wide characters. Typing below an emoji line re-writes that line on each key.
  This was bisected over the 15 commits after `f5a850f9`, which is what Bubble Tea v2.0.10 pins.
  ternly's other dependencies raise ultraviolet to exactly `006e29f`, so with CJK or emoji on
  screen ternly's bytes per key include those lines. The bisect is reported in #1857, and #1859
  avoids it because it touches only changed lines. Check it when upgrading (issue #37).
- **Two guards from the withdrawn patch weren't needed.** The patch fell back to a full redraw for
  "moved" and blank lines, to keep its output byte-identical to a full redraw. Judged by the screen
  instead, those fallbacks only added bytes (1.10 MB against 0.95 MB over the property test). The
  upstream PR drops them.

## Security

Bubble Tea is now an ordinary module dependency. Its advisories reach ternly through govulncheck in
CI (`go run golang.org/x/vuln/cmd/govulncheck ./...`) and Dependabot, with no extra step. The
owner's GitHub account follows charmbracelet/bubbletea#1857 and the two PRs.

## Install

`go install github.com/rajasatyajit/ternly@<version>` works again. Proof, from a clean module cache
and with `GOFLAGS` unset, is in PR #34.
