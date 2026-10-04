# ADR 000 — Rename to ternly, legacy migration, release metadata

Status: accepted (M0)

## Problem
The project was renamed from *vane* to *ternly*. Every user-visible name, path and env var must use
the new name. Existing users' config and cache must carry over. Releases need reproducible static
binaries and an AUR package.

## Findings
- `grep -rIni vane .` hits only the deliberate migration code in `main.go` (and `docs/DEV_PROMPT.md`,
  the spec describing the rename). Module path, binary, config/cache dirs, `TERNLY_THEME`, `TERNLY=1`,
  `.ternly-*`, OpenRouter headers, TUI, help and system prompt already used *ternly*.
- Gaps: no migration test; `TERNLY.md` was never read when `AGENTS.md` existed (the loop loads only
  the first file found and `AGENTS.md` came first); no release config.
- `go.mod` declared `go 1.23` but required `golang.org/x/{sys,term,text}` versions that declare
  `go 1.26.0`, so `go mod tidy` raised the directive to `1.26.0`. This is the honest minimum.

## Decisions
1. **Migration** (`main.go:migrateLegacy`): `Lstat` the destination, so anything at the ternly path,
   including a dangling symlink, blocks the move. `Stat` the source, so a symlinked legacy dir
   (dotfile managers) is moved as a link. `rename(2)` cannot replace a non-empty directory, so the
   check-then-rename race cannot destroy data. The user gets a note when a move happens or fails.
   Tested in `main_test.go`.
2. **Instruction file order:** `TERNLY.md` → `AGENTS.md` → `CLAUDE.md` → `.cursorrules`. The
   tool-specific file wins. M5 will merge multiple rule sources properly.
3. **Wordmark:** keep `◆ ternly`; tagline "Code, ternly." in the TUI welcome line and README.
4. **Release:** GoReleaser v2 (`.goreleaser.yaml`), `CGO_ENABLED=0 -trimpath -s -w`, linux/darwin ×
   amd64/arm64, stable archive names `ternly_<ver>_<os>_<arch>.tar.gz`, sha256 `checksums.txt`.
5. **AUR:** a hand-written `packaging/aur/ternly-bin/PKGBUILD` plus a generated `.SRCINFO`.
   Considered GoReleaser's `aurs:` publisher, which generates the PKGBUILD with checksums. Rejected
   for now: it needs an AUR SSH key in CI and would be a second source of truth. Checksums are
   `SKIP` until a release exists; the release step is `updpkgsums && makepkg --printsrcinfo > .SRCINFO`.

## Trade-offs and limits
- **darwin is not fully static.** macOS supports no fully static binaries. Go links the system
  `libSystem` even with `CGO_ENABLED=0` (`file` reports `DYLDLINK`). Only Linux builds are
  `statically linked`.
- **License:** the repo has no LICENSE file, so the PKGBUILD says `license=('unknown')`. This needs
  an owner decision before AUR publication.

## Measurement (run 2026-10-04, goreleaser v2.18.2, go1.27.1)
- `goreleaser check`: config valid. `goreleaser release --snapshot --clean`: 4 archives in 9 s.
  The linux binaries are `statically linked`, and `ternly -version` prints the injected version.
- `makepkg -f` against the real linux_amd64 snapshot archive builds `ternly-bin-0.1.0-1`
  (`/usr/bin/ternly` 15.9 MB + README), and the packaged binary runs.
