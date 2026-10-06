# M9 — release readiness (v0.1.0): proposed plan

Status: approved 2026-10-06 with decisions; executed in M9 — see docs/adr/016-m9-release.md.

## What exists already
- `.goreleaser.yaml`: static linux/darwin × amd64/arm64 builds, `-trimpath -s -w`, the version
  through ldflags, tar.gz archives with README, LICENSE and NOTICE, sha256 checksums. A snapshot
  build exists in `dist/`, made from an older commit.
- `packaging/aur/ternly-bin/PKGBUILD`, with `sha256sums=SKIP` placeholders.
- **The GitHub repository is already public** (`gh repo view`: PUBLIC). Its description is empty
  and it has no topics.
- CI: Linux (gate + race tests) and macOS jobs. There is no release workflow.

## Findings that shape the plan
1. **macOS has no sandbox.** bubblewrap is Linux-only. On macOS, shell commands, hooks and MCP
   servers run unconfined. The README's security section doesn't say so. Either add a Seatbelt
   (`sandbox-exec`) profile or document the gap prominently before release. (Proposed: document it
   in v0.1.0 and add Seatbelt in v0.2. `sandbox-exec` is deprecated but still the only built-in
   option.)
2. **Names** (checked 2026-10-06; links and evidence in the review report):
   - ternly.com is registered (no site); ternly.dev was registered 2026-08-31 and is parked;
     ternly.app (2026-02) is parked.
   - **ternly.io, ternly.sh and ternly.ai look unregistered.**
   - npm, PyPI, crates.io, Homebrew, the AUR, Docker Hub and the GitHub org/user `ternly` are free.
   - No TERNLY mark turned up in a web search, but USPTO, TMview and WIPO couldn't be queried by
     script, so **they need a manual search**.
   - "Turnly" (a queue-management SaaS, turnly.app) sounds the same; in Class 9/42 that's a
     similarity risk to weigh.
3. **The AUR maintainer line** in the PKGBUILD reads `satyajit.rajaraman@gmail.com`, which isn't
   the account address. Confirm which one you want public.

## Work
### A. Release pipeline (goreleaser)
- **Workflow:** `.github/workflows/release.yml` on `v*` tags, using goreleaser-action, pinned by
  commit SHA. It:
  - runs the gate first;
  - publishes the release with checksums;
  - attaches an SBOM (syft, SPDX) per archive;
  - signs the checksums with cosign keyless (GitHub OIDC), so users can run `cosign verify-blob`;
  - adds a SLSA build-provenance attestation (`actions/attest-build-provenance`).
- **Release-commit gate:** `goreleaser check`; full `TERNLY_E2E_MODEL=<local> bench/run.sh e2e`
  on the tagged commit; a smoke test of each archive:
  - linux/amd64 natively;
  - linux/arm64 under qemu;
  - darwin/arm64 on the macOS runner.

  The smoke test is `ternly --version`, plus a scripted `-p` turn against a fake provider.
- **Notes:** a hand-edited `CHANGELOG.md` (goreleaser's commit list as a draft only) and release
  notes listing known limitations, the macOS sandbox first.
- **License audit:** `go-licenses` over the 59 modules. NOTICE already carries the grammar notices;
  add any new ones (mvdan.cc/sh is BSD-3).
- **Optional:** a Homebrew tap (`ternly/homebrew-tap`) via goreleaser's `brews`. It needs the
  GitHub org claimed (see E).

### B. AUR
- **After the release:** `updpkgsums`, then `makepkg --printsrcinfo > .SRCINFO`, then
  `namcap PKGBUILD *.pkg.tar.zst`. Test the install in a clean Arch container (ternly runs, and
  bwrap is picked up as an optdepend).
- **Publish:** push to `ssh://aur@aur.archlinux.org/ternly-bin.git`. That needs your AUR account and
  SSH key, so you do it, or you authorize me to.
- **Upkeep:** a `packaging/aur/update.sh` that fills in the version and checksums from the release's
  `checksums.txt`, so every later release is one command.

### C. Public repository hygiene (it's already public)
- **History audit:** run gitleaks and trufflehog over the full history for secrets; report, and
  rewrite history only if something real turns up (your decision). Check committed test data and
  examples for personal paths or emails.
- **Metadata:** a description; topics (`coding-agent`, `llm`, `cli`, `go`, `mcp`); social preview.
- **Policy:** branch protection on `main` (CI required); Dependabot for gomod and actions; issue
  and PR templates; CONTRIBUTING.md; a code of conduct.

### D. SECURITY.md and the threat model
- **SECURITY.md:** how to report (GitHub private vulnerability reporting, which needs enabling),
  supported versions (latest minor), the disclosure timeline, and scope.
- **`docs/threat-model.md`:**
  - **Assets:** provider keys, OAuth tokens, workspace and home files, the user's other repos, money
    (budgets).
  - **Adversaries:**
    - a malicious repository (files, links, `.mcp.json`, build scripts);
    - prompt injection through tool output, the web and MCP;
    - a malicious or compromised MCP server or plugin;
    - a model that takes the bait;
    - a malicious authorization server;
    - the network (SSRF, DNS rebinding);
    - a local unprivileged user.
  - **Trust boundaries and controls:** each mapped to the ADR that decided it:
    - confinement (rootfs, os.Root);
    - the permission policy and the AST classifier;
    - bwrap;
    - framing and the injection flagger;
    - the trust profile;
    - netguard grants;
    - OAuth checks;
    - the redactor;
    - verification coverage.
  - **Residual risks and non-goals:**
    - macOS is unsandboxed;
    - yolo mode;
    - build and test commands run repository code by design;
    - the keyring CLI backends;
    - model-provider data handling.
  - **Evidence:** the security e2e checks and fuzz targets, with how to rerun them.

### E. Names and domain
- You run the manual trademark searches (USPTO, TMview, WIPO, EUIPO); I prepare the exact queries.
- Decide on one of ternly.io, .sh or .ai, and register it if wanted.
- Claim the GitHub org `ternly` (useful for a tap and later moving the repo).
- Note: everything here costs money or creates accounts, so it's yours to do.

### F. README demo GIF
- **Recording:** a `vhs` tape (`docs/demo.tape`, committed), recorded from a real session, sped up
  where waiting, with no keys or paths on screen, under 2 MB. Storyline (~40 s):
  1. routing picks a cheap model, then escalates;
  2. an edit;
  3. **✓ verified** with coverage shown;
  4. a refused injected command;
  5. `/mcp` with a remote server.
- **Model:** a cloud model through Ollama, for speed. Recorded honestly: no fake provider, and
  anything cut is said in the caption.

## Exit criteria (the review report will show each)
- v0.1.0 tag; release with checksums, the signature, SBOMs and provenance; `cosign verify-blob`
  demonstrated.
- e2e on the tagged commit, with the security checks at 100%.
- Archive smoke tests on 3 platforms.
- AUR package live (or ready to push), with real checksums; the clean-container install log.
- SECURITY.md and the threat model reviewed; the macOS gap documented in the README.
- History audit report.
- The demo GIF in the README.

## Decisions needed from you
1. macOS sandbox: document now and add Seatbelt in v0.2 (proposed), or Seatbelt before v0.1.0?
2. Signing: cosign keyless (proposed), or no signing?
3. Homebrew tap now or later? (It needs the `ternly` GitHub org.)
4. AUR push: you, or me with your key?
5. Domain choice (.io / .sh / .ai), and the PKGBUILD maintainer email.
