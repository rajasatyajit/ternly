# ADR 031 — Background work: idle only, never on a spent quota, conditional catalog fetches

Status: accepted (F2 item (b), the owner's decisions of 2026-10-11).

The owner's decisions, verbatim:
- "(b) is a bug: never schedule an eval when quota is exhausted (check balance first); fetch the
  catalog conditionally (ETag/If-Modified-Since), no unchanged re-download."
- "Background work: pause evals and catalog refresh during active sessions; run only when idle,
  quota available and on AC power. My config stays as is. New installs: evals opt-in via a
  first-run question; catalog refresh idle-only + conditional."

## What the F2 survey found
In a fresh home, ternly started a `glm-5.3:cloud` background evaluation and a catalog download
while the user sat at the prompt. Idle CPU measured 10–40 s after start was 2.2% in a fresh home
and 3.5% on a second start in the same home; Phase F had measured the UI alone at 0.3–1.0%.
The Ollama Cloud monthly limit was already spent (HTTP 429 on 2026-10-09/10), so the evaluation
could only fail.

## 1. Quota: checked before anything is scheduled
- **The quota log** (`<data>/quota.json`, 0600, versioned, written atomically) records the last
  quota hit per quota group: a subscription's cloud models share one group (`ollama:cloud`); a
  pay-per-token API's models share theirs; local models have none.
  - The agent records every quota answer (HTTP 429, or a usage-limit message): cloud models via
    `Router.MarkExhausted` (also takes them out of routing, as before), pay-per-token models via
    `Router.NoteQuotaHit` (routing keeps them; background work doesn't touch them).
  - Every ternly process writes and reads the same file: an evaluation's trap runs record the hit,
    and the session's scheduler and the evaluation's driver read it.
- **The gate** (`quotaGate`, checked last in `eligible`, so before every schedule and in `/models`):
  no room when routing has the model out (a 429 this session), when the log has a hit for the
  group whose hold hasn't ended, or — for Ollama Cloud with `OLLAMA_API_KEY` and the network
  allowed — when the documented `ollama.com/api/balance` says the included allowance is spent
  (cached 10 minutes; recorded in the log until its reset). Purchased credit is spending, not
  room: background work stays at $0.
- **How long a hit holds:** the later of the provider's reset (Retry-After, or the balance's period
  end) and **24 hours** after the hit. A monthly limit isn't probed hourly by work nobody waits
  for; a day later one request finds out. A short Retry-After (a rate limit) also holds a day:
  background work loses nothing by waiting.
- **A hit during an evaluation stops it.** The driver checks the log after each trap and exits
  with code 75 (EX_TEMPFAIL); the scheduler also re-checks the gate every 10 s while an
  evaluation runs. The outcome `stopped: quota exhausted` doesn't count against the caps or as the
  model's last attempt: it's retried once the quota has room. Nothing is saved.
- **Without a key, the first 429 is the signal.** ternly can't read Ollama's balance without
  `OLLAMA_API_KEY`; a session's own 429s (interactive or background) are recorded, and from then
  on background work leaves the quota alone.
- A trap run (`TERNLY_EVAL=1`) refuses `--eval`: no chain of evaluations.

## 2. When background work runs
Background evaluations and the catalog refresh run only when **all** hold (`internal/idle`):
- **no turn is running;**
- **no input for 5 minutes** (`idle.After`): a key press or paste in the UI resets the clock (the
  UI's model is wrapped in `main`; `internal/tui` is unchanged). The clock starts at launch, so
  nothing runs in the first 5 minutes. Why 5 minutes: an evaluation takes 5–20 minutes and
  competes with the user's own turns for the model (and the GPU, locally). A pause to think
  between prompts (seconds to a couple of minutes) shouldn't start work that then has to pause at
  once; a user who has typed nothing for 5 minutes has usually stepped away.
- **AC power:** Linux reads `/sys/class/power_supply/*/{type,online,scope}`: a Mains or USB supply
  online means AC; no system battery (devices' batteries, scope "Device", don't count) means AC (a
  desktop); a system battery with no supply online means battery. macOS reads `pmset -g batt`
  ("'AC Power'" or "'Battery Power'"). **Unknown counts as AC** (no supplies listed: containers,
  some VMs; other platforms): refusing would turn background work off for good where ternly can't
  tell. Read at most every 30 s.

**Pausing.** When the session stops being idle:
- **An evaluation pauses between traps, not by stopping processes.** The scheduler writes "p" to a
  control pipe (the driver's fd 3); the driver cancels the trap in progress (its result is
  discarded) and waits; on "r" it runs that trap again and goes on. Completed traps are kept, so
  it resumes rather than restarts. A pipe, not signals: nothing is lost or fatal before the
  driver is ready, and the pipe's end tells the driver its session is gone. SIGSTOP was rejected:
  a stopped trap's request to a remote model times out on the server side, and the 8-minute
  per-trap timer runs on through the stop.
- **The time cap counts running time only** (20 minutes per model by default).
- Across ternly restarts an evaluation starts over: saving per-trap results is Phase C item 0.
- **The catalog refresh** waits before each request (a source, a registry page, an npm page)
  until allowed, so the user's return pauses it after the request in flight (at most one page).
- **Headless runs** (`-p`) and evaluation runs never do background work; before this change the
  catalog refresh also ran under `-p`.

## 3. Evaluations are opt-in
- On an interactive start with no `routing.background_eval.enabled` in the config, ternly asks once
  on stderr, before the UI takes the terminal:
  "Run background capability evaluations when idle? [y/N]". The answer (y/yes → true, anything
  else → false) is written into `config.json` (created 0600 if missing, every other key kept, a
  string `"routing"` turned into `{"version": …}`, a symlinked config written through the link).
  EOF saves nothing and leaves it off.
- **Never asked:** when the value is set (the owner's `false` stays as is), when stdin or stderr
  isn't a terminal (headless, piped), under `TERNLY_HARNESS`, `TERNLY_BACKGROUND_EVAL`,
  `TERNLY_EVAL` or `CI`. Not asked means off (`bgeval.DefaultCaps.Enabled` is now false).
- **For harness runs in a fresh HOME** (tuiprobe, e2e): set `TERNLY_HARNESS=1` or
  `TERNLY_BACKGROUND_EVAL=1`, or write the config first, or the question waits for an answer.
  The e2e helpers already set `TERNLY_BACKGROUND_EVAL=1`; F2's probe writes the config.

## 4. Conditional catalog fetches
| Source | Body | Validators (probed 2026-10-11) | Now |
|---|---|---|---|
| 5 Claude Code marketplaces (raw.githubusercontent.com) | 2 KB – 1.57 MB | ETag | `If-None-Match`; 304 → nothing downloaded or indexed |
| Gemini gallery (geminicli.com/extensions.json) | 1.79 MB | ETag, Last-Modified | both sent; 304 |
| MCP registry `/v0.1/servers` | ~79 KB a page | **none** | full crawl once (resumable by cursor), then `updated_since` deltas: only changed servers |
| npm search (`keywords:mcp-server`) | ~316 KB a page | **none** | body hash; searched **weekly**, not daily |

- Validators (ETag, Last-Modified, SHA-256 of the body) are kept per URL in
  `<data>/catalog/state.json`, **only after that source's entries are in the index**. A 304, or a
  200 whose hash matches, is not decoded or indexed (no log growth, no index rebuild). An emptied
  index always fetches in full. The registry's URLs carry a cursor or a timestamp and are never
  asked twice, so no validators are kept for them.
- **Interrupted refreshes resume.** State is saved after each source, and a full registry crawl
  saves its cursor after each page; a refresh only counts as done (`Due` false for a day) when it
  completes. Before, an interrupted first refresh saved nothing, and the next start downloaded
  everything again.
- `Due` reads `state.json` only; the index (a log replay) is opened when something needs it.
- `/plugin catalog refresh` (the user asked) runs at once; an idle refresh waiting for idle stops
  waiting and finishes first.

## Measurements
Measured 2026-10-11 on the owner's laptop (on AC power), with `bench/background/run.sh`:
- **Binaries:** main 1b0085a ("old") and this change ("new"), static builds.
- **Homes:** fresh `HOME`s. The old binary ran with `TERNLY_BACKGROUND_EVAL=1`, so it didn't
  evaluate: Ollama Cloud's quota was spent, and a 429-bound evaluation is what the survey already
  measured. The new binary's first-run question was answered N.
- **How each cell ran:** the UI in a pty (`bench/tuiprobe`, from track2/f2-probe). CPU is the
  process tree's CPU % over each window. Bytes are what each catalog host sent through
  `bench/countproxy`, an HTTPS CONNECT proxy, on the wire (gzip and TLS included).
- **Timing:** each cell held the shared lock (`flock -o`).

**Bytes from the catalog's hosts:**

| Scenario | Old | New |
|---|---|---|
| First start, fresh home: the full first refresh | 39.3 MB, from 10 s after start | 39.3 MB, from 5 min of idle (~5 min 40 s after start) |
| Second start, same home, first refresh completed | 0 | 0 |
| A day later (the daily refresh) | **1.19 MB** (marketplaces 565 KB, Gemini 337 KB, npm 277 KB, registry delta 13 KB) | **26 KB** (7 conditional requests answered 304 = 13.7 KB, registry delta 12.5 KB; npm is weekly) |
| First start interrupted, then a second start | 8.0 MB by 40 s, then **39.3 MB again** on the second start (nothing was saved) | 3.6 MB by ~340 s; the second start resumed the registry crawl from its saved cursor (2 marketplace/Gemini 304s; 23.4 MB of registry pages in its 5-minute window; the crawl's remaining pages next time) |

- **The survey's "9.9 MB at a second start" is the interrupted case.** A first refresh cut short
  saved no state, and the next start downloaded everything again.
- **The full first crawl of the MCP registry, about 38 MB gzipped, dominates.** It happens once,
  and only while idle.
- **"About 0 when unchanged" holds:** 0 bytes on a second start, and 26 KB a day later instead of
  1.19 MB.

**Idle CPU (process tree):**

| Window | Old | New |
|---|---|---|
| 10–40 s, fresh home | 3.49% (refresh running) | 0.87%, 0.40% (nothing running) |
| 10–40 s, second start | **9.13%**: `Due` opened the catalog index, replaying its log | 0.87%, 0.40% (`Due` reads `state.json` only) |
| 10–40 s, a day later | 11.78% (index open plus the refresh) | 0.97% |
| 40 s–5 min, background idle-gated (new) | — | 0.85%, 0.86%, 0.79%, 0.44%, 0.36% |
| The first refresh while it runs | 1.62% (10–340 s) | 1.86% (295–625 s) |
| The daily refresh while it runs | 1.01% (40–160 s) | 3.66% (295–385 s: opening the 40 MB index once to apply the registry delta) |

- **The UI-only target (≤ 0.5%) is not met consistently.** The windows where nothing in the
  background runs (no catalog bytes, no evaluation) measure 0.36–0.97%, in two clusters (about
  0.4% and about 0.85%) that this run didn't explain. Phase F measured the UI alone at 0.3–1.0%.
- **This is the UI's own idle cost** (F2 item (a), the other track), no longer background work:
  the background scheduler polls every 30 s and the idle gate reads sysfs at most every 30 s.
- **Background work at idle startup went from 3.5–11.8% to nothing.** It now runs only after
  5 minutes without input.


## Proof
- Tests: success and failure paths for each guard — idle `TestGateConditions`,
  `TestNewWaitsAfterStart`, `TestWaitReturnsWhenAllowed`, `TestSysfsPower`, `TestPmsetPower`;
  bgeval `TestOffByDefault`, `TestNothingStartsUnlessIdle`, `TestPausesAndResumes`,
  `TestQuotaExhaustedSchedulesNothing`, `TestQuotaHitStopsEvaluation`; discover `TestQuotaGroup`,
  `TestQuotaLogExhausted`, `TestMarkExhaustedPersists`; capability `TestConditionalFetch`,
  `TestNoValidatorsHashSkip`, `TestValidatorsOnlyAfterIndexing`, `TestRefreshIdleWaits`,
  `TestRegistryCrawlResumes`, `TestDueDoesNotOpenStore`; main `TestQuotaGate`, `TestShouldAsk`,
  `TestAskBackgroundEval`, `TestSetBackgroundEvalKeepsConfig`, `TestActivityTouches`,
  `TestRunPausable`, `TestEvalControl`, and end to end `TestEvalStopsOnQuota` (a real binary, a
  provider answering 429: exit 75 after the first trap, the hit recorded, nothing saved) and
  `TestEvalRefusedInTrapRun`.
- **Mutation runs:** 40 mutations, one or more per new guard. Each was applied, built and run against its guarding test, and all 40 were killed.
  - The first round found 4 weak or invalid mutations (3 didn't build, 1 was a no-op) and 2 survivors:
    - `shouldAsk`'s explicit-value check could only be tested from a terminal. It now takes `interactive`.
    - Commit-after-index wasn't exercised with a non-empty index. The strengthened test also
      found a real bug, fixed here: after the index was emptied, the first source refilled it and
      the rest answered 304 and were never indexed. Now an empty index at the start of a refresh
      clears all validators.

## Not done
- Per-trap results saved across restarts (Phase C item 0).
- The quota log is read-modify-write without a file lock; two processes recording a hit at the
  same instant can lose one mark (the next 429 records it again).
- Catalog requests in flight when the user returns finish (one page, at most ~2 MB).
