---
audience: "anyone looking up af-gcloud-exec's options, output, exit codes and exact behaviour"
source_of_truth: "af-gcloud-exec --help, from the workspace-agent in the workspace"
---

# af-gcloud-exec

English | [日本語](af-gcloud-exec.ja.md)

Runs one command with a short-lived access token of one Google Cloud profile. Only that command gets the token. The
usual flow is in [Use Google Cloud](../../user/integrations/gcloud.md).

## Synopsis

```sh
af-gcloud-exec --profile <name> --project <project-id> [--login | --no-login] [-q] -- <command> [args...]
af-gcloud-exec --list
af-gcloud-exec --help
af-gcloud-exec --version
```

Everything after `--` is the command to run. `--profile` and `--project` can also be written as `--profile=<name>`.
Give only one of `--login` and `--no-login`: when both are given, the last one wins.

## Options

| Option | Meaning |
|---|---|
| `--profile <name>` | The profile to use. Required. `--list` shows the names |
| `--project <id>` | The profile's project. Required. Anything else is refused, running nothing ([`--project`](#--project)) |
| `--login` | When the profile has no usable login, always run the gcloud login in this terminal |
| `--no-login` | Never prompt: print the command to run in a terminal and exit with status 3 |
| `-q`, `--quiet` | Do not print the account and the token's remaining minutes |
| `--list` | Pull the profiles from Settings now and list the names you can use |
| `-h`, `--help` | Print the help |
| `--version` | Print the version (the workspace-agent build it belongs to) |

## Exit status

| Status | Meaning |
|---|---|
| The command's own | When the command ran, its own exit status is returned as is |
| 1 | Any other refusal or failure (the project differs from `--project`, no token could be minted, for example) |
| 2 | Usage error |
| 3 | A login was needed and did not happen (`--no-login`, no login in the Console in time, Ctrl-C while waiting, or the token ended during the run and the login has to be done again) |

Exit status 3 is always about a login. A permission, an API or the network ends with exit status 1.

## Output

Before running the command, it prints one line to standard error (`-q` turns it off). The token itself is never
printed.

```
af-gcloud-exec: profile prod runs as deployer@acme-shop-prod.iam.gserviceaccount.com (impersonated by you@example.com) in project acme-shop-prod; the token is valid for 59 more minutes
```

That line shows which account the command actually runs as. For a profile that impersonates no one, only the account
you logged in with appears, as in `runs as you@example.com`.

## Reading `--list`

```
$ af-gcloud-exec --list
analytics   acme-analytics      (chosen at the first login)                                                ("analytics")
prod        acme-shop-prod      you@example.com  impersonates deployer@acme-shop-prod.iam.gserviceaccount.com  ("prod")
staging     acme-shop-staging   you@example.com                                                            ("staging")
prod-app    (not exported: Settings labels Prod App / prod-app all map to this name; rename all but one)
```

The real output is tab-separated; it is aligned with spaces here for reading.

- One profile per line: name, project, account, the service account it impersonates (if any), and the label
- A profile with no account set shows the account chosen at login, or `(chosen at the first login)` before the first
  login
- Profiles that cannot be used come after the list as `name  (not exported: reason)`. The reason is, for example, two
  labels that make the same name, or a value gcloud does not accept

Choose by project and account, not by the name alone.

`--list` reads the profiles from Settings at that moment. When Settings cannot be read, it uses the copy it read last
time and says so.

## `--project`

`--project` must be the profile's project. Anything else stops with exit status 1, running nothing.

- A Google token is not bound to a project. This only checks that you and the profile agree on where the command
  points by default; it does not narrow what the token can reach
- A command's own `--project`, or a project written in a Terraform configuration, still wins

## What the command receives

The command receives the profile's short-lived **access token** and nothing else: not your own gcloud login, not
application default credentials, not the machine's own Google identity. Your own `~/.config/gcloud` is neither read
nor changed.

| Variable | Contents |
|---|---|
| `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE` | The token file. gcloud and the GKE auth plugin read it |
| `GOOGLE_OAUTH_ACCESS_TOKEN` | The token itself. Terraform's Google provider reads it |
| `CLOUDSDK_CONFIG` | An empty gcloud configuration directory for this run only |
| `CLOUDSDK_CORE_PROJECT`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_PROJECT` | The profile's project |
| `CLOUDSDK_BILLING_QUOTA_PROJECT`, `GOOGLE_BILLING_PROJECT`, `GOOGLE_CLOUD_QUOTA_PROJECT` | The quota project (the project when left blank), with `USER_PROJECT_OVERRIDE=true` |
| `CLOUDSDK_COMPUTE_REGION`, `GOOGLE_REGION`, `CLOUDSDK_COMPUTE_ZONE`, `GOOGLE_ZONE` | The profile's region and zone (only when set) |
| `GOOGLE_APPLICATION_CREDENTIALS` | A path that holds no credentials |

Every `CLOUDSDK_*`, `GOOGLE_*`, `GCLOUD_*` and `GCE_METADATA_*` variable of your shell is removed first.

## Which tools the token reaches

| Tool | Uses the token |
|---|---|
| `gcloud` (`gcloud storage` included) | Yes |
| `kubectl` against a GKE cluster | Yes (through the GKE auth plugin; see [Use kubectl](#use-kubectl)) |
| Terraform's Google provider | Yes (with the quota project; renewals during the run do not reach it) |
| `bq` | Yes. It is in the SDK but not on `PATH`: run it as `~/.local/share/agent-fleet/google-cloud-sdk/bin/bq` |
| `gsutil` | **No**. It ignores the token. Use `gcloud storage` instead |
| Google's client libraries (Go, Python, Node and the rest) | Only when the program hands them the token ([Use a client library](#use-a-client-library)) |

With no boto configuration of your own, `gsutil` sends its requests **without credentials**. A public bucket answers,
so a read can look as if it worked. With a configuration, it can run as the other identity in it.

### Use kubectl

Fetch the cluster's connection details once through the profile, and run `kubectl` through `af-gcloud-exec` too. The
GKE auth plugin asks gcloud for the token, and gcloud holds the token only inside `af-gcloud-exec`.

```sh
af-gcloud-exec --profile prod --project acme-shop-prod -- gcloud container clusters get-credentials <cluster> --location <location>
af-gcloud-exec --profile prod --project acme-shop-prod -- kubectl get pods
```

### Use a client library

A program built on a client library stops, as it is, with an error such as "File … was not found" or "no such file or
directory". That is deliberate: otherwise it would find another identity, such as your own gcloud login or the
machine's.

Write the program so that it takes the token itself.

| Language | How |
|---|---|
| Python | `google.oauth2.credentials.Credentials(os.environ["GOOGLE_OAUTH_ACCESS_TOKEN"], quota_project_id=os.environ["GOOGLE_BILLING_PROJECT"])` |
| Go | `option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN")}))`. The quota project is read from `GOOGLE_CLOUD_QUOTA_PROJECT` |
| Node | An `OAuth2Client` with `setCredentials({access_token: process.env.GOOGLE_OAUTH_ACCESS_TOKEN})`, passed as `authClient` |

- In Node, create the `OAuth2Client` from **the same major version** of `google-auth-library` that the client library
  uses. One from another version is accepted without an error and sends no credential at all
- Agents ask you before changing your code this way

## Token lifetime

- A token lasts what remained when it was minted: at least ten minutes, at most about an hour. When only a token with
  less than ten minutes left can be minted, nothing runs (try again in a minute)
- While the command runs, `af-gcloud-exec` stays beside it and **renews the token file** about eight minutes before
  the token ends. Every `gcloud`, `bq` or GKE auth plugin started after that reads the new token, so a script that
  calls them again keeps working
- A process that keeps the token it read at start fails when that token ends: one long-running `gcloud`, and a program
  that reads `GOOGLE_OAUTH_ACCESS_TOKEN` once (**a long `terraform apply`**). Split such work into shorter runs
- Whether a `kubectl` watch or an open connection survives depends on the plugin being run again and the connection
  being re-made (not checked)

### When the token cannot be renewed

When the token cannot be renewed (the login was revoked or needs signing in again, a permission, the network):

1. It says so on standard error and keeps trying until the token ends
2. When the token ends, it stops the command (SIGTERM, then SIGKILL if it has not ended after 30 seconds)
3. It ends with exit status 3 when the login has to be done again, and exit status 1 otherwise

If the run could ask the Console when it started, the login request appears in the Console again.

### Stopping the command

Stopping `af-gcloud-exec` (or killing it) sends SIGTERM to the command it started. The command can ignore it, and
programs that command started are not reached. When `af-gcloud-exec` is killed, the token file stays until the token
expires, and a later run removes it.

## When there is no login

What happens when the profile has no login, or Google refuses the stored one, depends on where the run comes from.

| From | What happens |
|---|---|
| An agent | Asks for the login in the Console and waits about a minute and a half. If it is not done by then, ends with exit status 3 |
| Another session in the workspace (a shell script, say) | Asks in the Console and waits a few seconds |
| A terminal in the workspace | Asks in the Console and waits up to ten minutes. Ctrl-C stops waiting with exit status 3; the request stays in the Console |
| With `--login` | Starts the gcloud login in that terminal |
| With `--no-login`, or outside a workspace | Ends with exit status 3 and prints the command to run in a terminal |

When it asks the Console, standard error says so. There is one request per profile; a second run waits on the same
request.

```
af-gcloud-exec: Google Cloud login for profile "prod" requested in the Agent Fleet Console; waiting up to 1m30s for the member to finish it there
```

After a request is cancelled in the Console, it does not ask again for that profile for about a minute.

### Log in from a terminal

```sh
af-gcloud-exec --profile <name> --project <project-id> --login -- true
```

Open the URL it prints in your browser, sign in, and paste the verification code from the page into **that**
terminal. Paste only the code of a login you started yourself, just now. From Claude Code, typing `!` followed by the
command at the prompt runs it as your own terminal.

On a workspace that runs directly on a Google Cloud VM, gcloud asks whether to use a personal account on that VM. The
Console login does not answer that, so log in this way.

## Runs show in the session's conversation

Each run leaves one line in the session's conversation:

- the profile, the project, the account (and the service account it impersonates)
- the program's name and the command's exit status, or, when nothing ran, the reason

The command's arguments are never recorded. `--list` and `--help` are not recorded. Recent runs from every session
are under "**Recent runs**" on the "Google Cloud" tab of Settings.

## Google Cloud SDK

- The first run installs one pinned version of the Google Cloud SDK and the GKE auth plugin into your home. The
  download is about 85 MB; it takes about 510 MB on disk
- It survives a stop and a recreate. Once installed, gcloud appears in the tool versions table on the
  "**Toolchains**" tab of Settings
- After that, the first run of each gcloud command is a few seconds slower
- A bare `gcloud` in a terminal runs the same program, but with your own login (if any), not the profile's

## Messages when it stops

| Message | Meaning | What to do |
|---|---|---|
| exit status 3, `the login was requested in the Agent Fleet Console and is waiting …` | There is no login yet, or Google refused the stored one, and the run stopped waiting | Log in from the toast or from Settings, and run it again |
| exit status 3, `the token of profile … has ended and could not be renewed, so the command was stopped: …` | The token ended during the run and the login has to be done again | Run the terminal command in the message if there is one; otherwise log in in the Console |
| exit status 3, `the login request was cancelled in the Agent Fleet Console` | You cancelled the request | To log in, do it from Settings |
| exit status 3, `Google Cloud login required … log in from a terminal with:` | The Console could not be asked (`--no-login`, or outside a workspace) | Run the command shown in your own terminal |
| `is for project X, not Y; --project must be the profile's project` | That profile is for another project | Pick again with `--list` |
| `no Google Cloud profile …`, `not exported: …` | No usable profile has that name | Add it or fix it in Settings |
| `gcloud could not mint a token: …` | Not a login problem: a missing permission (including on the service account to impersonate), an API that is not enabled, or the network | Fix the reason in the message |
| `the token gcloud minted is valid for only …` | No token with ten minutes left could be minted | Try again in a minute |
| `the login finished but still gave no usable credential: …` | You signed in, and the credential still does not work | Check the message |
| `this deployment does not export Google Cloud profiles` | This deployment does not provide Google Cloud profiles | There is no other route |
| `could not renew the token of profile … trying again until it ends in N minutes` | The token in use has not been renewed yet | Wait. If the token ends first, the command is stopped |
| `the profile's login is now X, not Y, which the command started as; run it again` | The profile was logged in again with another account during the run | Run it again |
| `the profile changed in Settings while this run started; run it again` | Settings changed the profile while the run was starting | Run it again. If the account or project changed, check `--list` |
| `waiting for another af-gcloud-exec or a profile sync …` | A login in a terminal, or another run, is using the workspace's gcloud store | Nothing. It continues when that ends |

## What is not guaranteed

- The command runs as you, so it can read your home, your own gcloud directory included. `af-gcloud-exec` only makes
  sure that the standard ways of finding Google credentials (gcloud's and the client libraries') find nothing but the
  token it gave
- Tools that look elsewhere, such as `gsutil` with its own configuration, are out of scope
- On a workspace that runs on a Google Cloud VM or in a Kubernetes Pod, a program that ignores the standard ways can
  reach the VM's or node's identity (Workload Identity on GKE). Unless an administrator blocks that route in the
  network, the workspace cannot prevent it
