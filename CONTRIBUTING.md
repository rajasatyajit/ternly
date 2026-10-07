# Contributing

Thanks for helping. ternly is a small codebase with strong opinions about evidence; these notes
save a round trip.

## Before you start
- **Bugs:** open an issue with `ternly --version`, the OS, the mode, and what you saw versus what you
  expected. **Security issues** go through [SECURITY.md](SECURITY.md), not issues.
- **Bigger changes:** open an issue first. Design decisions are recorded as ADRs in `docs/adr/`, and
  a change that alters one adds or amends an ADR.

## The fast tier, before every push
`make hooks` installs a pre-push hook (`.githooks/pre-push`, ~20 s): gofmt, `go vet`,
`go test -short ./...` and the confinement lint (`TestOnlyConfinedReads`, ADR 013). It's the tier
that catches most CI failures before they cost a round trip. `git push --no-verify` skips it once.

## The gate (CI runs it; run it locally first)
```
gofmt -l .                                   # prints nothing
go vet ./... && GOOS=darwin go vet ./...
go vet -tags e2e . && go vet -tags ternly_all_grammars ./internal/graph
go test -race ./...                          # Linux: install bubblewrap and ripgrep
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...   # and with GOOS=darwin, and -tags e2e .
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
CGO_ENABLED=0 go build -trimpath -o ternly . # static
```
Changes to routing, prompts, guards or tools also run the real-model suite against a local model:
`TERNLY_E2E_MODEL=<model> bench/run.sh e2e` (isolated HOME, throwaway workspaces). Paste its summary
in the PR.

## Conventions
- **Go standard library first.** A new dependency needs a reason in the PR (and the license in
  NOTICE if it requires one).
- **Match the surrounding code:** short doc comments that say why, no dead code, tests next to the
  code. A fix comes with the test that fails without it.
- **Claims need evidence:** a number, a test, or a log line. "Should work" isn't evidence.
- **Commit messages** say what changed and why; reference the ADR when there is one.

## License
Contributions are under the Apache License 2.0 (see LICENSE). By opening a PR you agree to license
your contribution under it.
