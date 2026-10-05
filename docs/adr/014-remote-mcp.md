# ADR 014 — M8: remote MCP (Streamable HTTP, OAuth, token storage, per-server network grants)

Status: accepted (M8).

## Grounding (read 2026-10-05)
**The spec.** The latest MCP revision is **2026-07-28**
(modelcontextprotocol.io/specification/2026-07-28; git tag `2026-07-28`). It removes from the
HTTP transport:
- sessions (`Mcp-Session-Id`);
- `initialize`;
- the GET stream;
- `Last-Event-ID` resumability;
- server-to-client requests.

In their place every POST carries the protocol version in `_meta` and in the `MCP-Protocol-Version`
header, plus `Mcp-Method` and `Mcp-Name` headers, with `server/discover` for negotiation.
Authorization adds:
- the RFC 9207 `iss` check;
- `application_type` for Dynamic Client Registration;
- credentials bound to the authorization server's issuer.

DCR and the old HTTP+SSE transport are deprecated.

**Live servers** (probed with curl):
- **Most still speak 2025-xx.**
  - DeepWiki (`mcp.deepwiki.com/mcp`) answers a 2025-11-25 `initialize` over SSE, and answers a
    2026-07-28 request with `400` and a non-modern error (-32600, "Unsupported protocol version").
    That is exactly the spec's fallback trigger.
  - gitmcp.io uses legacy sessions.
- **Context7 speaks 2026-07-28** (`server/discover` → `supportedVersions:["2026-07-28"]`), and
  replies without `jsonrpc` and `id`.
- **OAuth:** Linear and Notion answer `401` with `resource_metadata`. Their authorization servers
  advertise S256, a registration endpoint and Client ID Metadata Documents. Linear also advertises
  `iss`.

## Decisions

### Transport: both eras
One client, `internal/mcphttp`.
1. **Modern first.** Every request is a POST with the 2026-07-28 `_meta` and headers, sent with
   `Accept: application/json, text/event-stream`. The response may be JSON or SSE. On SSE,
   notifications are skipped until the response arrives.
2. **Fall back to legacy.** On `400`/`404`/`405` whose body is not a recognised modern error
   (-32022 with `supported` versions), use 2025-11-25 Streamable HTTP:
   - `initialize`, with the `Mcp-Session-Id` it returns;
   - `notifications/initialized`;
   - the negotiated version in `MCP-Protocol-Version`;
   - `404` re-initializes;
   - `DELETE` on close.
3. **Legacy HTTP+SSE** (2024-11-05, deprecated) is not supported; that is reported, not guessed.
4. **Tolerance:** a response missing `jsonrpc` or `id` is accepted as the answer to the single
   request in flight, as Context7 sends it. Server-to-client requests are declined.

Remote tools are registered exactly like stdio ones:
- named `mcp__<server>__<tool>`;
- descriptions marked as server-provided and withheld when manipulative;
- validated against their schemas;
- results framed as untrusted.

### Authorization: OAuth 2.1 as the spec requires
Triggered on `401`, or `403 insufficient_scope`:
1. **Find the resource metadata:** the `resource_metadata` URL from `WWW-Authenticate`, else the
   two well-known URLs in spec order.
2. **Find the authorization server metadata:** the well-known URLs in spec order (OAuth, then
   OIDC; with and without a path). Use it only if its `issuer` matches exactly. Refuse to proceed
   without `S256` in `code_challenge_methods_supported`.
3. **Register:** a pre-registered client from config if there is one; else a Client ID Metadata
   Document if configured and supported; else Dynamic Client Registration
   (`application_type: native`, a loopback redirect).
   - Credentials are stored per issuer, and a different authorization server means registering
     again.
4. **Authorize:** authorization code with PKCE S256, `state`, `resource` (the canonical server
   URI) and scopes in spec order (challenge, then `scopes_supported`, then none).
   - The redirect is a one-shot loopback listener on `127.0.0.1:<random>/callback`.
   - The browser is opened (`xdg-open` / `open`), and the URL is printed as well.
   - `iss` is checked before the code is redeemed.
5. **Token:** request with `resource` and the verifier; refresh before expiry and on `401`.
   - Step-up requests the union of scopes; a request is retried once after authorization.
6. **Only an explicit login opens a browser.** Start-up and tool calls use stored or refreshed
   tokens; a challenge that needs a login fails with "login required — /mcp login <server>"
   (`Flow.Interactive`). Every endpoint from metadata must be https (http only on loopback) before
   it is used, and the authorization URL is checked again before it is handed to `xdg-open`.
7. **Bearer on every request.** A token is never put in a URL, and is only ever sent to the server
   its authorization server issued it for.

### Token storage
Tokens and client credentials are kept per issuer and resource:
- in the **OS keyring** through its CLI (`secret-tool` on Linux, `security` on macOS) when
  available, which keeps the build static;
- otherwise in `<data>/mcp/credentials.json`, mode 0600.

Either way:
- the data directory is masked from sandboxed commands;
- every token value is added to the redactor, so none reaches a model or a log;
- `/mcp logout <server>` deletes them.

### Per-server network grants
Each remote server gets a grant: the hosts its client may reach. That is the server's own host,
plus its authorization server's hosts once discovered and approved.

`internal/netguard` builds the server's HTTP client:
- it refuses any other host, before connecting;
- it refuses redirects to other hosts, and https → http;
- unless the server itself was configured on such an address, it refuses loopback, private,
  link-local, unspecified and multicast addresses, **checked on the address actually dialed** (DNS
  rebinding);
- metadata that points somewhere new (another authorization server or token endpoint) needs
  approval. At start-up that is a refusal naming the host ("needs login"); `/mcp login` asks (the
  TUI's permission prompt, or y/N on the terminal for `--mcp-login`), saves the host to
  `<data>/mcp/grants.json` for that server **and URL** (pointing the name at another URL voids it),
  and extends the live client's grant (`netguard.Allow`) so the login can proceed.
- the client never uses a proxy (it would be the address dialed); its timeout bounds dial, TLS and
  response headers, not the whole request, since a response may stream; a server that doesn't
  answer within 30 s doesn't hold up start-up.

This is the spec's SSRF guidance applied on the client side.

### Configuration and UI
- **Config:** `mcp.json` accepts `{"url": "https://…"}` servers (and `headers` for API-key servers).
  **Deferred:** remote servers in plugin `.mcp.json` are still skipped. Loading them belongs in the
  plugin review (URL, hosts, scopes), which is more than this milestone needed; the skip message now
  says how to add the server to `mcp.json` instead. `sse` and `ws` are refused by name.
- **UI:** `/mcp` lists servers with transport era, auth state and granted hosts.
  `/mcp login <server>` and `/mcp logout <server>` manage auth; `ternly --mcp-login <server>` does
  the same from a terminal. `--no-net` doesn't start remote servers.
- **Code:** `internal/sse` (shared with the LLM clients), `internal/mcphttp` (transport),
  `internal/mcpauth` (OAuth, credential store), `internal/netguard` (grants),
  `internal/mcpremote` (the manager the TUI and CLI call), `tools/remote.go` (tool registration).

## Dogfooding
Two pieces of M8 were handed to ternly itself (headless, `--mode edits`, one git worktree per run,
the task spec as the prompt, the pre-M8 binary). Each result was reviewed like a contributor's patch.

| Run | Task | Model | Wall | Calls (failed / policy-refused) | Outcome |
|---|---|---|---|---|---|
| 1 | `mcpauth` credential store (keyring + 0600 file) | kimi-k2.7-code:cloud | 3 m 40 s | 29 (7 / 4) | verify passed; **adopted after fixes** (below) |
| 2 | `netguard` | qwen3.6 (local; ~84% on CPU here) | 28 m 45 s | 23 (8 / 7) | **does not compile** (duplicate declarations, a `package main` mid-file, "This file is getting messy"), yet ternly reported **✓ verified**; discarded |
| 3 | `netguard` | glm-5.3:cloud | 30 m (turn limit) | 21 (1 / 1) | a clean, correct `netguard.go` after ~16 min of reasoning, no tests, never built; **adopted** with two additions |

**What the code needed.**
- kimi's store:
  - keyring exit codes misread (`secret-tool` not-found is exit 1 with empty stderr; macOS is 44);
  - the macOS secret on `security`'s argv (visible in `ps`; now `security -i` on stdin);
  - an `init()` with a fabricated comment;
  - no check that the keyring works;
  - tests writing to the real keyring (now opt-in, `TERNLY_KEYRING_TEST=1`);
  - reading before checking it's a regular 0600 file.
- glm's netguard was better than the reference I wrote in parallel on two points: it bounds dial,
  TLS and headers rather than the whole request (an SSE answer may stream), and it follows exactly
  5 redirects where mine followed 4. It lacked a way to extend a grant mid-login (`Allow`) and
  didn't close the request body on refusal (the RoundTripper contract). Tests are mine.

**What it exposed in ternly**, all fixed in this milestone unless marked:
1. **Every model's first command was refused.** `ls -la && cat go.mod`, `cat … | head`,
   `CGO_ENABLED=0 go build …`, `cd <workspace> && go build …`: the policy auto-approved only single
   commands without metacharacters, absolute paths or env prefixes. 12 refusals in 3 runs, each
   costing a turn (on qwen, minutes). Now `safeCommand` (`tools/shellsafe.go`) approves a chain when
   every `&&`/`||`/`;`/`|` segment is a known read-only, build or test command:
   - quotes are understood;
   - `2>&1` and `>/dev/null` are allowed; every other redirect, `$`, backquotes, braces, `!`, `#`
     and a lone `&` still ask;
   - an allowlist of env prefixes (`CGO_ENABLED`, `GOOS`, …; not `GOFLAGS`, `LD_PRELOAD`, `PATH`);
   - absolute paths only inside the workspace;
   - `cd` only with an argument;
   - `mkdir` only in edits/yolo;
   - `-toolexec` and `rg --pre` refused.

   Replaying the 12: 9 now run; 3 still ask (`rm`, `rmdir`, `echo "$?"`). A table test holds
   them verbatim, next to 30 bypass attempts.
   - It also exposed an old bug: `find . …` was never auto-approved (a `\b` after `.`).
2. **"permission denied by user" when no one was asked** (headless). Now "permission denied by
   policy"; "…by user" only when a person said no.
3. **Refused calls were retried** despite "do not retry" (kimi sent the same refused build twice;
   the repeat guard fires at 3). An identical call refused earlier in the turn, in the same mode,
   is now answered at once without re-asking, with alternatives.
4. **No way to delete a file, and `write_file` didn't say it creates directories.** qwen, refused
   `mkdir`, wrote an empty `internal/netguard/go.mod` to make the directory, then was refused `rm`.
   Now there's `delete_file` (a file or an empty directory; a link is removed, never its target;
   checkpointed). `write_file` says it creates parents.
5. **That stray go.mod fooled verification.** It made `internal/netguard` a separate module, so
   `go build ./... && go vet ./...` passed without compiling the broken package, and ternly said
   **✓ verified**. Now a passing Go `./...` check is not believed when a file changed this turn sits
   under a nested go.mod. The model is told which go.mod hides which files
   (`agent/coverage.go`, `TestVerifyNotFooledByNestedModule`).
6. **Silent for up to 25 minutes.** glm streamed 9.6 MB of reasoning and qwen generated on CPU,
   with nothing on screen. The LLM clients now emit progress for non-text chunks (tool arguments,
   reasoning). Headless prints "… generating (N chunks, 3m)" or "… waiting for the model (2m)"
   after 30 s of silence, which tells a slow model from a stalled one.
7. **A spurious "plugins, skills or rules changed on disk" every run.** Logging the watched trees
   every 3 s caught it: Claude Code's skill sync rewrites
   `~/.claude/skills/synced/*/manifest.json` and `.last-complete-round` **unchanged, exactly every
   10 minutes** (21:06:10, 21:16:12). The watcher compared mtimes. It now compares contents
   (re-read only when size or mtime moves) and ignores hidden files.
8. **Memory saved a task requirement as a standing preference.** "Never log or return secrets in
   error messages" came from the store's spec. A bare "always …"/"never …" is now a preference
   only in a short prompt (≤ 400 bytes); in a longer one it needs an explicit marker ("from now
   on", "in this repo", "remember to", …).
9. **Not fixed, recorded:**
   - qwen3.6 runs 84% on CPU on this machine (25 GB at 131k context), which makes it a poor
     dogfooding driver here: 28 minutes for a file that doesn't compile.
   - glm spent 16 of its 30 minutes reasoning before its first edit. There's no reasoning budget
     to set yet.
   - All three models re-read files they had just written; not measured further.

**Verdict.** Cloud models did real work: one usable patch, one good core file. The harness, not
the models, caused most of the friction (items 1–5), and item 5 is the one that mattered: ternly
claimed a verification it hadn't done.

## Measurement
- **Protocol:** full flows against local fake servers in tests:
  - modern;
  - legacy with sessions and expiry;
  - fallback;
  - OAuth: discovery, DCR, PKCE, `iss`, refresh, step-up;
  - grant violations.
- **Live:** DeepWiki (legacy, no auth) and Context7 (modern) for `tools/list` and `tools/call`.
  Linear and Notion for discovery only (read-only GETs): a full login needs a person in a
  browser, and registering a client creates state on their service.
- **Tokens:** one `tools/call` against each live server; the latency of fallback detection.

### Results (2026-10-05)
- **Fake servers** (race detector, all pass):
  - `mcphttp`: modern as JSON, as SSE with a notification first, and bare (no `jsonrpc`/`id`); the
    legacy fallback, a forgotten session re-initialized, `DELETE` on close; 401 → authorize → retry;
    `x-mcp-header` parameters.
  - `mcpauth`: login with DCR and PKCE (the verifier checked by the fake server), refresh with
    rotation, step-up to the union of scopes on the same registration. Refusals: no S256, an issuer
    mismatch, an `iss` mismatch, an authorization server on an unapproved host, an endpoint that
    isn't https. The store: 0600, links refused, the index; the real keyring opt-in.
  - `netguard`: host and host:port, case, default ports, schemes; exactly 5 redirects; a redirect
    away refused before the target is hit (and followed after `Allow`); https → http; loopback,
    the metadata service and a rebinding resolver refused at dial time; 17 addresses classified.
  - `mcpremote` end to end: a protected server whose authorization server is on another port.
    - Start-up says "needs login" without opening a browser.
    - A declined host grants nothing and is never contacted.
    - An approved one is saved and the login runs; the tools register.
    - The token is redacted.
    - A restart reuses the token and the grant without a login.
    - Moving the name to another URL voids the grant; logout.
- **Live, through the manager** (`TERNLY_MCP_LIVE=1`):
  - Four servers started concurrently in **1.46 s**.
  - DeepWiki (2025, sessions; fallback detected from the modern probe's 400):
    `read_wiki_structure` in **0.35–0.52 s**.
  - Context7 (2026-07-28): `resolve-library-id` in **1.1 s**.
  - Linear and Notion: "login required — /mcp login …". Their authorization servers are on their
    own hosts, so nothing needed approval; no registration was made and no browser opened.
  - Directly through `mcphttp`, connecting took 1.33 s on DeepWiki (probe + `initialize`) and
    0.72 s on Context7.
- **Not measured:** a full browser login against a real service. It needs a person, and
  registration creates state on their side. The flow is covered by the fake authorization server,
  and the user can run `ternly --mcp-login linear` to try it.
