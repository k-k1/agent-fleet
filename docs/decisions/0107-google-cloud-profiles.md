# 0107. Google Cloud profiles: Settings defines them, `af-gcloud-exec` runs with one, and the Console finishes the gcloud login

English | [日本語](0107-google-cloud-profiles.ja.md)

- Status: **proposed** (2026-10-02). Nothing is built yet. The gcloud behaviour below was measured on
  Google Cloud SDK 587.0.0 in a workspace; what was not measured says so.
- Tracking: #1092 (Tier 2, "a gcloud counterpart of the user's own cloud features")
- Related: [0102](0102-aws-login-through-the-console.md) (the AWS login through the Console, whose
  machinery this reuses) / [0106](0106-kubernetes-runtime.md) (the metadata server blocked on GCE and
  GKE) / [0104](0104-long-lived-member-workspace.md) (why `~/.local` and the home outlive a stop)

## Context

A member can define AWS profiles in Settings and run any command as one of them with
`af-aws-exec --profile <name> --account <id> -- <command>`. The profile reaches the workspace through a
per-member bridge, the wrapper keeps the workspace's own cloud identity out of the command, and when the
login is missing the Console finishes it ([0102](0102-aws-login-through-the-console.md)). Nothing of the
kind exists for Google Cloud: no gcloud in the image, no profile, no wrapper, and the policy text tells
agents nothing about Google credentials. A member who runs Agent Fleet on Google Cloud (ADR 0106) and
operates their own projects from a session has to install gcloud by hand and paste a login into a
terminal.

### What carries over from AWS unchanged

- **The bridge.** A per-membership HMAC token, injected into the workspace, verified on each pull with
  a live membership lookup, served on the workspace-only listener's allowlist
  (`aws_profiles_bridge.go`, `workspace_listener.go`).
- **The Agent's pull loop.** Once at boot and every five minutes, under a lock, with a cache bound to
  the token for when the CP is unreachable (`internal/awsx/profiles.go`).
- **The Console login machinery of 0102.** A request filed by the wrapper, an outbox notice whose
  payload is only an id, a sticky toast, a start that only the member's press can trigger and whose
  result only the pressing tab sees, the CP relay with an audit record, the wait-then-exit-3 contract.
- **The wrapper's shape.** `syscall.Exec` into the command, a private state directory, `--list`,
  exit codes 2 (usage), 3 (login needed and not started), 1 (refused).

### What gcloud does differently (measured)

- **The login runs the other way.** `gcloud auth login --no-launch-browser` prints an
  `https://accounts.google.com/o/oauth2/auth?…` URL whose redirect is
  `https://sdk.cloud.google.com/authcode.html`, then waits on stdin for "the verification code provided
  in your browser". The member signs in in their browser and **pastes a code back**. AWS's device flow
  has the member type a code shown by the CLI into the IdP instead.
- **The code is bound to the process that asked for it.** The URL carries PKCE
  (`code_challenge_method=S256`): a code obtained through any other URL cannot complete this login.
- **Two credential stores.** gcloud's own credentials (`credentials.db` under `CLOUDSDK_CONFIG`), and
  Application Default Credentials, a single file that Google's client libraries and Terraform read.
  `--update-adc` writes both from one login.
- **No SDK-wide switch away from the metadata server.** AWS has `AWS_EC2_METADATA_DISABLED`; Google's
  libraries fall back to the metadata server whenever they find nothing else. ADR 0106 blocks it at
  the network on GCE and GKE; elsewhere there is no metadata server to fall back to.
- **Size.** The SDK unpacks to 486 MB (88 MB compressed).

## Decisions

### 1. A Google Cloud profile is a member's Settings row, like an AWS profile

Settings gains a **Google Cloud** section beside AWS, member-scoped like it. A profile has:

| Field | Required | Meaning |
|---|---|---|
| label | yes | the name, sanitised the way AWS profile names are |
| project | yes | the default project, and what `--project` is checked against |
| account | no | the Google account to log in as; when set, a login as anyone else is refused |
| region, zone | no | written as the configuration's `compute/region` and `compute/zone` |
| impersonate service account | no | the service account the command acts as, through the logged-in account (gcloud's `--impersonate-service-account`) — the counterpart of an AWS role |

No secret is stored in the CP: as with AWS, the credential is obtained inside the workspace by the CLI.
Service-account JSON keys are not accepted anywhere — they are long-lived secrets, many organisations
forbid them, and impersonation covers the same need.

The profiles reach the workspace over `GET /internal/gcp-profiles` with its own token
(`AF_GCP_PROFILES_TOKEN`), added to the workspace-only listener's allowlist. The Agent renders each
profile as a gcloud named configuration `af-<name>` in the member's own gcloud directory, so that
`gcloud --configuration af-<name>` also works by hand in a terminal. A configuration of that name the
member made themselves is left alone and the profile is reported as shadowed, as an AWS profile is.

### 2. `af-gcloud-exec --profile <name> --project <id> -- <command>` runs a command as one profile

- **`--project` must match the profile's project**, as `--account` must match an AWS profile's
  account: the check that the command lands where the member meant.
- **The command gets a short-lived access token, not the member's refresh token.** The wrapper asks
  gcloud for an access token as the profile (`gcloud auth print-access-token --configuration af-<name>`,
  with impersonation when the profile sets it), writes it to a file in a private directory, and starts the
  command with:
  - `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` — gcloud uses that token and nothing else;
  - `GOOGLE_OAUTH_ACCESS_TOKEN` — Terraform's Google provider uses it;
  - `CLOUDSDK_CORE_PROJECT`, `GOOGLE_CLOUD_PROJECT`, and the region and zone;
  - `CLOUDSDK_CONFIG` pointed at an empty private directory, so the command's gcloud cannot reach any
    other account the member is logged in as;
  - `GOOGLE_APPLICATION_CREDENTIALS` and every other `CLOUDSDK_*` override from the caller removed.
- **One login serves gcloud and Terraform.** No ADC file is written or read: the access token is what
  both receive. `--update-adc` is not used, because ADC is one file for every profile and would make
  "which identity is this" depend on whichever login ran last.
- **The token lives an hour.** A command that runs longer than that sees its token expire; the wrapper
  says so in its usage text, and a re-run is the remedy. Measuring which tools refresh by themselves is
  open question 1.
- **Exit codes and `--list` follow `af-aws-exec`.**

What the command can still do: a Google client library that ignores both variables falls back to ADC and
then to the metadata server. With no ADC file in the private config and the metadata server blocked on
GCE and GKE (ADR 0106), that fallback fails rather than acting as the workspace's own identity. The usage
text and the notes say which tools are covered.

### 3. The Console finishes the login, and the member pastes the code into it

Without a terminal, `af-gcloud-exec` files a login request exactly as `af-aws-exec` does (0102 decisions
1, 2, 5 and 6 apply unchanged, with one request per profile). The difference is the start:

1. The member presses **Log in** in the toast or in Settings. The Agent starts
   `gcloud auth login [<account>] --no-launch-browser --configuration af-<name>` in its own process group,
   one attempt per profile.
2. The Agent reads the URL from its output and checks it before showing it: scheme `https`, host exactly
   `accounts.google.com`, and a `redirect_uri` of exactly `https://sdk.cloud.google.com/authcode.html`.
   Anything else ends the attempt.
3. Only the tab that pressed sees the URL, which opens only on the member's press. The modal has one
   field: **the verification code**.
4. The member pastes the code. The Console posts it to the Agent's attempt (relayed and audited by the
   CP as `gcp.login.submit`), and the Agent writes it to that gcloud's stdin. A code is accepted only for
   an attempt that exists, is still waiting, and was started by the same tab.
5. When the profile names an account and the login produced another, the Agent revokes the new
   credential and reports the mismatch.

The rule the guide teaches carries over in its gcloud form: **paste a code only into a login you
started yourself**. PKCE makes a code useless anywhere but the gcloud that asked for it, and the press
rule makes sure that gcloud was started by the member.

### 4. gcloud is installed on demand, pinned, into `~/.local`

`workspace-agent install-gcloud` installs one pinned version, checked against its sha256, with the
core components only, into `~/.local`, which outlives a stop and a Recreate. `af-gcloud-exec` runs it on
first use, as `af-aws-exec` runs `install-awscli`. The version is pinned the way `AWSCLI_VERSION`
is — a Dockerfile `ARG` recorded in `versions.json`, read back by `readBuildPins` — with the versioned
archive (`google-cloud-cli-<version>-linux-<arch>.tar.gz`) and its sha256 for both architectures.
Bumping it is a manual pin change, as for the AWS CLI (the CLI pin-bump workflow covers the agent CLIs
only). The Toolchain tab's table of effective tool versions lists it.

It is not baked into the image. 486 MB would land on every cold image pull — Fargate pulls cold at
every start, and GKE nodes pull per node — for a tool a fraction of members use.

### 5. The rest of the AWS surface gets its counterpart

- The workspace bar shows a Google Cloud badge beside the AWS one when profiles exist.
- `workspace/notes/gcp.md` (the `af-gcp` skill) teaches the wrapper, the paste rule and the exit
  codes; the policy text in `workspace/workspace-notes.md` gains the Google Cloud bullet: the default
  credential chain is not the member, and anything about their projects goes through `af-gcloud-exec`.
- The guide's integrations and Settings pages, in English and Japanese.

### Out of scope

- **Workforce Identity Federation** (`gcloud auth login --login-config`), for organisations that sign in
  to Google Cloud through a non-Google IdP. Its login is a different flow; a follow-up issue.
- **A session kind** like SSM's, e.g. `gcloud compute ssh --tunnel-through-iap`; a follow-up.
- **Google Cloud MCP servers**, the counterpart of the AWS MCP builtin.
- **Service-account keys** (decision 1).

## Rejected

- **Storing refresh tokens in the CP** and minting access tokens there. It would make the CP a store of
  members' Google credentials, which the AWS design deliberately avoided.
- **ADC as the shared store** (`--update-adc`). Decision 2.
- **Baking gcloud into the image.** Decision 4.
- **Handing the command the member's whole gcloud directory.** Every account the member is logged in as
  would be reachable from any command run as any profile.

## Consequences

- A second cloud's worth of Settings, bridge, wrapper, login and notes to keep in step with the first.
  The generic parts — the login request and attempt machinery, the bridge token, the wrapper skeleton —
  are worth extracting from `awsx` once, rather than copying.
- The Console's login modal grows an input field. For AWS it only displayed a code; here the member's
  paste is a secret in transit through the CP relay, so the relay must not log the body.
- Commands that outlive an access token fail and are re-run.

## Open questions (decide after measuring)

1. Which tools honour `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` / `GOOGLE_OAUTH_ACCESS_TOKEN`: gcloud and
   Terraform are expected to; `gke-gcloud-auth-plugin` (kubectl against GKE), `gsutil`/`gcloud storage`,
   `bq` and the client libraries are to be measured.
2. How an organisation's reauthentication policy (session length for Google Cloud) shows up, and whether
   the Agent can warn before it, as it does for AWS SSO.
3. Whether `install-gcloud` can drop `bq` and the bundled extras to shrink below 486 MB.

## Phases

| Phase | What | Done when |
|---|---|---|
| 1 | Decisions 1, 2, 4: Settings, bridge, `af-gcloud-exec`, `install-gcloud`, with the terminal login | a member runs `gcloud` and `terraform plan` against their project through the wrapper, and a project mismatch is refused |
| 2 | Decision 3: the Console login with the code paste | an agent's `af-gcloud-exec` is finished from the Console without a terminal |
| 3 | Decision 5 and open question 1 | the notes, guide and badge ship; the measured tool list is in the notes |
