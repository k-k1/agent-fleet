---
audience: "anyone looking up af-aws-exec's options, output, exit codes and exact behaviour"
source_of_truth: "af-aws-exec --help, from the workspace-agent in the workspace"
---

# af-aws-exec

English | [日本語](af-aws-exec.ja.md)

Runs one command with the short-lived credentials of one AWS profile. Only that command gets them. The usual flow is
in [Use AWS SSO](../../user/integrations/aws-sso.md).

## Synopsis

```sh
af-aws-exec --profile <name> [--account <id>] [--region <region>] [--login | --no-login]
            [--keep-aws-config] [-q] -- <command> [args...]
af-aws-exec --list
af-aws-exec --help
af-aws-exec --version
```

Everything after `--` is the command to run. Give only one of `--login` and `--no-login`: when both are given, the last one wins.

## Options

| Option | Meaning |
|---|---|
| `--profile <name>` | The profile to use. Required. `--list` shows the names |
| `--account <id>` | Refuse, running nothing, unless the profile is this AWS account. Required for a profile that is **not** one of your Settings profiles (one you defined in `~/.aws` yourself) |
| `--region <region>` | The region for the command. How it is chosen: [Region](#region) |
| `--login` | When the SSO login is not usable, run the device-code login in this terminal |
| `--no-login` | Never prompt: print the login command and exit with status 3 |
| `--keep-aws-config` | Give the command your own `~/.aws` files and `AWS_ENDPOINT_URL*` settings as well |
| `-q` | Do not print the identity the command runs as |
| `--list` | Pull the profiles from Settings now and list the names you can use |
| `-h`, `--help` | Print the help |
| `--version` | Print the version |

## Exit status

| Status | Meaning |
|---|---|
| The command's own | When the command ran, its own exit status is returned as is |
| 1 | Any other refusal or failure (the account differs from `--account`, for example) |
| 2 | Usage error |
| 3 | The SSO login was needed but not started (`--no-login`, no approval in the Console in time, or Ctrl-C while waiting) |
| 4 | The profile asks for confirmation before use, and it was denied in the Console, not given in time, or the run belongs to no Agent Fleet session |

With status 4 the request stays in the Console. Allow it, then run the command again.

## Output

Before running the command, it prints one line to standard error (`-q` turns it off):

```
af-aws-exec: running as arn:aws:sts::222222222222:assumed-role/AWSReservedSSO_Developer_…/you@example.com in region ap-northeast-1 (expires …)
```

That line shows which account and role the command actually runs as. `(expires …)` appears only when the credentials carry an expiry; with no region set, the line reads `in region (none)`.

## Reading `--list`

```
$ af-aws-exec --list
Sandbox      333333333333  Developer  ("Sandbox")
Staging      222222222222  Developer  ("Staging")
Deploy       444444444444  deploy     ("Deploy", role assumed from Staging)
Production   111111111111  Deployer   ("Production", asks for confirmation before use: usable through af-aws-exec only, not in ~/.aws/config)
prod-app     (not exported: Settings labels prod app / prod-app all map to this name; rename all but one)
```

The real output is tab-separated; here the columns are aligned with spaces.

One profile per line, tab-separated: name, account, role and label. The label is quoted inside parentheses.

- A profile that assumes a role shows `("label", role assumed from <source>)`
- A profile set to confirm before use says after its label that it is not written to `~/.aws/config`
- A profile that was not written to `~/.aws/config` is listed after the others as `name  (not exported: reason)`, with
  no account or role columns. The reasons include two labels that map to the same name, a definition of the same name
  in your own `~/.aws`, a missing account or role, and a clash with a `[DEFAULT]` line

## Profile names and ~/.aws/config

Your profiles are also written into **`~/.aws/config`**, so `aws --profile <name>`, the AWS SDKs and build tools
(Gradle, Maven, CDK, Terraform, …) can select them by name without you copying anything. Prefer `--profile` (or a
tool's own profile setting) to `AWS_PROFILE=<name>`: keys already exported in the shell (`AWS_ACCESS_KEY_ID` and
friends) win over `AWS_PROFILE`, so it does not pin who a command runs as.

- **The name** is the profile's label with every character other than letters, digits and `._@-` replaced by
  `-` (label `prod app` → profile `prod-app`). `af-aws-exec --list` prints each name with its account, role and
  label; pick by those, not by the name alone.
- If two labels map to the same name (`prod app` and `prod-app`), **neither is exported** and `--list` says so:
  either one could be the wrong account. Rename one of them in Settings.
- The profiles sit in a **managed block** at the end of the file, between two `# agent-fleet` marker lines. Edit
  them in Settings, not inside the block: the block is rewritten, and so is anything `aws configure set` writes
  into it. Everything outside it is yours and is kept.
  Only profiles with both an **account and a role** are exported. With neither, `aws --profile <name>` would not
  use SSO at all and would quietly run as the workspace's own role; with only one it would fail. `--list` says
  which is missing.
  If a `[DEFAULT]` line in `~/.aws/config` would break a profile (a different region, a `role_arn` that would make
  every profile assume that role, …), that profile is not exported, and `af-aws-exec --list` names the line.
  If you already defined a profile with the same name yourself (in `~/.aws/config` or `~/.aws/credentials`),
  **your definition is used** and ours is left out. A profile labelled `default` is never exported: it would
  change what every command without a profile runs as.
- Changes arrive **within about five minutes**, at the next workspace start, or immediately when you run
  `af-aws-exec`.

## Logging in from a terminal

The profile row's **Log in** (above) does this in the Console. In a terminal, plain
`aws sso login` opens a callback on `127.0.0.1` inside the workspace, which
your browser cannot reach. Use the device-code flow instead:

```sh
aws sso login --profile <name> --use-device-code --no-browser
```

Open the URL it prints and approve the code. Approve only a code you started yourself just now. The login is shared with
SSM sessions of the same profile, so logging in once covers both.

## Why a command needs a profile

In a container workspace (docker or AWS ECS, Agent Fleet 0.26.0 or later), your sessions and terminals get no AWS
identity of their own:

- The workspace's own role (the *workload role*) is withheld: the Agent keeps its credentials variables out of
  everything it starts.
- The machine's identity is withheld too: the SDKs' instance metadata lookup is switched off
  (`AWS_EC2_METADATA_DISABLED=true`). A tool that ignores that variable is stopped only by the host's network block,
  which holds once your administrator has finished the 0.26.0 migration
  ([operator guide 04](../../operate/04-secure.md), "Other operational controls").
- A workspace that runs directly on your own machine is left as it is: an instance role there is your machine's, and
  the SDKs still find it.

So what a command runs as depends on whether it names a profile:

| The command | What happens |
|---|---|
| Names no profile (a bare `aws …`, an SDK's default credential chain, a build tool with no profile setting) | Fails with "Unable to locate credentials", or its SDK's wording |
| Names a profile that is misspelled or logged out | Fails with that profile's own error |
| Names no profile, where your administrator lets the workspace use its own task role again (AWS ECS) | Quietly runs as that role, in another account |

"Unable to locate credentials" means **name your profile**, not "configure credentials". Do not answer it with
`aws configure`, `aws login` (the CLI's own hint) or keys in a `[default]` section of `~/.aws`, and do not look for
credentials elsewhere. For deployments, writes and anything else whose account matters, pass your credentials
explicitly:

```sh
af-aws-exec --profile <name> -- ./gradlew deploy
af-aws-exec --profile <name> -- npx cdk deploy
```

## Example: a build tool with an S3 upload plugin

A Gradle deploy task built on an AWS plugin (an S3 upload task, for instance) with no profile in the build script asks
the SDK's default chain, in this order: environment variables, JVM system properties, the `default` profile in
`~/.aws`, then the container credentials and instance metadata. In a session, `./gradlew uploadArtifact` therefore
stops with "Unable to load AWS credentials from any provider in the chain" instead of uploading as the workspace's
role. Run it as you, naming the account:

```sh
af-aws-exec --profile <name> --account <id> --region <region> -- ./gradlew uploadArtifact
```

- The plugin finds the profile's short-lived credentials in its environment, the first place the chain looks.
- The "running as" line shows who uploads.
- If the build script names a profile itself (`profileName = "prod"`, say), see
  [The config the command gets](#the-config-the-command-gets).

## Every run shows in the session's conversation

Each `af-aws-exec` run leaves one line in the conversation of the session that ran it.

- **What it shows**: the profile, the account and role it ran as, the region, the command (`aws s3 ls`, or just the
  program's name), or why af-aws-exec refused to run it. **Never the command's arguments**, which can carry secrets.
- **Where it sits**: under the shell call that ran it, inside the turn's work, so a turn with many runs keeps its
  reply in view.
  - Several runs of one call are one line with a count that opens to each run; a refused one is counted in red.
  - Runs that no call can be matched to (a kind whose conversation records no times, a background process) are
    collapsed the same way between the turns, at the time they started.
  - A shell call whose label is cut opens to the whole command it ran.
- **What it does not show**: how the command ended. af-aws-exec becomes the command, so its exit status is the one
  your shell sees.
- **Every session's runs**: Settings > AWS profiles/SSM lists the latest runs of every session under "Recent runs".
- **It is not an audit.** It is there so you can see what ran: the session is the one the run reported, which the
  command can change, and a command run with plain `aws` leaves no line.

## Confirm before use

A profile can be set to "Confirm before use" in its form in Settings > AWS profiles/SSM. `af-aws-exec` then asks you
before that profile's first use in each session, before it fetches any credential.

- **Where it asks**: a card in the session's conversation, or a toast when that conversation is not on screen. It
  names the session, the profile, its account and role, with **Allow for this session** and **Deny**.
- **An approval** lasts 8 hours or until the session ends, and only for the profile as it is now: editing its
  account, role or portal asks again.
- **Denied, or not answered in time**: the command does not run and af-aws-exec exits with status 4. An unanswered request
  stays up for 15 minutes, so you can still allow it and the agent's next run goes through.
- A role chained from such a profile asks for that profile too.
- Such a profile is not written to `~/.aws/config`, so a plain `aws --profile` cannot use it: run it with
  `af-aws-exec`.

**What it does not do.** It catches mistakes and use you would not notice, not an agent that sets out to get around
it:

- An agent can answer the request through the workspace's own interface (the conversation line then says "approved
  without the Console").
- An agent can reuse a login that another profile on the same sign-in keeps open. A profile that stays behind a
  sign-in of its own, logged out after use, still needs your device-code approval every time.

## Model route

Using a profile for claude's Amazon Bedrock model calls (the "Model route" mark in the profile's form) is in
[Amazon Bedrock route](../bedrock-route.md).

## A read-only lookup with `aws --profile`

Deployments, writes, build tools, SDK programs and scripts stay with `af-aws-exec`, because it does what a plain
`aws --profile` does not:

- checks the account (`--account`);
- hands short-lived credentials to tools whose SDK cannot read an SSO profile (older SDKs such as the AWS SDK for
  Java v1, common in Gradle/Maven plugins);
- asks you in the Console when a login is missing;
- refuses a profile name that means different identities to different tools;
- pins the region with `--region` and removes endpoint overrides.

A quick look-up with the AWS CLI itself (`describe-*`, `list-*`, `s3 ls`) may name one of your Settings profiles
directly, but only in a shell where this check prints `isolated`. It prints one word and nothing of the environment
or of the error:

```sh
if err=$(env -u AWS_PROFILE -u AWS_DEFAULT_PROFILE AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true \
           aws sts get-caller-identity 2>&1 >/dev/null); then st=0; else st=$?; fi
err=${err#"${err%%[![:space:]]*}"}
if [ "$st" = 253 ] && [ "$(printenv AWS_EC2_METADATA_DISABLED)" = true ] && [ "${AF_WS_WORKLOAD_AWS:-}" != 1 ] \
   && [ -z "$(env | cut -d= -f1 | grep -E '^AWS_(CONTAINER_|CONFIG_FILE$|SHARED_CREDENTIALS_FILE$|ENDPOINT_URL)')" ] \
   && case $err in "Unable to locate credentials"* | \
        "aws: [ERROR]: An error occurred (NoCredentials): Unable to locate credentials"*) true ;; *) false ;; esac
then echo isolated; else echo not-isolated; fi; unset err st
```

**`isolated`** means all of these hold, so a misspelled or logged-out profile fails instead of answering for another
account:

- `AWS_EC2_METADATA_DISABLED=true` is exported, so the CLI does not ask instance metadata;
- the environment holds no workload credentials and no AWS config-file or endpoint overrides;
- the CLI's default chain (with `AWS_PROFILE` set aside and configured endpoints ignored) ended in its own "no
  credentials" error (exit status 253).

**`not-isolated`** is anything else: your own default credentials, an expired session, a failing
`credential_process`, a network error. A workspace on your own machine, a deployment that hands the task role back and
a workspace not started again since the upgrade normally print it too. Where it prints `not-isolated`, run lookups
through `af-aws-exec` too.

The check reads what this shell would hand the CLI; it does not prove the runtime, the version or the host's network
block. Where it prints `isolated`, a direct lookup still needs **all** of these, or it goes through `af-aws-exec`:

- The profile is one `af-aws-exec --list` shows as exported, chosen there by its account and role (not by the name
  alone). If you do not know which account, find out first.
  - When `--list` warns that its names come "from an earlier sync; not checked against Settings now", nothing is
    verified: use `af-aws-exec`.
  - A profile `--list` does not show, marks as not exported, or one you defined yourself (`role_arn`,
    `credential_process`): use `af-aws-exec`.
- Name the region and ignore configured endpoints, with no `--endpoint-url`:
  `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true aws --profile <name> --region <region> ec2 describe-instances`.
  A stale `AWS_REGION` in your shell beats the profile's region, and an `endpoint_url` in your `~/.aws/config` (left
  by an emulator setup, say) would send the request somewhere other than AWS.
- Run it in the same shell you checked.
- An SSO token error means the login is missing. Plain `aws --profile` does not ask the Console: rerun with
  `af-aws-exec`, which does.
- A profile `af-aws-exec` refused is refused here too; plain `--profile` is no way around it.

Agents in the workspace follow the same rule.

## What the command receives

- The profile's **short-lived** credentials, through that one command's environment only. `af-aws-exec` itself writes
  them nowhere and prints nothing but the identity the command runs as. (The AWS CLI keeps its own login and role
  caches under `~/.aws`, as it always does.)
- **No way to the workload role.** If the login is missing or expired, the command fails instead of falling back.
  Inside a workspace, at a terminal too, it asks you in the Console ([When the login is missing](#when-the-login-is-missing));
  `--login` runs the device-code login in that terminal instead.
- **Credentials last** as long as the SSO role session, or the assumed role's session (often one hour). A longer
  command fails when they expire rather than switching identity.

## Settings profiles it accepts

A Settings profile must have an **account and role**. The credentials are obtained through the profile's SSO login
alone (from a minimal config holding only its SSO settings, with endpoint overrides ignored), then checked with AWS to
be a session of that profile's permission-set role in that account.

It is refused when the profile also carries:

| Also set | Why it is refused |
|---|---|
| `role_arn` (even empty), or a `web_identity_token_file` path | The AWS CLI would then not use the SSO login |
| `source_profile`, `credential_source`, `credential_process`, static keys (in `~/.aws/config` or `~/.aws/credentials`), or an empty `web_identity_token_file` (next to `role_arn` or not) | The AWS CLI would still use SSO, but other SDKs and tools use those first, so the one name would mean different identities to different tools |

- Keys under a `[DEFAULT]` section count, since the CLI applies them to every profile, and so does an empty value.
- A tool that syncs credentials into `~/.aws/credentials` under the same name (yawsso, for example) causes this: sync
  to another name.
- `--profile default` is refused: name the SSO profile.
- A name that means two things is refused: two Settings labels that map to it, or your own `~/.aws` definition of a
  Settings profile's name with a different account, role or sign-in portal.

## Profiles you defined yourself

Some accounts are reached only through a profile of your own in `~/.aws`: one that assumes a role from a
`source_profile` (`role_arn` + `source_profile`), or one with a `credential_process`. `af-aws-exec` runs such a profile
when you name its account with `--account`, which is required here:

```sh
af-aws-exec --profile deploy-target --account <id> --region <region> -- ./deploy.sh
```

**How it runs.** The AWS CLI resolves the profile from your own files, with its own ways to the workload role (the
container credentials variables, IMDS) and endpoint overrides switched off. The command runs only if AWS reports the
credentials in that account (and, for a role, as a session of that role).

- A `credential_process` is your own program and runs as you wrote it: `af-aws-exec` checks the account of what it
  returns, not where it came from. When it fails, its output is not shown; run it yourself to see why.
- Only temporary credentials are passed. A profile that resolves to long-lived keys is refused: use the keys to
  assume a role instead.
- If the chain ends in an SSO profile whose login is missing, the command exits with code 3 and the `aws sso login`
  command for that SSO profile (at a terminal it starts the login itself). The Console is not asked.
- These profiles do not appear in `af-aws-exec --list`, and they cannot share a name with a Settings profile.
- The source keys stay in your `~/.aws` files as before; `af-aws-exec` never hands them to the command.

**It is refused, before anything is fetched**, when:

- the profile, or any profile in its `source_profile` chain (a `[DEFAULT]` section included), sets
  `credential_source` (that takes the workspace's own credentials), `web_identity_token_file`, `login_session` or
  `mfa_serial` (nobody can answer the MFA prompt when an agent runs the command);
- a profile in the chain names more than one way to get credentials, for example keys (even only a session token, or
  keys in `~/.aws/config`) beside a `credential_process` or on a role profile: the AWS CLI and other SDKs would not
  agree on which to use;
- the SSO profile the chain ends in is incomplete, or the chain is broken.

Keep one way per profile: keys go in a profile of their own, named as `source_profile`. The one exception is a role
profile you name that is its own `source_profile` and holds the keys itself.

## When the login is missing

When a command needs the login (an agent's, or yours in a shell session's terminal), it asks you in the Console. This
covers your Settings profiles; for a profile you defined yourself, or with `--no-login`, the command exits with code 3
and the login command to run in a terminal.

A toast at the bottom of the screen names the profile, its account and role from Settings, and the session that asks.
It counts down until the request is withdrawn: 15 minutes after a command last asked (no countdown while a sign-in for
it runs).

1. Press **Log in** on the toast to open the login window, and check what it is for.
2. Press **Log in** there. Only then is a sign-in code created, and only that window shows it.
3. Check the code, press **Sign in and approve**, and approve it on the page that opens.

The agent's command waits about a minute and a half (a few seconds when it is not run by an agent session, such as a
script in a shell session) and continues once you approve. If it has given up by then, the agent runs it again.

| On the toast | What happens |
|---|---|
| **Close** | Keeps the request: it stays in the Console on your other devices too. On a device whose browser cannot sign in, close it and press **Log in** in the Console on another one |
| **Cancel the request** | For a login you do not want: withdraws the request, and for about a minute that profile is not asked for again |
| Closing the toast | Only hides it in that tab |

**Log in** on the profile's row in Settings works at any time, also during that minute.

**At your own terminal** (a shell or SSM session):

- The command prints that it asked the Console and waits up to ten minutes, as long as the device code lives, for you
  to approve there, then carries on without a rerun.
- **Ctrl-C** stops waiting: the command exits with code 3 and the request stays in the Console.
- `--login` runs the device-code login in that terminal instead (it prints the URL and code there). That is also what
  happens when the Console cannot be asked at all, and for the profiles the Console does not cover.
- Outside a workspace, the terminal login starts at a terminal, and the command exits with code 3
  without one.

## The config the command gets

The command gets an AWS config that defines **only the profile you chose** (it hands back the same short-lived
credentials), no credentials file, and no `AWS_ENDPOINT_URL*` overrides.

| The tool inside the command | Result |
|---|---|
| Names no profile, or the same profile | Works |
| Names a different one (Terraform's `profile = "staging"`, `cdk deploy --profile staging`, `AWS_PROFILE=staging` in a script) | Fails with "The config profile (staging) could not be found", instead of quietly running as that other profile |
| Replaces `AWS_ACCESS_KEY_ID` (after an `assume-role`, say) | The chosen profile stops resolving there rather than quietly meaning the new account: use the new credentials without `--profile` |

- **When you see "could not be found"**, remove that profile setting from the tool, or run it under that profile
  (`af-aws-exec --profile staging …`).
- **Do not add `--keep-aws-config` to get past it**: that hands the tool your own `~/.aws` files and endpoint settings
  again, and it would then run as the profile it names. Keep `--keep-aws-config` for tools that need other settings
  from those files.
- If `af-aws-exec` cannot keep that config private (a home directory other users can write to, for example), it gives
  the command an empty AWS config instead, with a warning. The command stays isolated; only a tool naming the same
  profile will not find it.

## Region

1. `--region`, if you give it
2. otherwise a region already exported in your shell: `AWS_REGION`, then `AWS_DEFAULT_REGION`
3. otherwise the profile's

The command gets it in both `AWS_REGION` and `AWS_DEFAULT_REGION`, and the "running as" line shows it. A stale
`AWS_REGION` in your shell beats the profile's region, so give `--region` for deployments.

## `--account`

`--account <id>` refuses to run unless the profile is that AWS account. Put it in scripts, runbooks and agent
instructions for anything that deploys, so a wrong profile name stops before anything happens. For a profile that is
**not** one of your Settings profiles (one you defined yourself), `--account` is required.
