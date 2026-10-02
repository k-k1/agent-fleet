---
name: af-gcp
description: "Agent Fleet workspace: running Google Cloud commands as the user - why a bare gcloud, client library or Terraform call is not the user, choosing the user's Settings > Google Cloud profile with af-gcloud-exec --list, af-gcloud-exec --profile/--project, which tools the token reaches (gcloud, the GKE auth plugin for kubectl, Terraform's Google provider) and which do not, what exit 3 and each refusal mean and who fixes them, and why you never run gcloud auth login or touch the Agent's gcloud store. Read before any gcloud, kubectl-against-GKE, Terraform-with-Google or Google client-library command about the user's projects (reads included), or when one fails with no credentials, a reauthentication error, 'Could not automatically determine credentials', or af-gcloud-exec exit 3."
user-invocable: false
---
# Google Cloud as the user: `af-gcloud-exec`

Read when: a command touches the user's Google Cloud projects (deploys, writes and reads), or a
Google Cloud command failed with no credentials, a reauthentication error, "Could not automatically
determine credentials", or `af-gcloud-exec` exited 3.

## The trap

The user's Google Cloud identity is not in your shell. A bare `gcloud`, a client library's default
credentials or Terraform's Google provider finds either nothing, a login of the user's own
`~/.config/gcloud` that is not yours to use, or — on a Google Cloud VM or GKE node — the machine's
own service account, which is another identity in possibly another project. None of those is the
user's profile. Everything about their projects goes through `af-gcloud-exec`.

## Choose the profile

```sh
af-gcloud-exec --list
```

prints each of the user's Settings > Google Cloud profiles as
`name  project  account  [impersonates <service account>]  ("label")`, and names the ones that are
not exported (two labels that map to one name, a value gcloud cannot hold). Choose by project and
account, not by the name alone. If the task does not say which project, ask the user.

## Run

```sh
af-gcloud-exec --profile <name> --project <id> -- <command> [args...]
```

- `--project` must be the profile's project; anything else is refused. A user token is not bound to a
  project, so this only checks that you and the profile agree where the command points by default:
  a command's own `--project`, or a project written in a Terraform configuration, still wins.
- The stderr line "profile … runs as <account> [(impersonated by …)] in project …; the token is valid
  for N more minutes" shows who the command acts as. Check it. The token is never printed.
- The token lasts what remained when it was minted (at least ten minutes, at most about an hour). It
  is not refreshed during the command: a long `terraform apply` or a `kubectl` watch that outlives it
  fails. Split long work into shorter runs.
- The first run installs the pinned Google Cloud SDK (`workspace-agent install-gcloud`, which
  downloads about 85 MB once and takes about 510 MB). The first run of each gcloud command after
  that is a few seconds slower once: Python compiles what it imports on first use.
- A Google login can end before its token does, when the user's organisation requires
  reauthentication after a set time. Nothing in the workspace records when that is, so there is no
  warning: runs keep working while the cached token has more than ten minutes left, and the first
  run that needs a fresh one exits 3 ("Reauthentication failed. cannot prompt during
  non-interactive execution"). Hand the user the login command as below.

What the command gets: the token in `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` and
`GOOGLE_OAUTH_ACCESS_TOKEN`, an empty private `CLOUDSDK_CONFIG`, the profile's project, quota project,
region and zone in their `CLOUDSDK_*` / `GOOGLE_*` forms, and `GOOGLE_APPLICATION_CREDENTIALS`
pointing at a path that holds no credentials. Every `CLOUDSDK_*`, `GOOGLE_*`, `GCLOUD_*` and
`GCE_METADATA_*` variable of your shell is removed first.

Which tools the token reaches (measured on SDK 587.0.0 against a local mock that checked the
`Authorization` header; ADR 0107 note of 2026-10-02):

- `gcloud`, `gcloud storage` included (through the token file), with the quota project sent as
  `X-Goog-User-Project`; and `kubectl` against GKE through `gke-gcloud-auth-plugin`, which asks that
  same gcloud.
- Terraform's Google provider (through `GOOGLE_OAUTH_ACCESS_TOKEN`), with `USER_PROJECT_OVERRIDE=true`
  and the quota project set.
- `bq`, with project and quota project. It is in the SDK but not on `PATH`: run it as
  `af-gcloud-exec … -- ~/.local/share/agent-fleet/google-cloud-sdk/bin/bq …`.
- **Not** `gsutil`: it ignores the token and sends its requests **without credentials**. A public
  bucket answers, so a read can look as if it worked; a private one refuses. Use `gcloud storage`.
- **Not** Google's client libraries (Go, Python, Node and the rest) by themselves: they ignore the
  token variables and stop at the empty `GOOGLE_APPLICATION_CREDENTIALS` with an error such as
  "File … was not found", "dialing: open …: no such file or directory" or "The file at … does not
  exist". That is deliberate: it stops them from finding another identity. A program that needs a
  library must take the token from `GOOGLE_OAUTH_ACCESS_TOKEN` explicitly; changing the user's code
  that way needs their agreement. Measured to work:
  - Go (`cloud.google.com/go/storage` 1.69.0):
    `option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN")}))`.
    It sends no quota project unless `GOOGLE_CLOUD_QUOTA_PROJECT` is set (the wrapper does not set
    it; `export GOOGLE_CLOUD_QUOTA_PROJECT="$GOOGLE_BILLING_PROJECT"` inside the command does), and
    refuses `option.WithQuotaProject` beside a token source.
  - Python (`google-cloud-storage` 3.16.0, `google-auth` 2.59.1):
    `google.oauth2.credentials.Credentials(os.environ["GOOGLE_OAUTH_ACCESS_TOKEN"], quota_project_id=os.environ["GOOGLE_BILLING_PROJECT"])`;
    the project comes from `GOOGLE_CLOUD_PROJECT`.
  - Node (`@google-cloud/storage` 8.2.0): an `OAuth2Client` with
    `setCredentials({access_token: process.env.GOOGLE_OAUTH_ACCESS_TOKEN})`, passed as `authClient`,
    **from the same `google-auth-library` the client library depends on** (8.2.0 bundles 9.x). One from
    another major version (11.x) was accepted without an error and sent **no** credential at all.

## When it stops

| What you see | Meaning | What to do |
|---|---|---|
| exit 3, "Google Cloud login required … log in from a terminal with: af-gcloud-exec --profile … --project … --login -- true" | The profile has no login yet, it expired, or Google asks for reauthentication. Exit 3 is only ever a login. | You cannot log in for the user. Give them that exact command to run in their own terminal (in Claude Code: `!` at the prompt). It prints a Google sign-in URL; they sign in in their browser and paste the code back into **that** terminal. Rerun once they say it is done. |
| exit 1, "is for project X, not Y; --project must be the profile's project" | Wrong profile for this project. | Recheck `--list`. Ask the user; do not switch `--project` to match. |
| exit 1, "no Google Cloud profile …" / "not exported: …" | The name is not one of the user's exported profiles. | Report it; the user adds or fixes the profile in Settings > Google Cloud. |
| exit 1, "holds a … credential …, not a user login" | Something other than a user login is stored for that account in the Agent's store. | Report it to the user; do not try to fix the store. |
| exit 1, "gcloud could not mint a token: …" | Not a login problem: permission denied (including on impersonation), a disabled API, the network. | Report the error as it is; do not ask the user to log in. |
| exit 1, "the token gcloud minted is valid for only …" | gcloud could not mint a token with ten minutes left. | Rerun in a minute; if it repeats, report it. |
| exit 1, "this deployment does not export Google Cloud profiles" | No profiles in this workspace. | Tell the user; there is no other route. |
| exit 1, "the login finished but still gave no usable credential: …" | The user signed in, and the credential still does not work. | Report the message; do not start another login yourself. |
| exit 1, "the profile changed in Settings while this run started; run it again" | Settings changed the profile between the run's check and its mint. | Rerun once; recheck `--list` if the account or project now differs from what you expected. |
| "waiting for another af-gcloud-exec or a profile sync …" (stderr) | A login in a terminal, or another run, holds the Agent's store. | Nothing; the run continues when it is released. |

## Never

- Run a user-identity action with bare `gcloud`, a client library or Terraform outside
  `af-gcloud-exec`, or retry that way after it refused.
- Run `gcloud auth login`, `gcloud auth application-default login`, `gcloud config set` or any gcloud
  command with `CLOUDSDK_CONFIG` pointed at the Agent's store (`~/.local/state/agent-fleet/gcloud`),
  or edit files there. Settings owns those configurations and the user owns the logins.
- Run `af-gcloud-exec --login` in your own shell, or pass a sign-in URL or code to the user: only a
  sign-in the user started themselves in their own terminal is safe to complete.
- Use the user's own `~/.config/gcloud` (their logins and application default credentials), or the
  machine's service account, instead.
- Print, write or paste the token (`GOOGLE_OAUTH_ACCESS_TOKEN`, the token file), or `env` output.
