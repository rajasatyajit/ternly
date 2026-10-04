# ADR 006 — Bubble Tea v2: assessment, not yet migrated

Status: proposed (no migration yet, per the M2 review). The review asked for `004-bubbletea-v2.md`;
004 was already taken by sessions, so this is 006.

## Why look at v2
- **Start-up stall.** In v1, bubbletea's package `init` calls `lipgloss.HasDarkBackground()`, which
  sends OSC 11 and DSR to the terminal and waits up to termenv's timeout. It runs before `main`, so
  ternly can't pre-empt it, and Go's package-init order is fixed. A terminal that never answers
  stalls start-up: measured 5.08 s (ADR 004).
- **Keys.** Terminals with the kitty keyboard protocol can tell Shift+Enter from Enter, report key
  releases and so on. v1 can't use any of that.

## Verified (2026-10-04, Go module proxy and module source)
| Module | Latest stable | Canonical path | Requires |
|---|---|---|---|
| bubbletea | v2.0.10 (2026-09-24) | `charm.land/bubbletea/v2` | go 1.26.0 (ours: 1.26.0) |
| lipgloss | v2.0.6 (2026-08-11) | `charm.land/lipgloss/v2` | go 1.25 |
| bubbles | v2.2.1 (2026-08-24) | `charm.land/bubbles/v2` | bubbletea v2, lipgloss v2 |
| glamour | v2.0.1 (2026-06-12) | `charm.land/glamour/v2` | lipgloss v2 |

All four are also published under `github.com/charmbracelet/...`, but the go.mod module paths are
`charm.land/...`. The whole stack has stable v2s, so there is no mix of v1 and v2 lipgloss.

**What the v2 source shows:**
- **No `init()` querying the terminal.** The background colour is an explicit command,
  `tea.RequestBackgroundColor`, answered by a `BackgroundColorMsg` with an `IsDark` method.
  `lipgloss.LightDark(isDark)` replaces `AdaptiveColor`. Start-up never waits on the terminal.
- **Keyboard enhancements:** `tea.View.KeyboardEnhancements` requests them, and
  `KeyboardEnhancementsMsg` reports what the terminal supports (`SupportsKeyDisambiguation`,
  `SupportsEventTypes`, `SupportsAlternateKeys`, …). Key input becomes
  `KeyPressMsg`/`KeyReleaseMsg` behind a `KeyMsg` interface.
- **Declarative view:** `View()` returns a `tea.View` struct (`Content`, `Cursor`, `WindowTitle`,
  `MouseMode`, `KeyboardEnhancements`, …) instead of a string. Options such as `WithAltScreen` are no
  longer program options. *Unverified:* where alt-screen is configured in v2. It is not a `View`
  field in v2.0.10, so this needs reading the docs before migrating.
- **Bubbles constructors:** `viewport.New(opts ...Option)` takes functional options; `textarea.New()`
  is unchanged.

## Migration scope for ternly
The v1 API is confined to `internal/tui` (tui.go, sessions.go) and `main.go` (program options,
`lipgloss.SetHasDarkBackground`). There are 19 call sites of v1-specific APIs: `tea.KeyMsg`,
`k.String()` key matching, `AdaptiveColor`, `WithAltScreen`, `viewport.New`, and glamour.
- `onKey`/`pickerKey`: `tea.KeyMsg` becomes `tea.KeyPressMsg`; check key names and `Type` constants.
- `View() string` becomes `View() tea.View`; alt-screen and cursor move into it.
- Palette: `AdaptiveColor` becomes `LightDark`, driven by `BackgroundColorMsg` (re-render on arrival).
- glamour v1 to v2, with style names re-checked.
- The pty e2e tests keep working unchanged and form the regression suite. The "terminal that ignores
  queries" measurement should drop from 5.08 s to process start-up.

Estimated scope: one focused milestone item (roughly 300 lines touched in `internal/tui`), no agent or
tool changes.

## Decision
Don't migrate now (review instruction). Schedule it with M5 (slash-command parity), which reworks
the TUI's command entry, tab completion and fuzzy search anyway. Doing both together avoids touching
the input layer twice. Until then the 5 s stall stays a documented limitation: it only affects
terminals that ignore status queries.

## Trade-offs
- v2 needs Go 1.26 (already our floor) and changes every TUI file.
- Keyboard enhancements only help on terminals that support the kitty protocol; others fall back to
  legacy keys.
