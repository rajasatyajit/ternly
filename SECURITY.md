# Security policy

ternly runs model-chosen commands on your machine, reads your code, holds your API keys and OAuth
tokens, and installs other people's plugins. Security reports are welcome and taken seriously.

## Reporting a vulnerability
Please report privately, not in a public issue:

- **GitHub private vulnerability reporting** (preferred):
  [Report a vulnerability](https://github.com/rajasatyajit/ternly/security/advisories/new).
- Or email `maintainer at ternly dot sh`, once that address is live.

Include what you ran (version from `ternly --version`, OS, mode), what happened, and what should
have happened. A minimal repository or prompt that reproduces it helps most.

You can expect an acknowledgement within **3 working days** and an assessment within **10**. Fixes
for confirmed issues are released as soon as they are ready. The advisory is published with the fix
and credits you, unless you'd rather not be named. Please give us **90 days** before disclosing, or
less if the fix ships sooner.

## Supported versions
| Version | Supported |
|---|---|
| latest 0.x release | ✓ |
| older releases | no: upgrade |

## In scope
Anything that lets a repository, a web page, a tool's output, an MCP server, a plugin or a model:
- read files outside the workspace, or ternly's keys, tokens, memory or sessions;
- run a command or an edit that the permission policy should have stopped, or escape the
  sandbox (Linux, bubblewrap);
- reach a network host that a remote MCP server's grant doesn't include, or get a token sent to
  the wrong server;
- make ternly report **✓ verified** for changes that no check covered;
- break plugin pinning or review (code that runs without your approval).

## Out of scope, or known
- **macOS has no sandbox in v0.1** (experimental). There, every shell command asks and plugin code
  is disabled. Reports that depend only on "the command wasn't sandboxed on macOS" are known.
- `--mode yolo`, `--no-sandbox`, and commands you approved yourself do what they say.
- Build and test commands run the repository's own code by design: run ternly on code you'd build
  yourself, or keep the sandbox on.
- What a model provider does with the data you send it.

The threat model is in [`docs/threat-model.md`](docs/threat-model.md).

## Verifying a release
Releases are built by `.github/workflows/release.yml`. `checksums.txt` is signed keyless with
cosign, and every archive carries a GitHub build-provenance attestation; the release notes show the
commands. To check the signature:

```
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rajasatyajit/ternly/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
