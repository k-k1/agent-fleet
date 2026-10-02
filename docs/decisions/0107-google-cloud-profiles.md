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
- **`GCE_METADATA_*` is removed for the child too**, beside `CLOUDSDK_*`, `GOOGLE_*` and `GCLOUD_*`:
  a caller's metadata host must not steer a library that does reach for the metadata server.
