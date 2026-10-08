# ADR 027 — Sign in with ChatGPT: how ternly would hold the tokens

Status: proposed (design only; nothing is implemented). Owner's decision (Phase B review,
2026-10-08): apply for OpenAI's Sign in with ChatGPT for open-source apps. This ADR comes first.
The feature sits behind a flag and blocks nothing.

Number: 025 and 026 are left to the core-fixes and perf-gate work running at the same time.

## What OpenAI's flow is (read 2026-10-08)
Sources:
- [overview](https://developers.openai.com/siwc/token-sharing-open-source)
- [sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
- [accounts and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)
- [token reference](https://developers.openai.com/siwc/token-sharing-open-source/token-reference)
- [errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)

The pages show no dates. The preview-limitations page couldn't be fetched, so it is an open item.

**Registration happens during the first sign-in, not in a form.**
- The open-source flow starts with `client_id=dynamic_agent_client`, plus an `ext_agent_host_id`
  and an `agent_name_hint`.
- In the browser, the owner names and authorizes the agent. The callback then returns the issued
  `client_id` (`oaiapp_…`), bound to that user and workspace.
- It is a public client: "No client secret is required."
- Paid or remotely hosted apps go through an interest form instead. ternly is open-source and runs
  locally, so it doesn't.
- **So the owner's action is that first sign-in and consent.** ternly never automates it.

**The OAuth flow:**
- Authorization code, with PKCE S256 and an OIDC `nonce`, through the system browser.
- Endpoints:
  - authorize: `https://auth.openai.com/api/accounts/authorize`
  - token: `https://auth.openai.com/api/accounts/oauth/token`
  - discovery: `https://auth.openai.com/.well-known/openid-configuration`, which gives
    `revocation_endpoint`.
- Parameters:
  - `resource=https://api.openai.com/v1`
  - `scope=openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`
- The redirect is `http://127.0.0.1:<port>/auth/callback`. "Only the port may vary", and not
  `localhost`.

**Tokens:**

| Token | Lifetime | Notes |
|---|---|---|
| access | 1 hour | |
| refresh | 30 days | rotating: each refresh returns a new one with a fresh 30 days |
| ID token | not stated | kept for `id_token_hint` |

- Plan usage is authorized only if `chatgpt.tokens.use.direct` was granted. "A valid ID token alone
  does not authorize ChatGPT plan usage."
- The token is for `POST /v1/responses` only. Another route returns
  `subscription_sharing_route_not_supported`.

**Refresh and revocation:**
- Refreshes must be serialized: "so two processes do not race a rotating token". The access token,
  expiry, scopes and refresh token are replaced together.
- On `invalid_grant`, `refresh_token_reused`, `refresh_token_expired` or a similar error, the tokens
  are cleared and the owner signs in again with the saved client ID.
- Sign-out revokes the refresh token at `revocation_endpoint`. An empty 200 means success, even
  for a token that's already invalid. The client registration and host ID survive sign-out.
- The owner can disconnect the app in ChatGPT Settings. OpenAI doesn't notify the app; it learns
  when a request or refresh fails.

**Storage guidance (OpenAI):**
- "Write files atomically with owner-only permissions (`0600` on Unix), and never commit or log
  them."
- Tokens are never put in URLs. The ID token goes only to the authorize endpoint.

**Limits:**
- ChatGPT Plus's five-hour limit is shared across every app using the plan. Pro has none.
- `subscription_sharing_usage_limit_exceeded` is a 429. There's no silent fallback to another
  billing path.

## Decision (to implement once approved)

### 1. Behind a flag, off by default; nothing blocks on it
- **Config:** `"chatgpt_signin": true` opts in. Without it:
  - no code path reaches auth.openai.com;
  - no credential file or keyring entry is read;
  - `--chatgpt-login` and `--chatgpt-logout` explain how to turn the feature on.
- **Not under `--no-net` or `--local-only`:** with either, it stays off even when configured.
- **It never blocks:**
  - discovery looks up the credential with the same short timeouts as other sources;
  - a refresh or eligibility failure makes the connection `error`, with ternly's wording and the
    next step;
  - startup, routing and every other source go on.
- **Sign-in is only ever started by the owner:** `ternly --chatgpt-login`, or `/chatgpt login` in
  the TUI. A model has no tool that starts it. Prompt injection can't open a browser or trigger a
  consent.

### 2. Where the tokens live: OS keyring first, then a 0600 file
This reuses ADR 014's store (`internal/mcpauth`), keyed by issuer and resource:
- **First choice:** the OS keyring through its CLI (`secret-tool` on Linux, `security` on macOS).
  ternly probes that the keyring works, as for MCP.
- **Fallback:** `<data>/chatgpt/credentials.json`, written atomically with mode 0600. Links are
  refused. This is the storage OpenAI documents.

Either way:
- **The data directory is masked from sandboxed commands,** so model-run shell commands can't read
  it.
- **The record holds** issuer, `sub`, `client_id`, `ext_agent_host_id`, the three tokens, token
  type, expiry, granted scopes, `earliest_refresh_at` and `saved_at`. There is one record per
  issued client ID and verified identity, and a record is never overwritten by another account's.
- **A non-secret index** in `<data>/chatgpt/clients.json` maps account (`sub`) to `client_id`. It
  survives logout, so re-sign-in reuses the client.
- **Not kept:** email and name from the ID token. The UI shows "signed in" and the plan state, not
  identity. `login_hint` is not used.

### 3. The host ID
- **`ext_agent_host_id` is `urn:uuid:<v4>`,** generated once per machine and stored in
  `<data>/chatgpt/host-id` (0600).
- **Why not a JWK thumbprint,** which OpenAI recommends: OpenAI states it doesn't verify
  possession of the key. A key would add key material to protect for no security gain.
- **It's opaque, not a credential,** and not identifying. Copying the data directory to another
  machine would copy it, so ternly records the machine's ID next to it and regenerates the host ID
  if they differ. OpenAI requires a distinct host ID per host.

### 4. Scope: exactly what's needed, used only where allowed
- **Requested scopes:** the six above and no more. Plan usage is enabled only if
  `chatgpt.tokens.use.direct` is in the *granted* scopes. Otherwise the connection says "the plan
  scope wasn't granted", and points to an API key.
- **The access token is sent only to `POST https://api.openai.com/v1/responses`,** by a dedicated
  client whose allowlist is that host and path. A test checks that no other request ever carries
  it.
- **Requests use `store: false`.**
- **ID tokens are validated:** JWKS signature, `iss`, `aud` = the issued client ID, `exp`, `nonce`.
  The validated `sub` is the identity, and it must match before a re-sign-in replaces a record.
- **The callback is checked:**
  - `state` must match;
  - a `client_id` that differs from the saved one is rejected;
  - on `access_denied`, ternly stops.

### 5. The loopback callback
- **Listener:** bound to `127.0.0.1` only, on a random port per attempt. It is started before the
  browser opens, serves exactly `/auth/callback`, takes one request, and expires after 5 minutes.
- **Protection:** PKCE and `state` protect the code against another local process racing the port.
  The response page says only "you can close this tab".
- **The authorize URL is fixed in ternly,** not read from config or discovery, so a hostile
  config can't send the owner to a look-alike page. It doesn't carry `id_token_hint`, so nothing
  printed or logged holds a token.

### 6. Refresh
- **When:** proactively, when under 5 minutes are left (and not before `earliest_refresh_at`).
  Otherwise on a 401.
- **Serialized across processes** by an exclusive file lock on `<data>/chatgpt/refresh.lock`.
  Several ternly sessions share one record, and a rotating token would otherwise be reused. After
  taking the lock, ternly re-reads the record; another process may already have refreshed.
- **Replaced as one unit:** the access token, expiry, scopes and new refresh token, written
  atomically.
- **Fatal errors** (`invalid_grant`, `invalid_refresh_token`, `token_expired`,
  `refresh_token_expired`, `refresh_token_invalidated`, `refresh_token_reused`): clear the tokens,
  keep the client ID and host ID, and the connection says "sign in again: ternly --chatgpt-login".
- **Network errors and 5xx never erase credentials.** ternly retries with backoff and marks the
  connection unreachable meanwhile.

### 7. Revocation and logout
- **`--chatgpt-logout` / `/chatgpt logout`:**
  - POSTs the refresh token to the discovered `revocation_endpoint` (an empty 200 is success),
    retrying with backoff on network errors and 5xx;
  - then deletes the tokens and keeps the client index and host ID.
  - If revocation can't be confirmed, the tokens are still deleted locally, and ternly says so and
    links to ChatGPT Settings to disconnect the app.
- **A disconnect in ChatGPT Settings** shows up as a fatal refresh error or a 401/403. The tokens
  are cleared as above.

### 8. Redaction
- **Every token value goes into the session redactor** when loaded and after every refresh. That
  covers model context, tool output, logs, crash reports and the event log (ADR 001, 012).
- **Errors are ternly's wording by error code** (the `subscription_sharing_*` codes and the OAuth
  errors), never a response body. This matches Phase B's connection rule (ADR 022).
- **A test seeds known token values** and checks none appears in the snapshot (ADR 021
  `TestSnapshotRedaction`), the logs, the event log or `/doctor`.

### 9. Routing and the UI contract
- **A ChatGPT-plan connection** appears in discovery when a credential with the plan scope exists.
- **Quota:**
  - OpenAI documents no usage endpoint for this flow. Usage reads "unknown (see
    chatgpt.com/settings/usage)".
  - The 429 `subscription_sharing_usage_limit_exceeded` is a quota hit, so routing fails over
    (ADR 018). The `subscription_sharing_usage_unavailable` and `subscription_sharing_user_unavailable`
    503s are retried.
  - There's no silent fallback to another billing path. ternly reports the error and the next
    model is chosen by the router as usual.
- **Contract change needed:** the connection's `Kind` would be a new value, `"sign-in"`. ADR 021
  lists `daemon`, `cli-bridge` and `api-key`. A new kind is a contract change, so implementing this
  needs an ADR 021 amendment first.
- **Trust (ADR 020) and the permission policy apply unchanged** to models reached this way.

## Threat model (to add when implemented)
- **New asset:** a rolling 30-day refresh token that spends the owner's ChatGPT plan.
- **Theft paths and controls:**

  | Theft path | Control |
  |---|---|
  | a model reading the file | the data directory is masked from the sandbox; there's no tool to read it |
  | logs or a model's context | redaction (§8) |
  | URLs | never used (§5) |
  | another local process on the callback | PKCE and state (§5) |
  | a phishing authorize URL | fixed in code (§5) |
  | prompt injection starting a sign-in | sign-in is owner-only (§1) |
  | a copied data directory | the host-ID check (§3) |

- **Reuse and races:** serialized, atomic refresh (§6). A reused rotating token is treated as
  fatal, not retried.
- **Out of scope, as before:** a local attacker with the owner's privileges. That attacker can read
  the keyring through its own CLI, or the 0600 file. The response to a compromise is to disconnect
  in ChatGPT Settings (§7).

## Tests the implementation must have (each guard broken once to prove it)
- **A fake OpenAI** (authorize, token, JWKS, discovery and revocation) for the full flow:
  - registration with `dynamic_agent_client`, then reuse of the issued ID;
  - a `state` mismatch, a different `client_id` and `access_denied`, each rejected;
  - the plan scope not granted.
- **Flag off:** zero network calls and zero credential reads (a sentinel, as in ADR 024).
- **Refresh:**
  - two processes refresh at once and exactly one token exchange happens;
  - `refresh_token_reused` clears the tokens but keeps the client ID;
  - a 5xx keeps the credentials.
- **Revocation:** success, then a failure that still deletes locally and says so.
- **Route allowlist:** the token appears on no request but `POST /v1/responses`.
- **Redaction:** seeded token values appear nowhere visible.
- **Storage:** the file is 0600 and written atomically, links are refused, and the keyring test is
  opt-in (`TERNLY_KEYRING_TEST=1`, as for MCP).

## Open items
- **The preview-limitations page** couldn't be fetched. Read it before implementing: eligible
  models and plans, rate limits, anything prohibited.
- **The UI/UX branding guidelines** ("Continue with ChatGPT") apply to the TUI's sign-in prompt
  (Phase F).
- **The owner's first sign-in is the registration.** It happens only when the owner runs
  `ternly --chatgpt-login` with the flag on.
