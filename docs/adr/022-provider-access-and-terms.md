# ADR 022 — Phase B: which sign-ins ternly may use, and connections

Status: proposed (Phase B, for review). The two subscription CLI bridges are **open decisions**
for the owner (below).

## Problem
The v0.2 plan asks for zero-config access to every source the owner is entitled to:
- Ollama (local and cloud);
- the Claude subscription;
- the ChatGPT/Codex subscription;
- API keys.

It prefers keyless setups, and it puts the terms first: "Do not implement any auth path a provider
prohibits or doesn't officially support, even if it technically works."

## What the providers' terms and docs say (read 2026-10-08)
Sources were fetched on 2026-10-08. Quotes are verbatim; some are excerpted because the fetch tool
limits quote length.

### Anthropic: Claude subscription and the `claude` CLI
- **[Claude Code legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)**
  (no date shown):
  - "OAuth authentication is intended exclusively for purchasers of Claude Free, Pro, Max, Team,
    and Enterprise subscription plans and is designed to support ordinary use of Claude Code and
    other native Anthropic applications."
  - "Developers building products or services that interact with Claude's capabilities, including
    those using the Agent SDK, should use API key authentication … Anthropic does not permit
    third-party developers to offer Claude.ai login into their own applications, or to route
    requests through Free, Pro, or Max plan credentials on behalf of their users … developers may
    not collect, store, or intermediate Claude.ai credentials or session tokens."
  - It also says this doesn't "prevent an end user from signing in to the unmodified Claude Code
    binary with their own Claude subscription".
  - And: "Advertised usage limits for Pro and Max plans assume ordinary, individual usage of Claude
    Code and the Agent SDK."
- **[Consumer Terms](https://www.anthropic.com/legal/consumer-terms)** (effective 2025-10-08), §3.7.
  You may not "Except when you are accessing our Services via an Anthropic API Key or where we
  otherwise explicitly permit it, … access the Services through automated or non-human means,
  whether through a bot, script, or otherwise."
- **[Headless mode](https://code.claude.com/docs/en/headless):**
  - `claude -p` is "the Agent SDK via the CLI";
  - "`--bare` … doesn't use your subscription login";
  - `--bare` "will become the default for `-p` in a future release".
- **[The Register, 2026-02-20](https://www.theregister.com/2026/02/20/anthropic_clarifies_ban_third_party_claude_access/):**
  Anthropic then said subscription OAuth "in any other product, tool, or service — including the
  Agent SDK — is not permitted". Today's docs page no longer has that sentence.

**Reading:**
- An Anthropic API key is fully permitted.
- Taking or reusing subscription tokens is prohibited.
- Driving the unmodified `claude` binary, signed in with the owner's own subscription, from another
  program on the owner's own machine is **not clearly permitted or prohibited**:
  - §3.7 bars automated access unless "explicitly permit[ted]";
  - the docs speak of "ordinary, individual usage of … the Agent SDK";
  - `-p` is moving to API-key-only (`--bare`) by default.

### OpenAI: ChatGPT plan and the `codex` CLI
- **[Codex non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)**
  (developers.openai.com/codex/noninteractive redirects here):
  - "Non-interactive mode lets you run Codex from scripts";
  - "`codex exec` reuses saved CLI authentication by default";
  - ChatGPT-managed auth in CI is "advanced": "API keys are the right default for automation" and
    "Do not use this workflow for public or open-source repositories."
- **[Codex auth](https://learn.chatgpt.com/docs/auth):** "Use API key authentication for
  programmatic Codex CLI workflows."
- **[Sign in with ChatGPT for open-source apps](https://developers.openai.com/siwc/token-sharing-open-source):**
  - "your open-source app can request permission to use the user's ChatGPT plan for eligible
    Responses API requests";
  - "These docs explain ChatGPT plan usage for open-source and locally hosted apps".
  - It works through an OAuth client registration. It is preview, and paid or hosted apps need an
    interest form.
- **[Terms of Use](https://openai.com/policies/row-terms-of-use/)** forbid you to "Automatically or
  programmatically extract data or Output". The policy pages returned 403 to the fetch tool; this
  text is from search results quoting the page.
- **[openai/codex discussion #8338](https://github.com/openai/codex/discussions/8338)** has an
  OpenAI engineer (2026-02-09): "our terms of use and code license are quite permissive … I'm an
  engineer, not a lawyer".

**Reading:**
- An OpenAI API key is permitted.
- Driving `codex exec` with the owner's ChatGPT login is documented by OpenAI for scripts, but
  steered to API keys for automation, and in tension with the Terms' programmatic-extraction clause.
  That is **ambiguous**.
- Sign in with ChatGPT for open-source apps is an **official path**. ternly is open-source and
  local, so this fits. But it needs a client registration by the project owner, it's in preview,
  and it's a new OAuth integration. Not built without a decision.

### Ollama: local daemon and cloud
- **[Authentication](https://docs.ollama.com/api/authentication):**
  - "The local API at `http://localhost:11434` does not require authentication."
  - "To use cloud models … sign in to Ollama" (`ollama signin`), after which "Ollama then
    authenticates cloud requests for you", also "through the local API".
- **Usage:** [`GET https://ollama.com/api/balance`](https://docs.ollama.com/api/balance.md) and
  [`/api/usage`](https://docs.ollama.com/api/cloud-usage.md) are documented, with an API key:
  - "10 requests per minute per user … We recommend polling once per minute";
  - current plans report a dollar allowance and balance;
  - legacy plans report session and weekly `remaining_percent`.
- **No account endpoint:** the documented API has no account, plan or sign-in-status endpoint. The
  daemon answers an undocumented `/api/me` with the account's email and plan; ternly doesn't use
  it.

**Reading:** the signed-in daemon is the official keyless path, for both local and cloud models.
Usage is official only through `/api/balance` with `OLLAMA_API_KEY`.

### API keys (Anthropic, OpenAI, and the other builtins)
API keys are the authentication these providers document for programs. ternly has used them since
v0.1 (`discover.Builtins`).

## Decision
**1. Built:**
- **Ollama, keyless:** the signed-in daemon serves local and cloud models (unchanged from v0.1;
  cloud models come from `/api/tags` `remote_host`).
- **Ollama usage:** read from the documented `/api/balance`, only when `OLLAMA_API_KEY` is set and
  the network is allowed (not `--no-net`, not `--local-only`), once per discovery. Discovery runs
  at startup and on Reconnect, well under the 10/min limit.
- **API keys:** as before.
- **Connections** (`discover.DiscoverAll`):
  - every configured source gets a record: how it was found (the env var's *name*, the daemon's
    address), its state, its model count, its usage, and the next step when it isn't connected;
  - the wording is ternly's own, by status class (401/403 refused, 402 no credit, 429, 5xx,
    unreachable, timeout), never the server's body (ADR 001: provider text is untrusted).
- **The ADR 021 producer** (`internal/status`): connections, quota (official, or ternly's failover
  hold), models with trust, routing why/switched, the meter, Explain, Pin, and Reconnect.
- **Where it shows:** `--models` and the TUI's `/status` and `/doctor` (through `tui.App.Status`,
  which main supplies). There are no prompts.
- **Official CLIs** (`claude`, `codex`) are detected on PATH without running them. They are listed
  as "found, not used as a backend", with the reason.

**2. Not built, open decisions for the owner:**
1. **A `claude` CLI bridge** using the owner's subscription.
   - Options:
     - (a) don't build it, and use `ANTHROPIC_API_KEY`;
     - (b) ask Anthropic (the page says "contact sales") whether a local, personal, unmodified-CLI
       bridge is "ordinary, individual usage";
     - (c) build it behind an explicit opt-in, accepting the risk.
   - Note `-p` is moving to `--bare` (API key only) by default.
   - Recommended: (a), with (b) if it's wanted.
2. **A `codex exec` bridge** using the owner's ChatGPT plan.
   - Options:
     - (a) don't build it, and use `OPENAI_API_KEY`;
     - (b) build it behind an explicit opt-in, private machines only (the docs' own rule).
   - Recommended: (a) until (3) is decided, since (3) is the officially supported route.
3. **Sign in with ChatGPT for open-source apps.**
   - This is OpenAI's documented route for an open-source, locally hosted app to use the owner's
     ChatGPT plan.
   - It needs the owner to register an OAuth client (`/siwc/request-client-id`), it's preview, and
     it adds a credential ternly would hold. That means a threat-model change: token storage,
     refresh, and scope.
   - Recommended: decide whether to apply. If yes, a separate ADR covers the token handling first.

**3. The contract is unchanged.** CLIs aren't connections, so ADR 021's `Connection.State` values
are enough.

## Proof
- **discover:**
  - `TestConnections`: real `DiscoverAll` with a fake Ollama (local and 7 cloud models), a working
    keyed provider, a 401 provider whose body is hostile, and a provider without its key.
  - `TestConnectionsOllamaDown`, `TestFailureWording`, `TestOllamaBalance` (current, legacy and empty
    plans), `TestBalanceOnlyWhenAllowed`, `TestFindCLIs`.
- **status:**
  - `TestSnapshot`, `TestExhaustedShowsOnConnection`, `TestRoutingWhyAndSwitch`,
    `TestChangesCoalesced`, `TestExplain`, `TestReconnect`, `TestLines`.
  - `TestSnapshotRedaction`: real discovery with three keys in the environment, providers that echo
    the Authorization header into hostile error bodies, and a failing balance call. The snapshot and
    the explanation carry no key, no "Bearer", no markup and no provider text.
- **`BenchmarkSnapshot`:** 14 µs/op for 50 models and 10 connections (budget 50 µs). It is in
  `bench/perf.json`.
- **Mutation tests:** 16 guards, each broken in turn and caught. The first run had two survivors:
  - the Reconnect re-pin test compared keys, so the stale-pointer bug `SetModels` leaves would pass.
    It now checks the object.
  - Explain's own sort duplicated the router's, so the sort was removed.
- **Live:** `ternly --models` on the owner's machine lists:
  - "Ollama (local): the Ollama daemon at 127.0.0.1:11434 · 12 models · free";
  - "Ollama Cloud: the signed-in Ollama daemon · 14 models · usage unknown (Ollama reports it only to
    OLLAMA_API_KEY)";
  - both CLIs as found but not used.
  - No subscription calls were made: zero through `claude`, zero through `codex`.
- **e2e:** see the PR.

## Not done
- **The TUI's own connections panel** is Phase F, which reads `surface.Snapshot().Connections`.
  Until then `/status` and `/doctor` show the lines.
- **`Snapshot.Plan`** stays empty: the agent has no todo list yet (Phase F's plan panel).
