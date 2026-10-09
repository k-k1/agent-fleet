# 0107. Google Cloud profiles: Settings defines them, `af-gcloud-exec` runs with one, and the Console finishes the gcloud login

English | [日本語](0107-google-cloud-profiles.ja.md)

- Status: **proposed** (2026-10-02). Nothing is built yet. The gcloud behaviour below was measured on
  Google Cloud SDK 587.0.0 in a workspace, with synthetic credentials and local mock servers where a
  real login or API would be needed; what was not measured says so.
- Tracking: #1092 (Tier 2, "a gcloud counterpart of the user's own cloud features")
- Follow-ups: #1487 (Workforce Identity Federation, e.g. Microsoft Entra ID), #1488 (refreshing the
  token for commands that outlive it)
- Related: [0102](0102-aws-login-through-the-console.md) (the AWS login through the Console, whose
  request and attempt machinery this reuses) / [0106](0106-kubernetes-runtime.md) (the metadata server
  blocked on GCE and GKE) / [0104](0104-long-lived-member-workspace.md) (why `~/.local` and the home
  outlive a stop)

## Context

A member can define AWS profiles in Settings and run any command as one of them with
`af-aws-exec --profile <name> --account <id> -- <command>`. The profile reaches the workspace through a
per-member bridge, the wrapper keeps the workspace's own cloud identity out of the command, and when the
login is missing the Console finishes it ([0102](0102-aws-login-through-the-console.md)). Nothing of the
kind exists for Google Cloud: no gcloud in the image, no profile, no wrapper, and the policy text tells
agents nothing about Google credentials. The user who asked for Google Cloud support (ADR 0106) runs
GKE; operating their projects from a session today means installing gcloud by hand and pasting a login
into a terminal.

### What carries over from AWS

- **The bridge.** A per-membership HMAC token, injected into the workspace, verified on each pull with a
  live membership lookup, served on the workspace-only listener's allowlist (`aws_profiles_bridge.go`,
  `workspace_listener.go`); names that collide after sanitising are exported for neither row.
- **The Agent's pull loop.** At boot and every five minutes, under a lock, with a cache bound to the
  token for when the CP is unreachable (`internal/awsx/profiles.go`).
- **The skeleton of 0102's Console login**: a request filed by the wrapper, an outbox notice whose
  payload is only an id, a sticky toast, an attempt started by a press whose id only the pressing tab
  holds, the CP relay with an audit record, the wait-then-exit-3 contract — and 0102's threat model with
  it (below, decision 3).
- **The wrapper's shape.** `syscall.Exec` into the command, a private state directory, `--list`, exit
  codes 2 (usage), 3 (login needed and not started), 1 (refused).

What does **not** carry over is everything that reads AWS's credential store: the SSO cache file per
session, its expiry and token hash, the "resolved" test of 0102, the expiry warning. gcloud keeps
credentials in SQLite keyed by account, not by profile; a Google Cloud backend has to answer those
questions its own way (decision 3).

### What gcloud does differently (measured)

- **The login runs the other way.** `gcloud auth login --no-launch-browser` prints an
  `https://accounts.google.com/o/oauth2/auth?…` URL whose redirect is
  `https://sdk.cloud.google.com/authcode.html` and waits on stdin for "the verification code provided in
  your browser". The member signs in in their browser and **pastes a code back**. The URL carries PKCE
  (`code_challenge_method=S256`), so a code is redeemable only by the gcloud process whose URL produced
  it. The URL carries no `login_hint`.
- **`gcloud auth login <account>` enforces the account.** The SDK compares the signed-in email with the
  argument and refuses (`WrongAccountError`) **before** storing anything. With a valid stored credential
  for that account it prints no URL at all and succeeds at once ("Re-using locally stored credentials").
- **The store is per account.** Credentials live in `credentials.db` and `access_tokens.db` under the
  config root (`CLOUDSDK_CONFIG`), keyed by account; a named configuration
  (`configurations/config_<name>`) holds only properties, and a login writes `core/account` into the
  configuration it ran with.
- **Configuration names** must start with a lowercase letter and contain only `a-z`, `0-9` and `-`:
  `af-Prod`, `af-prod_app`, `af-prod.app` are refused.
- **An access-token override beats everything.** With `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` set, gcloud uses
  that token whatever the configuration's account says — for the caller of the wrapper as much as for
  its child.
- **`print-access-token` returns the cached token** while it is valid: its remaining life is whatever is
  left of it, not an hour from now.
- **Application Default Credentials are found outside gcloud's config root.** Google's Go libraries
  (`golang.org/x/oauth2/google`, `cloud.google.com/go/auth`) look for
  `$HOME/.config/gcloud/application_default_credentials.json` regardless of `CLOUDSDK_CONFIG`; only when
  that is absent too do they fall back to the metadata server. Python's `google.auth` does honour
  `CLOUDSDK_CONFIG`.
- **Size.** The SDK unpacks to 486 MB (88 MB compressed).

## Decisions

### 1. A Google Cloud profile is a member's Settings row; the Agent keeps its own gcloud store

Settings gains a **Google Cloud** section beside AWS, member-scoped like it. A profile has:

| Field | Required | Meaning |
|---|---|---|
| label | yes | the display name |
| login method | yes | `google` (a Google account). The only value in the first version; `workforce` is #1487 |
| project | yes | the default resource project the command is pointed at |
| quota project | no | the project billed for API quota with a user token; defaults to the project |
| account | no | the Google account to log in as; when set, gcloud refuses any other |
| region, zone | no | `compute/region`, `compute/zone` |
| impersonate service account | no | the service account the command acts as, through the logged-in account — the practical counterpart of an AWS role |

**The name.** The profile's name is derived from the label by lowercasing and replacing every run of
characters outside `a-z0-9` with `-` (leading and trailing `-` dropped, a leading digit prefixed with
`p`), and is shown next to the label. A label that normalises to nothing — `本番`, `開発`, `---` — gets
`p-` and a short stable hash of the row's id instead, so a Japanese label still yields a usable name.
Labels whose names collide (`Prod` and `prod`, `prod_app` and `prod.app`) are exported for neither, as
AWS does, and Settings and `--list` say why. Settings, the bridge and `--list` share one implementation
of the normalisation, the empty-name rule and the collision rule.

No secret is stored in the CP. Service-account JSON keys are not accepted anywhere: they are long-lived
secrets, many organisations forbid them, and impersonation covers the need.

The profiles reach the workspace over `GET /internal/gcp-profiles` with its own token
(`AF_GCP_PROFILES_TOKEN`) on the workspace-only listener's allowlist. Settings export and import carry
the rows, add-only, as they carry AWS profiles.

**The Agent keeps a gcloud config root of its own** under its state directory, not the member's
`~/.config/gcloud`. The member's own gcloud — their logins, their active configuration, their
application default credentials — is never read or changed by a profile, so there is no file the two
could fight over, no ownership to infer, and no pull that switches the member's terminal to another
project. In that root each profile becomes a named configuration `af-<name>`, created with
`--no-activate`. The Agent owns the root outright: a configuration whose profile is gone is removed.

Inside a configuration, **Settings owns** the project, quota project, region, zone and impersonation;
**the login owns** `core/account` when the profile names no account. A sync rewrites the first set and
keeps the second. When the profile's account, login method or name changes, or the profile is
recreated, its selection is reset and its pending requests are dropped: a login-owned account is
cleared, so the next run asks which account to use. That is not a fresh authentication — the store is
per account, so a configuration that names an account another profile is already logged in as can use
that credential at once.

**Settings for the Agent's own gcloud runs**, kept apart from the profile properties above and applied
after the caller's variables are removed (decision 2), so nothing a caller sets can undo them:

- `CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true`. gcloud otherwise writes a log file per run under the config
  root at DEBUG level — including what `print-access-token` printed, the login URL and, inside the code
  exchange, the request and response bodies (measured: the token appears in the log; with the setting,
  no log file is written).
- `CLOUDSDK_CORE_CHECK_GCE_METADATA=false`, and no `GCE_METADATA_*` variable from the caller. Otherwise a
  configuration with no account selected falls back to the VM's service account on GCE (measured against
  a metadata mock: `print-access-token` returned the VM's token for a configuration that had never been
  logged in). This is a gcloud property, not a switch for Google's libraries.

### 2. `af-gcloud-exec --profile <name> --project <id> -- <command>` runs a command with one profile's token

- **`--project` must equal the profile's project.** On AWS, `--account` checks which account the
  credentials belong to. A Google user token is not bound to a project, so this is weaker and the
  usage text says so: it checks that the caller and the profile agree on where the command is pointed by
  default. A command's own `--project`, or a project written in a Terraform configuration, still wins.
- **A token is minted only for a selected account.** The configuration must have an account — from
  Settings, or from a completed login — whose credential is a user credential in the Agent's store;
  otherwise the wrapper files a login request instead of minting. The VM's or node's identity is never
  a fallback.
- **The token is minted in a clean environment.** The wrapper runs
  `gcloud auth print-access-token --configuration af-<name>` (with `--impersonate-service-account` when
  the profile sets it) against the Agent's config root, with every `CLOUDSDK_*`, `GOOGLE_*` and
  `GCLOUD_*` variable of the caller removed — an inherited `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` would
  otherwise replace the profile's identity before the profile is consulted. The configuration it reads
  holds only the properties of decision 1; any other property found there is removed by the next sync.
  It prints to stderr the account, the impersonated principal if any, and the token's remaining
  minutes — never the token.
- **The command receives the token and nothing else of the member's.** Its environment is the caller's
  with every `CLOUDSDK_*`, `GOOGLE_*` and `GCLOUD_*` variable removed, then:
  - `CLOUDSDK_CONFIG` → an empty private directory; `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` → the token file;
    `CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true`, so the child's gcloud and the GKE plugin's
    `config-helper` do not copy the token into a log by default;
  - `GOOGLE_OAUTH_ACCESS_TOKEN` → the token (Terraform's Google provider);
  - `CLOUDSDK_CORE_PROJECT`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_PROJECT` → the project;
  - `CLOUDSDK_BILLING_QUOTA_PROJECT`, `GOOGLE_BILLING_PROJECT` and `USER_PROJECT_OVERRIDE=true` → the
    quota project, because a user token from gcloud's OAuth client needs one for some APIs (the
    principal needs `serviceusage.services.use` on it);
  - region and zone in their `CLOUDSDK_COMPUTE_*` and `GOOGLE_*` forms;
  - `GOOGLE_APPLICATION_CREDENTIALS` → a path in the private directory that holds no credentials, so a
    client library that ignores the token variables **stops at that path** instead of finding the
    member's well-known ADC file or reaching for the metadata server. That this fails closed in Go,
    Python and Node's libraries is measured in phase 1 (open question 1).
- **The token's life is what remained when it was minted.** When less than ten minutes remain, the
  wrapper makes gcloud mint a fresh one before starting the command — the Agent owns the store, so it can
  drop that account's cached access token; how exactly is measured in phase 1 — and refuses to start if
  it still cannot. It does not refresh during the command. A command that outlives its token fails — a
  long `terraform apply`, a `kubectl` watch. Refreshing is #1488.
- **GKE is in scope from the start.** `install-gcloud` installs `gke-gcloud-auth-plugin` with the core,
  and `kubectl` against a GKE cluster through the wrapper is part of phase 1's completion test: the plugin
  calls `gcloud config config-helper`, which takes the token from the file (measured; the reported expiry
  is null, so how kubectl behaves at the token's end is measured too).
- **Exit codes and `--list` follow `af-aws-exec`.**

What the wrapper does not guarantee: a command runs as the member's uid and can read the member's home,
including the member's own gcloud directory. The wrapper keeps the standard credential search from
finding anything but the token it hands over; it does not make the home unreadable. On a host where the
metadata server is reachable and nothing stops it — `native` on a GCE VM, which ADR 0106 does not cover —
a library that bypasses `GOOGLE_APPLICATION_CREDENTIALS` could still reach it; the notes say so. On such
a host gcloud's login also asks first whether to use a personal account on a GCE VM; whether the Console
login can answer that, or `native` on GCE is left to the terminal login, is settled in phase 2.

### 3. The Console finishes the login; the member pastes the code into the attempt they started

Without a terminal, `af-gcloud-exec` files a login request as `af-aws-exec` does (0102 decisions 1, 2, 5
and 6, one request per profile). What changes is the start, the submit and the backend:

1. The member presses **Log in** in the toast or in Settings. The Agent re-syncs the profiles first and
   refuses a profile that is not exported or is shadowed (0102's amended Settings-row rule). It then
   starts `gcloud auth login [<account>] --no-launch-browser --configuration af-<name>` in the Agent's
   config root, in a clean environment (decision 2), in its own process group, one attempt per profile.
   When the profile names an account it is always passed, and gcloud refuses a different sign-in before
   storing it; the Agent never revokes anything to undo a login.
2. If gcloud succeeds at once with a stored credential, the attempt ends as done without a URL — except
   for a request filed because the credential was rejected (step 5), which starts with `--force` so the
   member really re-authenticates instead of reusing the credential that failed.
   Otherwise the Agent parses the URL once and shows it only if the scheme is `https`, the host is
   exactly `accounts.google.com`, there is exactly one `redirect_uri` and it is exactly
   `https://sdk.cloud.google.com/authcode.html`, and there is no userinfo, port or fragment. Anything
   else ends the attempt.
3. The start route answers the pressing tab with an attempt id that cannot be guessed. The Console shows
   the URL and a single **verification code** field only for an attempt it holds from its own press — it
   never adopts one from a notice or a list.
4. The code is posted, in a bounded request body only, to that attempt's submit route, relayed and
   audited by the CP. The Agent writes it to that gcloud's stdin once, under the attempt's lock; a submit
   to an attempt that is not waiting, was cancelled or replaced, or has already received a code is
   refused. Neither the CP, the Agent nor the notice records the code or the URL: audit and logs carry
   the profile, the attempt and whether the call came through the relay.
5. **Whether a request is resolved** is decided by the Google Cloud backend, not by 0102's file
   snapshot. For a request filed because no credential was found, it is resolved when
   `print-access-token` succeeds for its configuration in the clean environment. That only shows a local
   credential exists: gcloud returns a cached token without asking Google. So a request filed because an
   API **rejected** the token is resolved only by a login completed after it, never by the same cached
   credential succeeding again, and Settings offers **Log in again** (`--force`) for when a member knows
   their login was revoked. Until something rejects the token, a revocation on Google's side is not
   detected locally. Errors are classified: no or revoked credentials, `invalid_grant` and reauthentication
   prompts mean "log in"; permission denied (including on impersonation), a disabled API or a network
   failure do not, and end the request with the reason instead of asking for a login that cannot help.
   A request whose profile changed in Settings since it was filed is dropped.

**What this protects, stated as 0102 states it.** Any process in the workspace holds `AGENT_TOKEN` and
can call the Agent's start and submit routes; the Agent cannot tell a member's press from such a call.
What the design guarantees is narrower: the Console shows a URL and accepts a code only for an attempt
id it received in answer to its own press, and the URL shown was checked against Google's. PKCE ties a
code to the gcloud that printed the URL it came from, so a code is useful only to that attempt — but it
does not prove who started the attempt. The guide's rule carries over: **paste a code only into a login
you started yourself**. A hostile agent running as the member can read the Agent's credential store and
replace the gcloud binary; that is outside what any of this can defend, as in 0102.

### 4. gcloud is installed on demand, pinned, into `~/.local`

`workspace-agent install-gcloud` installs one pinned version of the SDK with the core and
`gke-gcloud-auth-plugin` components, checked against its sha256, into `~/.local`, which outlives a stop
and a Recreate. `af-gcloud-exec` runs it on first use. The version is a Dockerfile `ARG` recorded in
`versions.json` and read back by `readBuildPins`, as `AWSCLI_VERSION` is, with the archive name and
sha256 for each architecture; the archive names use `x86_64` and `arm`, which the installer maps from
`amd64` and `arm64`. Bumping it is a manual pin change, as for the AWS CLI. The Toolchain tab's table of
effective tool versions lists it.

It is not baked into the image: 486 MB would land on every cold image pull — Fargate pulls cold at every
start, and GKE pulls per node — for a tool a fraction of members use.

### 5. The rest of the AWS surface gets its counterpart, and the notes ship with the wrapper

- **Phase 1, with the wrapper:** `workspace/notes/gcp.md` (the `af-gcp` skill) and the policy bullet in
  `workspace/workspace-notes.md` — the default credential chain is not the member; anything about their
  projects goes through `af-gcloud-exec`; never run `gcloud auth login` or change the Agent's store
  yourself; exit 3 means the member has to log in; which tools the token reaches; never print a token.
- **Phase 2, with the Console login:** the paste rule in the notes and guide, and the relay's audit.
- **Phase 3:** the workspace bar's Google Cloud badge beside the AWS one, and the guide's integrations
  and Settings pages in English and Japanese.

### Out of scope

- **Workforce Identity Federation** — external principals of a workforce pool, signing in through an IdP
  such as Microsoft Entra ID with `gcloud auth login --login-config`. Google Workspace or Cloud Identity
  accounts federated by SAML are ordinary Google accounts and are covered. #1487; the login-method field
  exists so that it lands without reshaping profiles.
- **Refreshing the token** for long commands: #1488.
- **A session kind** like SSM's (`gcloud compute ssh --tunnel-through-iap`), **Google Cloud MCP
  servers**, and **service-account keys** (decision 1).

## Rejected

- **Sharing the member's `~/.config/gcloud`.** Configuration files have no boundary like the AWS
  managed block, so the Agent could not tell its files from the member's, a pull could switch the
  member's active configuration, and every account the member is logged in as would be one property away
  from any profile.
- **Storing refresh tokens in the CP** and minting access tokens there: the CP would become a store of
  members' Google credentials, which the AWS design deliberately avoided.
- **ADC as the shared store** (`--update-adc`): one file for every profile, so "which identity is this"
  would depend on whichever login ran last.
- **Revoking a credential after a wrong-account login:** gcloud already refuses before storing, and the
  store is per account, so a revoke could undo a login another profile relies on.
- **Baking gcloud into the image:** decision 4.

## Consequences

- A second cloud's worth of Settings, bridge, wrapper, login and notes. The request and attempt
  lifecycle, the bridge token and the wrapper skeleton are extracted from `awsx` into a provider-neutral
  package once; the credential backend (what "logged in" and "resolved" mean, which errors mean "log in")
  is per provider.
- The Console's login modal grows an input field, and the relay carries a secret in a request body it
  must not log.
- The member's terminal `gcloud` and a profile are separate worlds: logging in in one does not log in
  the other. `af-gcloud-exec` is the way to use a profile by hand, too.
- Commands that outlive their token fail until #1488.

## Open questions (decide after measuring)

1. Whether `GOOGLE_APPLICATION_CREDENTIALS` pointing at an empty path makes Go, Python and Node client
   libraries fail closed rather than continue to the well-known file or the metadata server.
2. Which tools honour the token variables beyond gcloud, Terraform and the GKE plugin: `gcloud storage`,
   `bq`, client libraries.
3. How an organisation's reauthentication policy (session length for Google Cloud) surfaces, and whether
   the Agent can warn before it. AWS warns only for an SSO login that cannot renew, whose end the cache
   records; it does not guess when a renewable login's portal session ends. Google's access-token life and
   an organisation's reauthentication deadline are separate and both need measuring.
4. Whether `install-gcloud` can leave out `bq` and the bundled extras to shrink below 486 MB.

## Phases

| Phase | What | Done when |
|---|---|---|
| 1 | Decisions 1, 2, 4 and the phase-1 notes; the login from a terminal | a member runs `gcloud`, `terraform plan` (with an API that needs a quota project) and `kubectl` against a GKE cluster through the wrapper; a project mismatch is refused; open question 1 is answered |
| 2 | Decision 3 | an agent's `af-gcloud-exec` is finished from the Console without a terminal; a code submitted to any other attempt is refused; a synthetic code, token and URL appear in no CP, Agent or gcloud log |
| 3 | The badge, the guide, open questions 2–4 | the notes list the measured tools |

## Note — what `install-gcloud` installs, measured (2026-10-02)

Issue #1495. Nothing above is changed; this records the phase-1 installer's measurements for open
question 4, on 587.0.0 in a workspace.

- **Size.** The x86_64 archive is 84 MiB (arm: 51 MiB) and unpacks to 486 MB. After `install.sh` with
  `gke-gcloud-auth-plugin` the tree is **832 MB**: about 170 MB of it is `__pycache__` that `install.sh`
  compiles, 122 MB the bundled Python, 82 MB `gsutil` and 22 MB `bq`. Leaving any of them out is not
  attempted yet; the question stays open.
- **The plugin is pinned too.** It is not in the archive. With
  `CLOUDSDK_COMPONENT_MANAGER_FIXED_SDK_VERSION=<pin>`, `install.sh` reports "Installing components from
  version: 587.0.0", so the component comes from that version's snapshot (checked against its checksums)
  and not from whatever is current. The archive itself is checked against the sha256 in `versions.json`;
  Google publishes checksums only for the current release, so the pinned sums are computed from the
  downloaded archives.
- **`gcloud --version` is not harmless.** Run without `CLOUDSDK_CONFIG` it creates `~/.config/gcloud`,
  writes a log file there and records a metadata-server probe (`gce`). The installer therefore runs with a
  throwaway config root and the caller's `CLOUDSDK_*`, `GOOGLE_*`, `GCLOUD_*` and `GCE_METADATA_*`
  variables removed, and the Toolchain tab reads the SDK's `VERSION` file instead of running gcloud.
- **The tree is relocatable.** It is installed in a staging directory and renamed into place; gcloud and
  the plugin run from the new path and through the `~/.local/bin` links.

## Note — the child's credential-free path, the mint and the configurations, measured (2026-10-02)

Issue #1496. The decisions above are not changed. This note records the phase-1 Agent side's
measurements for open question 1 and decision 2's "how exactly is measured in phase 1", and two
implementation choices.

- **Open question 1: answered, fails closed.** Each library ran with a synthetic `authorized_user`
  ADC file in a scratch `$HOME/.config/gcloud` and a local metadata mock reached through
  `GCE_METADATA_HOST` / `GCE_METADATA_IP` / `GCE_METADATA_ROOT`. Versions: Go 1.26.8 with
  `golang.org/x/oauth2` 0.37.0 and `cloud.google.com/go/auth` 0.24.0, Python 3.13.5 with `google-auth`
  2.59.1, and Node 22.23.3 with `google-auth-library` 11.1.0. Two positive controls passed first: with
  no `GOOGLE_APPLICATION_CREDENTIALS`, all four found the ADC file; with no ADC file either, all four
  took the mock's token. Then `GOOGLE_APPLICATION_CREDENTIALS` pointed at nothing usable. Every one of
  the four code paths returned an error, sent no request to the mock and did not read the ADC file.
  "Nothing usable" was measured four ways:
  - a missing file in an empty private directory (the wrapper's choice);
  - an empty file;
  - a directory;
  - a missing file with `CLOUDSDK_CONFIG` pointing at an empty directory.

  The variable is what does it, not `CLOUDSDK_CONFIG`. With only `CLOUDSDK_CONFIG` set, Python and
  Node skip the home ADC file but go to the metadata server, and both Go libraries still load
  `$HOME/.config/gcloud/application_default_credentials.json`. Neither library mentions
  `CLOUDSDK_CONFIG`. Not measured: a token refresh against Google, and runs with gcloud on `PATH`.
- **The mint uses `gcloud config config-helper`, not `gcloud auth print-access-token`.** The full call is
  `gcloud config config-helper --configuration af-<name> --min-expiry 10m --format json(credential.access_token,credential.token_expiry)`,
  plus `--impersonate-service-account` when the profile sets one. It runs in the same clean environment
  against the Agent's root. Measured on 587.0.0 with a synthetic refresh token and a local token and
  `iamcredentials` mock:
  - `print-access-token` prints the token and no expiry, so the remaining minutes cannot be told from
    it. For an impersonated token nothing records an expiry at all: every impersonated call mints a
    new service-account token, and `access_tokens.db` holds only the user's.
  - `print-access-token` refreshes a cached user token only in its own window of about five minutes
    (the threshold lies between 240 s and 320 s). Tokens with 320, 400 and 600 s left came back
    unchanged, so the ten-minute rule needs something else.
  - `config-helper` returns the token and an RFC 3339 `token_expiry` in one call, for an impersonated
    token too. `--min-expiry 10m` refreshes a cached token with less than ten minutes left: tokens with
    320, 400 and 600 s left were refreshed, and one with 900 s left was kept. `--min-expiry` refuses
    values over 1h. `--force-auth-refresh` is the unconditional alternative.

  So "force a fresh mint" is `--min-expiry 10m`, with no write to gcloud's store. A token that still
  has under ten minutes left, or a missing or unparsable `token_expiry`, is refused. The JSON is read
  only in memory: the format projection leaves out `id_token`, and an error never quotes the output.
  The user approved this on 2026-10-02 (via the parent session).
- **The configurations are written as files.** The Agent writes `configurations/config_af-<name>`
  directly instead of running `gcloud config configurations create --no-activate` and
  `gcloud config set`. gcloud reads a hand-written file like its own (measured: `configurations list`
  and `config get`). `active_config` is never touched, which is what `--no-activate` is for. Writing
  files also means the five-minute poll needs no gcloud installed and pays for no gcloud starts, at
  about 1 s each. Which `core/account` the login owns is decided from a small record of the last sync
  (`gcloud/.agent-fleet-profiles.json`: id, login method, Settings account per name).
  A run mints and logs in only while that record and the file's properties are exactly the
  version of the profile it read from Settings; otherwise it refuses and asks for a rerun. The
  properties are compared, not the bytes: gcloud rewrites the file in its own layout when the login
  sets `core/account` (measured: the comment line goes and the keys move). The terminal login holds
  the root's lock from that check through the mint, so no sync or other run reads or rewrites the
  configuration while gcloud writes it. A Settings change made during the login is applied by the
  next sync, which resets the selection.
  When Google rejected the stored credential (`invalid_grant`, reauthentication), the terminal login
  runs with `--force`, as decision 3 has the Console login do. Without it, `gcloud auth login
  <account>` reuses a cached access token with more than about five minutes left
  (`ShouldUseCachedCredentials`, 587.0.0) and signs nobody in. A run whose login finished but still
  has no usable credential exits 1, not 3.
- **`GCE_METADATA_*` is removed for the child too**, beside `CLOUDSDK_*`, `GOOGLE_*` and `GCLOUD_*`:
  a caller's metadata host must not steer a library that does reach for the metadata server.

## Note — open questions 2–4: the tools the token reaches, reauthentication, a smaller install, measured (2026-10-02)

Issue #1498. The decisions above are not changed; one implementation choice (the installer's
`--no-compile-python`) follows from question 4. Measured on SDK 587.0.0 in a workspace, without a real
Google login: each command ran in the child environment decision 2 describes, with a synthetic token,
against a local mock that classified each request's `Authorization` header (the wrapper's token, none,
or another) and logged the `X-Goog-User-Project` header. A synthetic `authorized_user` ADC file whose
token endpoint was the mock sat in the scratch `$HOME/.config/gcloud`; no tool asked it for a token.
Sizes are `du` in MiB, as in the 2026-10-02 installer note.

- **Open question 2: which tools honour the token.**

  | Tool | Credential sent | Project / quota project |
  |---|---|---|
  | `gcloud storage ls`, `cat` | the wrapper's token | yes / `X-Goog-User-Project` |
  | `bq ls` (the SDK's `bin/bq`) | the wrapper's token | yes / `X-Goog-User-Project` |
  | `gsutil ls` | **none** — an unauthenticated request with gsutil's built-in API key | project yes / no quota |
  | Go `cloud.google.com/go/storage` 1.69.0, default credentials | none: `NewClient` fails at the empty ADC path, no request | — |
  | the same with `option.WithTokenSource` over `GOOGLE_OAUTH_ACCESS_TOKEN` | the wrapper's token | quota only with `GOOGLE_CLOUD_QUOTA_PROJECT` set |
  | Python `google-cloud-storage` 3.16.0 / `google-auth` 2.59.1, default | none: `DefaultCredentialsError`, no request | — |
  | the same with `google.oauth2.credentials.Credentials(token, quota_project_id=…)` | the wrapper's token | yes / yes |
  | Node `@google-cloud/storage` 8.2.0 (bundles `google-auth-library` 9.15.1), default | none: fails at the ADC path, no request | — |
  | the same with an `OAuth2Client` of that same library as `authClient` | the wrapper's token | yes / with `quotaProjectId` |
  | the same with an `OAuth2Client` of `google-auth-library` 11.1.0 | **none**, and no error | — |

  So the token reaches gcloud (`gcloud storage` included), `bq`, the GKE plugin and Terraform; it does
  not reach `gsutil`, and it reaches a client library only when the program passes it explicitly. Two
  findings change what the notes tell agents. `gsutil` ignores the token; in this clean environment,
  with no boto configuration, it fell back to anonymous, and a public bucket answers, so a read can look
  as if it ran as the profile. The child keeps `HOME`, `BOTO_CONFIG` and `BOTO_PATH`, and gsutil's
  bootstrap reads them, `/etc/boto.cfg` and `~/.boto` (SDK code read, not run with credentials there), so
  a member with boto credentials of their own would have gsutil act as that other identity — not
  measured. And a Node `OAuth2Client` from another major
  version of `google-auth-library` than the client library's own is accepted silently and sends nothing.
  `bq` and `gsutil` are not linked into `~/.local/bin` (the installer links only `gcloud` and the
  plugin), so neither is on `PATH`; the notes give `bq`'s path. The wrapper sets no
  `GOOGLE_CLOUD_QUOTA_PROJECT`, the variable Google's libraries read for a quota project; decision 2's
  list is unchanged here, and the notes tell a program to set it. Not measured: the real Google
  endpoints, Terraform again (phase 1's test), and libraries for other APIs than Cloud Storage.
- **Open question 3: reauthentication cannot be warned about; it surfaces as a login.** The mock's
  token endpoint answered a refresh with what Google sends when an organisation's session length has
  run out (`invalid_grant` with `error_subtype` `invalid_rapt`, and `rapt_required`). The wrapper's mint
  (`config-helper`, stdin not a terminal) then failed with "There was a problem refreshing your current
  auth tokens: Reauthentication failed. cannot prompt during non-interactive execution.", which
  `loginNeeded` classifies as "log in", so the run exits 3 and the terminal login runs with `--force`.
  With a terminal, gcloud printed "Reauthentication required." and asked the token endpoint again for a
  reauth-scoped token before starting its challenge; the mock refused that too, so the challenge itself
  was not reached. What gcloud stores: `credentials.db` holds the refresh token and client, with no time;
  `access_tokens.db` holds the access token, its expiry and a `rapt_token` column (empty here). Nothing
  records when the organisation's session ends, and its length is an Admin-console setting that a user
  token cannot read, so the Agent cannot warn before it. AWS does not guess a renewable login's end
  either. It also surfaces late: with the cached token 30 minutes from its end and the token endpoint
  set to answer `invalid_rapt`, the mint succeeded without asking Google. A login whose session has
  ended therefore keeps working until the cached token has less than ten minutes left, up to about 50
  minutes. Needs a real organisation with session control: the response Google really sends, whether the
  challenge (password or security key) appears in the `--no-launch-browser` login and can be answered
  there, whether a `--force` login satisfies the policy, and whether impersonated tokens are affected.
- **Open question 4: the `__pycache__` goes; `bq` and `gsutil` stay.** Four installs of the same archive
  in a scratch directory, with the installer's environment:

  | Install | Size | Files | install.sh time |
  |---|---|---|---|
  | as phase 1 (`install.sh` compiles every module) | 831 MiB | 50,939 | 74 s |
  | `--no-compile-python` | 511 MiB | 33,280 | 7–8 s |
  | `--no-compile-python`, then `gcloud components remove gsutil`, then every `__pycache__` deleted | 440 MiB | 29,948 (after one mint) | + 52 s for the removal |
  | the archive unpacked, nothing run | 485 MiB | 32,491 | — |

  The `__pycache__` is 320 MiB (18,443 `.pyc` files), not the 170 MB the installer note estimated.
  Without it Python writes the cache for the modules a run imports, so the tree grows only by what is
  used (the 440 MiB tree grew by 29 MiB over a mint, `gcloud storage`, `bq` and the plugin), and the first run of a command pays
  once: on this host, under a load average near 20, the first mint after the install took about 10 s
  and later ones 1–3 s, about what the compiled tree took on the same host (0.8–5 s). `gcloud`, `gcloud storage`, the mint, `bq` and the
  GKE plugin all ran from the uncompiled tree. So `install-gcloud` now passes `--no-compile-python`.
  The whole real installer ran into a scratch `HOME` in 37 s (download included), leaving 511 MiB and
  no `~/.config/gcloud`. A tree installed before keeps its cache until the next pin bump replaces it.

  Leaving out `gsutil` and `bq` is possible but not worth it. `gcloud components remove` works offline,
  but its post-processing recompiles the whole tree (511 → 749 MiB) and takes about 52 s, so it needs a
  sweep of every `__pycache__` afterwards. Removing the files by hand would depend on the component
  manager's layout. `gsutil` (55 MiB unpacked, 3,326 files) is not on `PATH` and does not use the
  token; the notes and the guide say so and point at `gcloud storage` instead. `bq` (12 MiB) works through the wrapper. What the
  wrapper needs is the core with the bundled Python (gcloud runs on it), `gcloud-crc32c` (used by
  `gcloud storage`) and `gke-gcloud-auth-plugin`, which asks the `gcloud` on `PATH` (`config config-helper`) for the token.

Follow-ups: #1517 (whether the child gets `GOOGLE_CLOUD_QUOTA_PROJECT`), #1518 (reauthentication with a real
organisation).

## Note — the Console login as built, and the Compute Engine prompt (2026-10-02)

Issue #1497. The decisions above are not changed. This note settles the question decision 2 left
to phase 2 and records how decision 3 was built. No real Google sign-in was run: gcloud's side was
read in SDK 587.0.0's source and played by a fake in the tests.

- **The "personal account on a Compute Engine VM?" prompt is not answered by the Console login;
  `native` on GCE is left to the terminal login.** gcloud asks it (`PromptContinue`, default yes)
  before it prints the URL whenever `c_gce.Metadata().connected` holds
  (`surface/auth/login.py`). That check is the GCE residency probe of `gce_cache.py`, which
  `CLOUDSDK_CORE_CHECK_GCE_METADATA=false` does not gate; the property gates only the credential
  fallback in `credentials/store.py`. Answering would be the Agent accepting, on the member's
  behalf, gcloud's warning that the credential may be visible to others on that VM, and the only
  line the Console ever writes to gcloud's stdin is the code. So any yes/no question before the URL
  ends the attempt, and the Console tells the member to log in with
  `af-gcloud-exec --profile … --project … --login -- true` in a terminal. Where ADR 0106 blocks the
  metadata server (ECS, GKE) the probe fails and the prompt does not come up; that is not measured
  on a live VM or node.
- **After the code, gcloud asks once more** ("overwrite existing credentials?", default yes) when a
  credential for the account is already stored. The Agent closes stdin after the code's line, so
  that question reads end-of-file and takes its default; nothing else is ever written.
- **The root's lock during a Console login.** The terminal login holds it for its whole run. A
  Console login waits on a person for minutes, and holding the lock that long would stall every
  `af-gcloud-exec` run of every profile. gcloud writes the configuration only before the URL (when
  a stored credential lets it finish at once) and after the code, so an attempt holds the lock from
  its start until the URL is out, and again from the code's submit until its process has exited and
  the login is recorded. The submit hands the lock to the attempt, and records that a code was
  exchanged, before the code is written, under one mutex the process's exit handler also takes:
  a gcloud that exits the moment it has the code finds both settled. The start and the submit check, under the lock, that the configuration is
  still the version of the profile the press read; a submit for a changed profile ends the attempt.
  An attempt that prints no URL within a minute ends, so a gcloud stuck before its URL cannot
  hold the lock for the attempt's whole fifteen minutes. A route waits for the lock at most ten
  seconds and otherwise answers "busy".
- **"Resolved" (step 5) as built.** The Agent records a mark per account in
  `gcloud/.agent-fleet-logins.json` for a login that exchanged a code — a Console attempt that
  took one, or a terminal login — and only after a token was minted from the user's credential
  in the clean environment (`config config-helper` without the profile's impersonation: no flag,
  and `CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT` set empty, which gcloud ranks above the
  configuration's property). That
  mint is step 5's "`print-access-token` succeeds", run once per login instead of on every sweep
  (a sweep runs on every list and poll, and a gcloud start costs about a second). A request
  records the profile's version (id, login method, Settings account — decision 1's reset
  triple), the selected account, whether a user credential is stored for it, and that mark. It
  is resolved only by a newer mark for an account that holds a user credential; a request whose
  profile version changed is dropped. A credential that appears in the store any other way, and
  a login gcloud ends at once on the stored credential (no URL, no code), settle nothing: the
  latter is done for the member but may be the very credential Google rejected, which gcloud
  reuses without asking Google. A request filed while a user credential was selected was filed
  because Google rejected it, and starts with `--force`. A Console login whose verification mint
  fails ends as failed.
- **What is logged and audited.** The Agent's log and the CP's audit name the profile and an
  attempt reference (the first 8 hex digits of the id's SHA-256), never the attempt id — whoever
  holds it can read the sign-in URL — and never the URL or the code. The Agent's access log
  replaces the id in an `…/attempts/<id>` path by the same reference and leaves out a login
  route's query. The audit row's detail says
  `via relay`. The code route's body is bounded to 4 KiB at the CP and at the Agent, and a code must
  be one line of the characters a Google code uses. The CP's error log replaces a gcp-login attempt
  id in a relayed path by its reference and drops the Agent URL from a transport error.

## Note — the child also gets `GOOGLE_CLOUD_QUOTA_PROJECT` (2026-10-03)

Issue #1517, decided by the user. Decision 2's list of what the command receives gains
`GOOGLE_CLOUD_QUOTA_PROJECT`, set to the same effective quota project as
`CLOUDSDK_BILLING_QUOTA_PROJECT` and `GOOGLE_BILLING_PROJECT` (the profile's quota project, or its
project when that is blank); the text of decision 2 above is left as written. The reason is the
2026-10-02 note on open question 2: Google's Go client libraries send `X-Goog-User-Project` beside an
explicit token only when this variable is set, and `cloud.google.com/go/storage` 1.69.0 refuses
`option.WithQuotaProject` beside a token source, so without it a Go program that takes the token from
`GOOGLE_OAUTH_ACCESS_TOKEN` called APIs with no quota project unless it exported the variable itself.
Python's explicit `Credentials` does not read it (it takes `quota_project_id=`), so the notes keep that
advice. A caller's own `GOOGLE_CLOUD_QUOTA_PROJECT` is still removed with every other `GOOGLE_*`
variable before the wrapper sets its value; the child-environment test covers both.

## Note — logging a profile out (2026-10-08)

Issue #1850, decided by the user. Settings > Google Cloud and the WS bar badge gain **Log out** on a
logged-in row, as AWS has (`POST /gcp-login/profiles/{name}/logout`, relayed and audited by the CP as
`gcp.logout`). Because the store is per account (decision 1), a logout signs the Agent's store out of the
account the profile selects, and every profile selecting that account with it; the confirmation names
them first. Under the root's lock the Agent ends the login attempts of those profiles, deletes the
account's rows from `credentials.db` and `access_tokens.db`, its `legacy_credentials/<account>` files
(they hold the refresh token too) and its login mark, and clears a login-owned `core/account`; an account
named in Settings stays, signed out all the same. Pending requests are left alone. The request names the
account the member confirmed, and the Agent refuses (`account_changed`) when the profile selects another
one by then: a login in between would otherwise sign out an account, and profiles, never shown.

**Nothing is revoked at Google.** `gcloud auth revoke` calls Google first and removes nothing locally when
that call fails (SDK 587.0.0, `store.Revoke`), and whether revoking one gcloud refresh token ends the
grant of gcloud's OAuth client for the user — the member's gcloud on other machines — is not measured.
That is different from the rejected revoke above, which was about undoing a wrong-account login. A token
a command already received stays valid until it expires.

## Note — a run at a member's terminal asks the Console too (2026-10-08)

Issue #1512, following the ADR 0102 note of this date. In decision 3, a run at a terminal now asks the Console
like an agent's run when it is inside a workspace, is at a terminal that is not an agent's, and has no `--login`
or `--no-login`: it files the request, waits up to ten minutes, and Ctrl-C ends the wait with exit 3 and leaves the
request. `--login` keeps the in-terminal sign-in (and is what the printed hint carries); outside a workspace, or
when the request cannot be filed, the terminal sign-in runs as before. The paste rule is unchanged: the code
field exists only in the Console window where the member pressed **Log in**.

## Note — the token is renewed while the command runs (2026-10-08)

Issue #1488. Decision 2's "It does not refresh during the command" no longer holds; the rest of the
decision stands. Follow-up for what this cannot cover: #1879.

- **The wrapper stays as the command's parent** instead of exec'ing into it, so that something can run
  beside the command. It forwards SIGTERM, SIGHUP, SIGUSR1 and SIGUSR2, and SIGINT and SIGQUIT only when,
  at the moment they arrive, it is not in the foreground group of a terminal (there the terminal already
  delivers them, a second SIGINT makes Terraform abort, and `fg`/`bg` move the group). The command shares
  the wrapper's process group and terminal. A command that stops itself (SIGSTOP) stops the wrapper too,
  and SIGCONT to the wrapper continues it. The wrapper exits with the command's status, or dies of the
  command's signal with the kernel's default action restored (Go's own default would turn SIGQUIT/SIGABRT
  into a stack dump and SIGPIPE/SIGUSR1 into an ordinary exit). The guarantee is narrow: if the wrapper
  dies of anything, SIGKILL included, the **direct child** is sent SIGTERM (`PR_SET_PDEATHSIG`), which
  it may ignore; the child's own children are not reached, and a killed wrapper does not remove the token
  file, which a later run sweeps. The renewer is a goroutine of the wrapper, so it cannot outlive it. The
  token file is deleted when the command ends normally.
- **What is renewed: the token file.** About eight minutes before the token ends the wrapper repeats the
  run's own mint (`gcloud config config-helper --min-expiry 10m` under the root's lock, same
  configuration, no `--lifetime`) and replaces the run directory's token file by rename (0600). A renewed
  token therefore lives no longer and reaches no further than the first, and a renewal that signs in as a
  different account than the run started with, or that lasts no longer than the token in use, is refused.
  Measured on SDK 587.0.0 in this workspace: `gcloud config config-helper` with
  `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` returns the file's current content on each start (a changed file gave
  the new value on the next process). The GKE auth plugin asks that same gcloud. So processes started
  after a renewal get the new token; a single gcloud process keeps the one it read (`store.py`
  `_LoadAccessTokenCredsFromFile` copies the file's content into an `AccessTokenCredentials` whose refresh
  is a no-op), and a `kubectl` watch depends on the plugin being run again (not measured). Not measured
  either: any of this against Google (no real login here).
- **What is not renewed: `GOOGLE_OAUTH_ACCESS_TOKEN`.** An environment variable cannot change in a running
  process, and Terraform's Google provider reads it once, so a `terraform apply` still fails when its first
  token ends. The option that would cover it (a per-run `authorized_user` credential whose `token_uri` is a
  loopback endpoint of the wrapper, with a per-run secret) is not built; a metadata-server shim was
  rejected as wider (anything on the loopback could ask it). Both are in #1879, with longer
  impersonated lifetimes (`--lifetime`).
- **A renewal that fails** prints one warning, tries again every 30 seconds until the token ends, and
  then stops the command (SIGTERM, SIGKILL after 30 seconds) with exit 3 when a login is needed and 1
  otherwise. The whole renewal, a hung gcloud and a Console wait included, is cut off at the token's end,
  and the login that the earlier tries met stays the reported reason. When the run could ask the Console at
  its start, it asks once more, waiting no longer than the token's remaining life, so a login finished
  meanwhile carries the command on; otherwise (`--login`, `--no-login`, no Console) the message carries the
  terminal command. Stopping the command at the token's end can end one that no longer needed the token; the
  alternative, leaving it running on a dead token, hides the cause.

## Note — a loopback `token_uri` for programs that read the token once: measured, not built (2026-10-09)

Issue #1879. The decisions above are not changed, and nothing is built. This note records the
measurement the issue asked for before any code, the trade-off, and the maintainer's decision (at the end).

Setup: a fake token endpoint on `127.0.0.1` that logs each request and returns `fake-access-N`, a
synthetic `authorized_user` file (`client_id`, `client_secret`, `refresh_token` all made up, `token_uri`
pointing at the fake), and each program run in a network namespace with only loopback
(`unshare -rn`, `lo` brought up). One early Python run was started outside the namespace and sent its
synthetic refresh request to Google's real token endpoint (answered `invalid_client`; no real credential
or user data was involved); every run below was inside the namespace.

| Consumer (version) | Honours `token_uri` of an `authorized_user` file? |
|---|---|
| Go `golang.org/x/oauth2/google` 0.37.0 | **Yes.** `FindDefaultCredentials` then `Token()` posted `grant_type=refresh_token` with the file's client id, secret and refresh token to the fake. |
| Go `cloud.google.com/go/auth` 0.24.0 | **No.** It attempted to POST to `https://oauth2.googleapis.com/token` (the dial failed in the namespace); the fake saw nothing. |
| Python `google-auth` 2.61.0 | **No.** `Credentials.from_authorized_user_info` overwrites it (`token_uri=_GOOGLE_OAUTH2_TOKEN_ENDPOINT,  # always overrides`); the one run outside the namespace attempted `https://oauth2.googleapis.com/token` and got `invalid_client` from Google. |
| Node `google-auth-library` 11.2.0 and 9.15.1 | **No.** The refresh client uses the fixed `oauth2TokenUrl`; the file's value is not read (11.2.0 failed resolving Google; 9.15.1 by source). 9.x is what `@google-cloud/storage` 8.2.0 bundles. |
| Terraform Google provider 8.6.0 (Terraform 1.16.4) | **Yes, but not with the variable the wrapper sets.** With `GOOGLE_CREDENTIALS` naming the file and no `GOOGLE_OAUTH_ACCESS_TOKEN`, the provider logged "Authenticating using configured Google JSON 'credentials'" and refreshed through the fake once per expiry (`expires_in=1` gave six requests in 25 s). With `GOOGLE_APPLICATION_CREDENTIALS` alone it logged "Authenticating using DefaultClient" and also asked the fake. With `GOOGLE_OAUTH_ACCESS_TOKEN` set beside either, it logged "Authenticating using configured Google JSON 'access_token'" and never asked the fake. |

The provider's own start-up also calls a fixed Google host (`openidconnect.googleapis.com/v1/userinfo`),
which cannot succeed in the namespace, so a complete `plan` was not run; the evidence is the token
requests and the authentication line, not an API call authorised end to end. What reached Google is only that one synthetic Python request; the Go and Node 11 attempts failed
before sending, and Node 9.15.1 was read from source, not run. Where this note says a library "would send" the
secret to Google, that is inferred from the fixed URL, and the Google error (`invalid_client` was observed
only for Python) is expected, not measured, for the others.

What follows:

- Only two of the five consumers follow `token_uri`, and one of those only when the variable that carries
  the static token is absent. Python, Node and `cloud.google.com/go/auth` ignore it, so a per-run file at
  `GOOGLE_APPLICATION_CREDENTIALS` would not make them work; it would turn today's "file not found"
  failure into a refresh request to Google carrying the per-run client secret and refresh token (random
  and useless there, but sent). The fail-closed rule of the 2026-10-02 note would still hold for them in
  effect, with a more confusing error.
- Terraform is the consumer that motivated the issue, and it can be reached without touching
  `GOOGLE_APPLICATION_CREDENTIALS`: the wrapper would set `GOOGLE_CREDENTIALS` (the provider's own
  variable) to the file and leave `GOOGLE_OAUTH_ACCESS_TOKEN` out. That is opt-in by construction (only
  the provider reads that variable), but it removes `GOOGLE_OAUTH_ACCESS_TOKEN` from the child's
  environment, which the af-gcp notes document as part of what a command gets, and which a program that
  reads it explicitly (the three client-library recipes in the 2026-10-02 note) relies on.
- The loopback listener is the same trade-off the issue names: any process in the workspace can connect,
  and the secret in the request body is the only gate. It would bind `127.0.0.1` only, compare in constant
  time, close with the command, and keep the secret in a 0600 file in the run directory, never in argv or
  environment.

Decision (the maintainer, 2026-10-10): **do not build; split the work.** The three options were:

- **The issue's option (a per-run `authorized_user` file at `GOOGLE_APPLICATION_CREDENTIALS`): rejected.**
  Three of the five consumers (Python, Node, `cloud.google.com/go/auth`) ignore `token_uri` and would send
  the per-run client secret and refresh token to Google. They are useless there, but they leave the
  workspace, and the fail-closed error would become a Google error such as `invalid_client` (observed for Python only).
- **(a) Terraform only (`GOOGLE_CREDENTIALS` plus the loopback listener): not taken for now.** Its cost is
  that `GOOGLE_OAUTH_ACCESS_TOKEN` disappears from the child's environment, which the documented contract
  and the client-library recipes rely on, and that a listener on the loopback is reachable by any process
  of the workspace with a body secret as its only gate. Worth revisiting if split runs prove too costly.
- **(c) Longer impersonated tokens (`--lifetime`): not taken.** It needs an organisation policy that
  allows longer service-account token lifetimes, which the workspace cannot assume.

Guidance for now: a `terraform apply` (or any program that reads `GOOGLE_OAUTH_ACCESS_TOKEN` once) must
finish within the token's remaining life, which each run prints at its start ("the token is valid for N more
minutes": at least ten, at most about an hour, and not necessarily a fresh hour); split longer work into
commands shorter than that, each under its own `af-gcloud-exec` run. See
[the guide](../../guide/member/10-integrations.md#running-commands-in-google-cloud-as-you-af-gcloud-exec). #1879 stays open for (a) and (c).
