# netguard scorer

A black-box scorer for the netguard task: the spec is
`bench/dogfood/2026-10-06-m9/watchdog/netguard.md`, a local copy of ADR 015/016's dogfood brief.
It is used to judge a model's implementation:

```sh
bench/netguard/score.sh <ternly checkout | a dir with netguard's .go (or saved .go.txt) files>
# core 7/7, extra 2/2
```

**What it runs.** `score_test.go.txt` is copied into the candidate's `internal/netguard` as an
external test package, so it reaches only the public API: `Grant`, `Client` and `ErrNotGranted`.
The candidate's own tests are left out.

**The core checks** are the spec's own list of what to test:
1. a granted host;
2. an ungranted host, refused with `ErrNotGranted` before any connection;
3. a redirect to a granted host;
4. a redirect elsewhere;
5. https → http;
6. private addresses (127.0.0.1, 169.254.169.254, and a name that resolves to loopback), refused
   without a timeout;
7. host:port matching, and case-insensitive hosts.

**The extra checks** are the spec's two other rules: only http and https, and at most 5
redirects.

**Provenance.** ADR 015's original scorer ("7 checks, calibrated at 7/7 on ternly's own
netguard") wasn't saved. This one was recreated on 2026-10-07 from the spec.

**Calibration:**
- ternly's own `internal/netguard`: core 7/7, extra 2/2.
- Every saved run's code scores core 7/7, as the original recorded. That is 15 implementations:
  ADR 015 batches 1–2, ADR 016, Phase A and the v0.1.1 re-run.
- ADR 015 batch 2 "low" fails the extra scheme check: its client accepts a `file://` redirect.

**Broken on purpose,** ternly's netguard with one rule removed:

| Removed | Score |
|---|---|
| private-address check | core 6/7 |
| https → http check | core 6/7 |
| the host grant | core 4/7 |
| the redirect limit | extra 1/2 |
| the scheme check | extra 1/2 |
