# 08. External integrations

English | [日本語](08-integrations.ja.md)

Audience: someone adding an external provider or a CLI agent
Source of truth: the code (this is a map of the methods and the intent)
Updated: 2026-07

Everything about talking to an outside provider is collected here. **Two patterns
recur**, and knowing which one you are in decides most of the design:

- **(a) No callback required** — a device flow, or pasting a code back. It works
  regardless of what sits at the edge.
- **(b) The CP owns a callback** — which means the CP needs a reachable public URL.

**When considering a new provider, the house rule is to look for (a) first.**

## 8.1 The integrations

| Party | Purpose | Method | Callback | Credential stored |
|---|---|---|---|---|
| Google | L1 Console login | OAuth authorisation code, natively in the CP | CP | a signed cookie — **no credential is stored** |
| GitHub | git auth | a pasted token, or **a device flow run by the CP using the tenant's app** | none | the encrypted store |
| Bitbucket | git auth | a pasted email + token, or **an authorisation code flow with a CP-owned callback, using the tenant's app** | CP | the encrypted store (refresh goes through a dedicated helper) |
| Internal git | git hosting | a per-membership HMAC token | — | **not stored** — derived each time ([91](91-internal-git.md)) |
| Anthropic | claude auth (L2) | its own sign-in, with a code pasted back | none | the CLI's own credentials file |
| OpenAI | codex auth | an API key, or a ChatGPT device flow | none | the CLI's own file |
| LLM providers | opencode auth | an environment key | none | the encrypted store |
| External Claude clients | MCP | a bearer PAT | — | only a hash, in the database ([06](06-data.md)) |
| AWS | ECS runtime 🚧, SSM login | SDK, SSO device flow | none | the short-lived credentials are cached **inside the container; the CP never sees them** |
| Caddy / Funnel | ingress and TLS | infrastructure, outside the code | — | — ([09 §9.3](09-deploy.md)) |

The design principle for connections: **a secret passes through the CP but is never
held or interpreted by it** ([07 §7.6](07-security.md)). Connection state is exposed in
one place, and a provider API called only for display is hit **once per connection and
cached** — never on the polling loop.

## 8.2 Google (L1)

Implemented natively in the CP. The flow, the allowlist and the gate's defences are
[07 §7.3](07-security.md). What matters here: the client id and secret, the public base
URL and the cookie secret, and **a redirect URI registered at the provider that matches
exactly**.

## 8.3 GitHub

- **Pasted token**: supplied through the credential helper.
- **Device flow** (the primary path): only the client id is needed — no secret — and
  **the app must have device flow enabled**. The user approves a code in their browser
  and the CP polls. Needing no callback, **it works behind any edge**.
- Listing remote repositories and branches uses GraphQL.

### Making `gh` work without a separate login

`gh` does not read git's credential helper; it looks only at its own environment
variables or config. Left alone, a user with a token already stored in Connections
would still have to run `gh auth login`.

To avoid that, the image replaces `gh` with a **thin wrapper** and moves the real binary
aside. On every call the wrapper fetches the token **from the same helper git uses** and
injects it before exec'ing the real `gh`. Everyone gets a working `gh` with no extra
login, **at the same freshness as git**, and it self-heals across rotation because it
fetches each time.

**Limits worth knowing:**

- **Scope**: the token carries the scope the device flow requested. Most of `gh` works;
  organisation-level calls may fail for want of a broader scope.
- **GitHub Enterprise is not covered** — the wrapper injects a github.com token only.
- **An explicit token wins**: if one is already in the environment, the wrapper does not
  override it.
- **Cost**: one helper invocation per `gh` call, comparable to a git push or fetch.
- **Home shadowing**: a real `gh` in the user's own `~/.local/bin` would come first on
  `PATH` and hide the wrapper, so the entrypoint removes a non-symlink there at start.

## 8.4 Bitbucket

- **Pasted**: email plus an API token.
- **OAuth**: the only CP-owned callback. The consumer's key and secret are read from
  **the tenant's row** ([decisions/0052](../decisions/0052-tenant-git-oauth.md)). The
  browser's own CP session carries the callback through the gate, so **no exemption is
  needed**.
  ★ **The tenant id travels in the state.** The callback is a plain redirect from the
  provider and carries no tenant header, so resolving it any other way could exchange
  the code against **another tenant's app**.
- **Refresh**: access tokens expire, so the git credential helper renews with the stored
  refresh token automatically.
- Listing remotes aggregates the workspaces' repositories.

### 8.4.1 The OAuth app belongs to **the tenant**

The apps are rows in a table, registered by a tenant administrator from the Console.
**The environment is not read**: the GitHub variable is now for *sign-in* only, and the
Bitbucket ones are referenced from nowhere.

- **The CP runs GitHub's device flow.** The agent used to, using a client id from the
  container's environment — but **that environment is fixed at container start, and
  there are four runtime implementations**, so making it per-tenant would have required
  restarting everybody's workspace for a change to land. The paths are unchanged, and
  the resulting token is handed to the agent through the same entry point a pasted token
  uses.
- **Whether to show the button is answered by a CP-native endpoint.** The connections
  endpoint is a proxy to the agent and returns an error while the workspace is stopped,
  so it cannot decide that.
- **The CP runs Bitbucket's refresh too.** It used to hand the key and secret to the
  agent, which meant **the tenant's client secret was copied into every member's
  encrypted store**. Now the agent sends the refresh token and the CP adds the secret.
  ★ **The refresh token does not move** — it stays in the workspace and the CP does not
  store it, preserving "the CP passes secrets through but does not hold them". The old
  copies are destroyed **once the bridge has succeeded once**, and kept until then as a
  fallback.
  ★ The bridge's coordinates live in the encrypted store, **not in the environment**:
  the credential helper is a separate process started by git, whose environment cannot
  be guaranteed.

## 8.5 Claude authentication and onboarding

**The method**: the CLI's own subscription sign-in. The agent drives a PTY, extracts the
authorise URL, the Console shows it, the user approves in their own browser and pastes
the code back, and **the CLI itself writes its credentials file**. Status and disconnect
use the CLI's own commands. Doing it by hand in a terminal still works.

- **The foundation, established by measurement**: the subscription flow's redirect URI
  is a **hosted code-display page — it does not depend on a localhost callback at all**,
  so it works headless and remote unconditionally.
- **"It shows the login method chooser" is an onboarding problem, not an authentication
  one.** Even when the CLI reports being logged in, a missing onboarding flag makes the
  interactive TUI re-run its wizard, whose first step is choosing a login method — so it
  *looks* unauthenticated. The fix is to seed the trust and onboarding flags at every
  session start; **skipping permissions does not skip those**.
  ⚠️ With a custom config directory set, **the config file is read from there too** —
  writing the one in the home has no effect.
- **Lessons, kept here so they are not repeated:**
  1. Injecting a setup token by environment variable works **headless only — the
     interactive TUI does not read it**.
  2. A synthesised credentials file with no refresh token is **rejected** by the
     interactive TUI.
  3. Using the API-key environment variable authenticates but is billed as API usage,
     which can disable subscription features.
  - **The judgement lesson**: neither the status command nor the banner proves
    authentication works. **Only a real prompt and a real answer do.** And **auth and
    onboarding are different things**
    ([decisions/0002](../decisions/0002-claude-auth-onboarding.md)).

## 8.6 codex and opencode

- **codex**: injecting the credential by environment does not work, so both paths make
  the CLI write its own auth file: an API key over stdin, or a ChatGPT device flow
  driven through a PTY, scraping the verification URL and one-time code. **The CLI polls
  the provider itself, so no callback is needed.** ⚠️ Device-code login must be enabled
  in the ChatGPT organisation settings.
- **opencode**: the provider key is stored in the encrypted store under an environment
  name and prefixed onto the command at session start, so **no plaintext auth file is
  ever created** ([04 §4.3](04-agent.md)).

## 8.7 MCP — the outward contract

| Surface | Who connects | Auth | Scope |
|---|---|---|---|
| The CP's endpoint (Streamable HTTP) | an external Claude client | bearer PAT, exempt from the login gate | member tools (driving your own remote sessions) plus admin read/write, **with the role resolved live** |
| The agent's stdio server, assistant side | the in-container assistant chat | none needed — same container, loopback | read-only by default; a flag advertises the fleet operations |
| The agent's stdio server, session side | any CLI that can materialise the built-in server | none needed | **only the report tool and the Chromium attach tools.** Every other fleet tool is refused, both in advertisement and on call |

The endpoint exists only when explicitly enabled. The decision is
[decisions/0006](../decisions/0006-mcp-unified.md).

## 8.8 AWS 🚧

- **The ECS runtime** (implemented, no production mileage): task definition
  registration, service upsert, EFS access points, secret injection. The SDK calls sit
  behind interfaces so they can be tested ([09 §9.5](09-deploy.md)).
- **SSM login** is two layers, a profile and a host ([06 §6.2](06-data.md)). The session
  signs in inside the container and starts the SSM session there. **No AWS secret is
  stored in, or reaches, the CP.**
- A KMS custodian is 📋 — the seam only ([07 §7.6](07-security.md)).
