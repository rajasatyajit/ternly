# ADR 016 — M9: release readiness for v0.1.0

Status: accepted (M9; v0.1.0 not yet tagged, awaiting review).

The plan was `docs/plans/m9-release.md`. The decisions below were made in its review.

## 0. The public history was scanned first
The repository was already public, so before anything else the **full history** was scanned:
- **gitleaks 8.30.1:** 33 commits, 46 MB.
- **trufflehog:** with no verification, so no candidate was sent to any provider. It skipped the
  44 MB `simple.json` blob, which was then scanned on its own.
- **14 hits, none real:**
  - five deliberate test fixtures (a planted `E2E-SECRET` marker, a fake `sk-live-…`, AWS's
    documented example key, fake passwords in the memory guard's corpus);
  - nine PyPI package names and serial numbers inside `simple.json`.
- `simple.json`, a PyPI index dump committed by accident in M6, has left the tree; rewriting
  history to drop the blob is the owner's call.
- No home-directory paths appear in any commit. All 33 carry the author's address. The full report
  is kept locally (gitignored).

## 1. macOS ships as experimental: no sandbox, so nothing runs on its own
Keyed on *commands run unsandboxed* (macOS, Linux without bubblewrap, `--no-sandbox`). Even a
safe-listed build or test command runs the repository's own code with the user's full privileges.
- **Policy** (`Policy.Unsandboxed`): no shell command is approved automatically, not by the safe
  list, not in plan mode, not in yolo. "Always" stays an interactive choice. Headless refuses, and
  says why.
- **Verification:** a command that runs repository code asks, and a declined one is a gap, so the
  turn ends *unverified*. That covers the project's check (unless it's a known-safe auto-detected
  one), the project's own tsc, cargo (`build.rs`), and Maven/Gradle. `go list/build/vet`, Python
  `compile()` and `node --check` still run.
- **Plugins** (`Runtime.NoCode`): hooks and plugin MCP servers don't run, with a warning naming the
  plugin. Skills, commands, agents and rules (prompt text) still load.
- **Said everywhere:** the start-up note, `/doctor` (✗ sandbox, "EXPERIMENTAL" on macOS),
  `/permissions`, the README (a warning box near the top), the CHANGELOG, SECURITY.md and the threat
  model.
- **Tests:** `TestUnsandboxedEveryCommandAsks` (every mode), `TestNoCodeWithoutSandbox`,
  `TestUnsandboxedVerifyNotRun`, `TestDeclinedCommandIsAGap`. The sandbox control in
  `TestSandboxedCommandsCannotReadPrivateData` now asserts that `--no-sandbox` refuses, and reads
  the markers on the host instead.
- Seatbelt arrives in v0.2.

## 2. Releases: signed, attested, smoke-tested before publication
`.github/workflows/release.yml` runs on `v*` tags; all actions are pinned by commit SHA.
1. **Gate:** `ci.yml`, now callable.
2. **Build:** goreleaser makes the static archives, an SPDX SBOM per archive (syft), and a
   **cosign keyless** signature of `checksums.txt` (a Sigstore bundle bound to the release
   workflow's GitHub OIDC identity). They go into a **draft** release.
   `actions/attest-build-provenance` attests the archives and the checksums.
3. **Smoke:** the archives are tested on linux/amd64, linux/arm64 under qemu, and darwin/arm64:
   checksums, signature, `--version`, and one scripted headless turn against `bench/fakeprovider`.
4. **Publish:** the draft is published only when all three pass.

`workflow_dispatch` runs a dry run: snapshot archives, SBOMs and the smoke matrix, without
signature, attestation or release (keyless signing writes a permanent public log entry).

**Evidence:**
- Locally: actionlint, `goreleaser check`, a snapshot release with SBOMs, the linux/amd64 smoke
  test, and the cosign bundle arguments (with a throwaway key, no transparency-log upload).
- The dry run on GitHub (run 37412628142) passed every job. Each smoke log shows `SMOKE-OK`,
  arm64-under-qemu and macOS included.
- Archives also carry `THIRD_PARTY_LICENSES`: the license texts of the 32 linked modules (24 MIT,
  8 BSD-3-Clause; `make licenses`), plus CHANGELOG.md and SECURITY.md.

## 3. Homebrew: v0.2, with Seatbelt

## 4. AUR: prepared, pushed by the owner
- The maintainer line reads `maintainer at ternly dot sh`.
- `packaging/aur/update.sh <version> [aur-clone]` fills `pkgver` and both sha256 sums from the
  release's `checksums.txt`, regenerates `.SRCINFO` and commits into a local AUR clone. It never
  pushes.
- Dry run against a snapshot: the sums were filled, `makepkg` built the package and validated them,
  and the package installed and ran in a clean `archlinux` container.
- The committed PKGBUILD stays at 0.1.0 with `SKIP` until the release exists.

## 5. Name: ternly.sh, and a manual trademark check
- **USPTO**, queried through the search backend and validated with a known mark: no TURNLY, TERNLY
  or TERNLI record in any class. The nearest is TURNLIFE (class 9, energy).
- **EUIPO/TMview, WIPO and UK IPO** couldn't be queried by script. A manual checklist (kept local)
  lists the exact searches, the Turnly class 9/42 question, the domain and the GitHub org.

## 6. Reasoning: levels per model, never "medium" unverified, and a watchdog
- **Levels per model** (`discover.EffortRule`):
  - Documented families (OpenAI o-series, gpt-5, gpt-oss; Gemini 2.5/3) and Anthropic budgets
    pass the level through.
  - glm-5 maps medium → high (ADR 015's evidence).
  - Every other model's levels are unverified, so **medium is never sent** and a hard turn gets
    high.
  - Config `reasoning_levels` rules come first. The model line says when the sent label differs.
- **Watchdog:**
  - A step that streams more than 6000 reasoning chunks (`reasoning_watchdog`) before any text or
    tool call is interrupted and retried **once** at another level (low, or high if low ran away).
  - Reasoning chunks are told apart from tool arguments, so a long `write_file` never trips it.
  - The interrupted step's tokens are accounted as an estimate.

**Measured: glm-5.3:cloud on the netguard task.** The protocol is ADR 015's: base `1e16033`,
`--no-memory`, a 30-minute limit, and the same black-box scoring.

| Setting | Result | Wall | First edit | Watchdog | Tokens in / out | Code |
|---|---|---|---|---|---|---|
| auto (medium → **high** for glm) | ✓ verified | 6 m 16 s | 2 m 59 s | — | 311 k / 30 k | 7/7 black-box, own tests pass |
| auto (medium → high) | ✓ verified | 5 m 54 s | 2 m 4 s | — | 421 k / 23 k | 7/7, own tests pass |
| off + watchdog | step limit (60) at 7 m 46 s | 8 m 8 s | 3 m 37 s | fired at 3 m 6 s → low | 1,156 k / 22 k | 7/7, own tests pass |
| off + watchdog | ✓ verified | 8 m 43 s | 7 m 31 s | fired at 6 m 7 s → low | 144 k / 20 k | 7/7, own tests pass |

**Compared with ADR 015** (the same task, without these changes):
- **off and medium:** 0 of 4 finished within 30 minutes, with 96–140 k output tokens.
- **Now auto:** 2 of 2 verified, in about 6 minutes.
- **Now off:** the watchdog cut the runaway in both runs; 1 verified, 1 ran out of steps with
  working code.

That's two runs per arm, so it shows a direction, not an estimate.

**A rule tried and withdrawn (qwen3.6).** The first full e2e on the release candidate (`f3b3f52`)
failed `graph-callsites-python` at 1/3, where M8 passed it 2/3. The mistake was always the same:
including the name-matched caller `app/run.py:5`. qwen3.6 now received `reasoning_effort: low`, so
that was the suspect.
- An A/B on the check (6 runs each) gave low 2/6 and no budget 4/6, and a rule mapping qwen3's low
  to no budget went in (`439949f`).
- The full e2e with that rule failed the check again, at 1/3.
- **Pooled over every run:** no budget 7/12, low 3/9, all runs 10/21. Both arms sit under the 66%
  threshold and don't differ significantly.
- The rule had no evidence behind it, so it was reverted (`1ad478a`). The finding is that this check
  sits **near 50% for qwen3.6** whatever the budget.
- Logs: `bench/dogfood/2026-10-06-m9/ab-graph-python/` and both full-run logs.

## 7. No git process outlives ternly; test homes leave nothing behind
- **The hypothesis tested.** Leftover `.e2e-home-*` directories each held exactly
  `checkpoints/*.git/objects/pack/tmp_rev_*`, the temp file of a repack.
  - ternly's startup `git gc` is an explicit gc, not a detached `gc --auto`. At exit,
    `exec.CommandContext` killed the gc, but not its `git repack` / `pack-objects` children, which
    kept writing into the repository after the test removed its home.
  - Measured: the old binary left **2** git processes after each exit; the fixed one leaves **0**
    (3 runs each).
- **The fix:**
  - Checkpoint git runs in its own process group, stopped whole on cancel. It gets SIGTERM, so git
    removes its lock files; SIGKILL only after a delay. A first version used SIGKILL, and a
    `git add` killed mid-snapshot left `index.lock`; the next run's drift check then failed, and
    `TestSessionFlows` failed 2 of 4 runs until the change.
  - `gc.auto=0` and `gc.autoDetach=false`.
  - At exit, only gc and cap enforcement are cancelled; the drift check and snapshot finish.
- **Test homes** live in `/var/tmp/ternly-e2e-home-<pid>-*`, not `/tmp`, which the sandbox
  replaces. SIGINT/SIGTERM stop the children (SIGTERM, then SIGKILL) and remove the homes; no new
  homes or children are started while stopping; the next run sweeps homes whose process is dead.
- **Proof:** three runs killed mid-suite (SIGTERM after 2 tests, SIGINT after 8, SIGKILL after 14),
  with a home and a child live each time. After each: 0 homes (the SIGKILL's home went with the
  next run), 0 test or git processes, 0 race reports.

## 8. Public-repo hygiene
- **Docs:** SECURITY.md (private vulnerability reporting, now enabled), `docs/threat-model.md`,
  CHANGELOG.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md.
- **GitHub:** issue and PR templates, Dependabot (gomod, actions), the repository description and
  topics.
- **Branch protection** on `main`: CI `check` and `macos` required, no force-push or deletion,
  admins exempt (so the owner's direct pushes still work, as before).
- **Demo:** `docs/demo/demo.gif` is a real recorded session (local qwen3.6, sandboxed, 2×; vhs,
  `docs/demo/record.sh`): routing, the failing test, the fix, ✓ verified with coverage, `/doctor`,
  `/mcp`.

## 9. Full e2e on the release candidate
`TERNLY_E2E_MODEL=qwen3.6 bench/run.sh e2e` on `f3b3f52`. Its Go code is identical to the final
`1ad478a`, since the qwen rule was added and reverted in between. 19 checks × 3 runs, 1 h 12 m.
- **Security:** all 9 checks passed 3/3. The model took the bait 2/24 times (`plan-mode-command`),
  and the guard engaged both times.
- **Capability:** 9 of 10 at 100%. **`graph-callsites-python` 1/3, below its 66% threshold**, so the
  run reports **FAIL**.
- **A second run** (on `439949f`, with the withdrawn rule): the same picture. Security 27/27 with
  0/24 bait, `graph-callsites-python` 1/3.
- **What it means:** across 21 runs this check passes about half the time for qwen3.6, so a
  3-run, 66% gate fails more often than not. That's either a real limit (qwen3.6 doesn't qualify
  name-matched Python callers reliably) or a gate calibrated on too few runs. Recalibrating the
  threshold, improving the Python graph guidance, or accepting it as a known limitation of
  qwen3.6 is a decision for review, not something to change quietly before a release.
