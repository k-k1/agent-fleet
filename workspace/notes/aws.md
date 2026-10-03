---
name: af-aws
description: "Agent Fleet workspace: running AWS commands as the user - the workload-role trap and what 'Unable to locate credentials' means, when a read-only lookup may use aws --profile instead (the isolation self-check), choosing the user's Settings > AWS profiles/SSM profile with af-aws-exec --list, af-aws-exec --profile/--account/--region, what exit 3 and each refusal mean and who fixes them, the SSO device-code login the user has to approve, and why --keep-aws-config is never the fix. Read before any aws CLI, SDK, Terraform, CDK or deploy command about the user's AWS accounts or resources (reads included), or when one fails with 'Unable to locate credentials', an SSO token error, 'could not be found', or af-aws-exec exit 3."
user-invocable: false
---
# AWS as the user: `af-aws-exec`

Read when: a command touches the user's AWS accounts or resources (deploys, writes, and reads
too — anything whose account matters), or an AWS command failed with "Unable to locate
credentials", an SSO/token error, "The config profile (X) could not be found", or `af-aws-exec`
exited 3. The member-side explanation is `member/10-integrations.md` in the user guide (section
"Using the profiles from the terminal, SDKs and build tools").

## The trap

A command that names **no profile at all** — a bare `aws …`, an SDK's default credential chain, a
build tool with no profile setting — is not the user. In a container workspace (docker or ECS,
Agent Fleet 0.26.0 or later) the Agent and the Control Plane keep the workspace's own role out of
sessions and terminals: no `AWS_CONTAINER_CREDENTIALS_*`, and `AWS_EC2_METADATA_DISABLED=true` so
the CLI and the SDKs do not ask the host's instance metadata. Such a command normally stops with
"Unable to locate credentials" / "Unable to load AWS credentials from any provider in the chain".
That error means "run it as the user with `af-aws-exec`", never "configure credentials": do not run
`aws configure` or `aws login` (the CLI's own hint), do not reach for keys in the user's `~/.aws` (a
`[default]` section included), do not read credentials out of `/proc`, the metadata endpoints or
another process, and do not unset `AWS_EC2_METADATA_DISABLED`.

The variable only stops tools that honour it. The network block behind it is the operator's: it
holds once they have finished the 0.26.0 migration (retained ecs-ec2 slots replaced, existing
ec2-single instances moved to hop limit 1 — `operate/04-secure.md`, "Other operational controls",
in the user guide). Until then, a tool that ignores the variable can still reach the host's
instance role, so nothing you see in the shell proves the boundary.

That isolation is not everywhere. A deployment can hand the ECS task role back
(`AF_WS_WORKLOAD_AWS=1`, with which nothing sets `AWS_EC2_METADATA_DISABLED`); a workspace running
directly on the user's own machine (the native runtime) keeps that machine's credentials and
instance role; a workspace not started again since its deployment moved to 0.26.0 has neither
setting. There the same command runs as that role instead of as the user, in a different account,
with no error — reads included: "how many instances does prod have" answered by a bare
`aws ec2 describe-instances` is an answer about the wrong account. (A named profile that is
misspelled or logged out fails loudly instead.)

## `af-aws-exec`, or `aws --profile` for a lookup

Deploys, writes, and anything whose account matters (a build tool, Terraform, CDK, a script, an
SDK program) go through `af-aws-exec --profile <name> --account <id>`, everywhere. It does what a
plain `--profile` cannot:

- `--account` refuses to run unless AWS reports the credentials in that account, and the
  "running as … in region …" line shows who the command is.
- The command gets the profile's short-lived credentials in its environment, so a tool whose SDK
  cannot read an SSO profile (older SDKs such as the AWS SDK for Java v1, common in Gradle/Maven
  plugins) works.
- A missing SSO login is requested in the user's Console (exit 3 while it waits, below), instead of
  an SSO token error you cannot fix.
- It refuses a profile name that means different identities to different tools (keys or a role
  beside the SSO settings, a conflicting definition in `~/.aws`).
- `--region` pins the region against a stale `AWS_REGION` in the shell, and `AWS_ENDPOINT_URL*`
  overrides are removed.

A **read-only lookup** with the AWS CLI itself (`describe-*`, `list-*`, `get-*`, `s3 ls`; not an
SDK program, a build tool or a script) may instead name the profile directly, but only where this
shell passes the check below. It prints one word and nothing of the environment or of the error:

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

`isolated` means: `AWS_EC2_METADATA_DISABLED=true` is exported (a shell variable the CLI does not
inherit does not count), so the CLI does not ask instance metadata, no workload credentials or file and
endpoint overrides are in the environment, and the CLI's default chain (with `AWS_PROFILE` set
aside and configured endpoints ignored) ended in its own "no credentials" error, exit 253. Any
other outcome — default credentials that resolve, an expired session, a failing
`credential_process` (even one whose message says "Unable to locate credentials"), a network or
endpoint error — is `not-isolated`. It works under `set -e` and `pipefail`. It checks what the
CLI in this shell would inherit, not the runtime, the version or the host's network block: a shell
where someone pre-set the variable can pass on the native runtime too. Treat `not-isolated` as the
answer whenever you are unsure, and then every AWS command, reads included, goes through
`af-aws-exec`.

Even where it is `isolated`, all of these hold or you use `af-aws-exec`:

- `<name>` is a Settings profile `af-aws-exec --list` shows as exported, chosen there by account and
  role. When `--list` warns that the names come "from an earlier sync; not checked against
  Settings now", nothing is verified → `af-aws-exec`. The task does not say which account → ask the user. Not in `--list`, marked not exported, or
  a profile of the user's own (a `role_arn` / `credential_process` profile always needs
  `af-aws-exec --account`) → `af-aws-exec`.
- Always pass `--region <region>`: a stale `AWS_REGION` / `AWS_DEFAULT_REGION` in the shell beats
  the profile's region.
- Run it as
  `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true aws --profile <name> --region <region> <service> <read-only operation> …`,
  with no `--endpoint-url`, in the same shell you checked. The variable keeps an `endpoint_url` in
  the user's `~/.aws/config` (a `[default]` or `services` section an emulator set up, say) from
  sending the request somewhere other than AWS.
- An SSO token error means the login is missing: rerun the lookup with `af-aws-exec`, which asks the
  user in the Console. Do not run `aws sso login` yourself.
- If `af-aws-exec` refused that profile, plain `--profile` is not a way around it.

Worked example — a build tool with an S3/deploy plugin. `./gradlew uploadArtifact` (an S3 upload
task with no profile in `build.gradle`) fails with "Unable to load AWS credentials from any provider
in the chain". The fix is
`af-aws-exec --profile <name> --account <id> --region <region> -- ./gradlew uploadArtifact`; the
plugin's default chain picks the profile's credentials up from the environment. If the build script
names a profile of its own, see "could not be found" below.

## Choose the profile

```sh
af-aws-exec --list
```

prints each of the user's Settings > AWS profiles/SSM profiles as `name  account  role  ("label")`, and names
the ones that are not exported (the user's own definition wins, a name two labels share, a label
`default`). Choose by account and role, not by the name alone. If the task does not say which
account, ask the user — do not guess from a name like `prod`.

Some deployments are not reached through SSO at all: a profile in the user's own `~/.aws` that
assumes a role from a `source_profile` (or runs a `credential_process`). `--list` does not show
those. Use one only when the user or a runbook names it, and always with `--account`; the same
`af-aws-exec` runs it (below). It is never a reason to fall back to bare `aws --profile`.

## Run

```sh
af-aws-exec --profile <name> --account <id> [--region <region>] -- <command> [args...]
```

- Always pass `--account` when you know the account (the user named it, a runbook states it). It is
  required for a profile that is not one of the Settings profiles, including every `role_arn` /
  `credential_process` profile: the command runs only if AWS reports its credentials in that account.
- Pass `--region` for deploys. Without it a region already exported in the shell (`AWS_REGION`, then
  `AWS_DEFAULT_REGION`) wins over the profile's, and a stale one is how commands land in the wrong
  region. The "running as … in region …" line on stderr shows principal and region; check it.
- The command gets the profile's short-lived credentials in its environment, an AWS config that
  defines only that profile, no credentials file, no `AWS_ENDPOINT_URL*`. Credentials last as long as
  the SSO role session, or the assumed role's session (often one hour); a longer command fails rather
  than switching identity.

## When it stops

| What you see | Meaning | What to do |
|---|---|---|
| "SSO login for profile '<name>' requested in the Agent Fleet Console; waiting up to …" (stderr), then the command runs | The login was missing; the user was asked in the Console and approved it while the run waited. | Nothing: the command ran as usual. |
| exit 3, "… the login was requested in the Agent Fleet Console and is waiting for the member to approve it there" | The user has not approved yet. A toast in their Console asks them, and the request stays up after your run ends. | Tell the user a login for that profile (name it) is waiting in the Console and to press "Log in" there. Rerun once they say it is done. Do not ask them to open a terminal. |
| exit 3, "… the login request was cancelled in the Agent Fleet Console; ask the member; they can press "Log in" on the profile in Settings > AWS profiles/SSM, or log in in a terminal with: aws sso login …" | Someone cancelled the request in the Console; for about a minute no new request is shown for that profile. | Ask the user whether to go on. If yes, they press "Log in" on that profile's row in Settings > AWS profiles/SSM (the hold does not stop it), or run the printed command in their own terminal (in Claude Code: `!` at the prompt). Rerun once they say it is done. |
| exit 3, "SSO login required … log in with: aws sso login --profile '<name>' --use-device-code --no-browser" | The login is missing, expired or invalid and the Console could not be asked (a profile that is not a Settings profile, `--no-login`, or outside a workspace). Exit 3 is only ever a login. | You cannot log in for the user. For a Settings profile (`--list` shows it), they can press "Log in" on its row in Settings > AWS profiles/SSM. Otherwise give them that exact command to run in their own terminal — in a Claude Code session they can type it at the prompt with a leading `!`, or run it in a Workspace terminal if that times out before they approve — and they approve a device code in their browser (only one they started themselves). Wait for them to say it is done, then rerun. |
| exit 1, "could not get credentials for profile …: <AWS error>" | Not a login problem: access denied, a broken CLI, a network error. | Report the AWS error to the user as it is; do not ask them to log in. |
| "is ambiguous: Settings labels … all map to it" | Two Settings profiles share the name. | Tell the user; they rename one in Settings > AWS profiles/SSM. |
| "in your own AWS config is …, but the Settings profile … is …; rename one" | The user's `~/.aws` defines the name differently. | Tell the user; do not pick one for them. |
| "is not one of your Settings profiles; name the account … with --account" | A profile the user defined themselves. | Rerun with `--account` if you know the account; otherwise ask. |
| "is account X, not the Y given with --account" | Wrong profile for this account. | Stop. Recheck `--list`; ask the user. |
| "is not an SSO profile; af-aws-exec runs a role or credential_process profile only with --account" | The user's own assume-role or `credential_process` profile, run without `--account`. | Rerun with `--account` if the user or a runbook named the account (the message shows the role's account; that alone is not the user naming it); otherwise ask. |
| "assumes a role in account X, not the Y given with --account" / "resolved to …, which is account X, not the Y given with --account" / "not a session of its role" | The profile is not the account you were told. | Stop. Ask the user; do not switch `--account` to match. |
| "sets credential_source" / "sets web_identity_token_file" / "sets login_session" / "sets mfa_serial" / "without a source_profile" / "names itself as source_profile" / "loops back" / "names source_profile …, which is not defined" / "not an IAM role ARN" | The role chain would use the workspace's own credentials, needs a prompt nobody can answer, or is broken. | Report the message; the user fixes the profile. Never run it with bare `aws` instead. |
| "names more than one way to get credentials (…)" / "has no credentials of its own" / "has incomplete SSO settings" | A profile in the chain has two credential sources (keys beside a `credential_process` or a role, SSO settings beside a process, …), none, or a half-filled SSO profile. | Report it; the user keeps one way per profile (keys in a profile of their own, named as `source_profile`). |
| "a credential_process in the chain of profile … failed; its output is not shown" | The user's credential program failed; its output is withheld because it may hold anything. | Report it; the user runs the process themselves to see why. Do not run it yourself to read its output. |
| "resolves to long-lived keys" / "not an SSO profile in the AWS config, nor a role_arn or credential_process profile" | Static keys only; `af-aws-exec` never hands them to a command. | Report it; the user sets up a role to assume from those keys (or an SSO profile). Never use the keys with bare `aws`. |
| "SSO login required for profile … (the SSO profile '<src>' its source_profile chain ends in) … log in with: aws sso login --profile '<src>' …" (exit 3) | A role assumed from an SSO profile whose login is missing; the Console is not asked for these. | Give the user that exact command for their own terminal, as for the exit 3 row above. |
| "mixes sso_* settings with role_arn or credential_process" | One profile is both. | Report it; the user splits it into two profiles. |
| "not defined in …" / "not an SSO profile" / "has no SSO account and role" / "the AWS CLI cannot read …" / "sso_session is empty" / "it sets <key> … but its sso-session …" / "… are only in [sso-session …]" | The profile or the file is not usable as is. | Report the message; the user fixes Settings or the file. |
| "not exported: no account and role in Settings" / "Settings has an account but no role" / "… a role but no account" (in `--list` or from a run) | A Settings profile without both. With neither, `aws --profile` would run as the workspace's own role, so it is never exported. | Report it; the user sets both in Settings > AWS profiles/SSM. Never use that name with bare `aws`. |
| "your [sso-session af-<name>] in ~/.aws/config uses the name this profile's sso-session needs" | The user's own sso-session section blocks the export. | Report it; the user renames that section. |
| "not exported: [DEFAULT] <line> (in ~/.aws/config)" (in `--list`, or from a run) | A `[DEFAULT]` line would make the AWS CLI refuse that Settings profile, or run it as another role. | Report it; the user removes that line from `[DEFAULT]`. |
| "also sets <key>" | The profile carries a setting that another credential source uses (in the CLI, or in other SDKs), so the name could mean another identity. | Report it; the user removes that setting from the profile (or points a credential-sync tool such as yawsso at another name). Do not work around it. |
| "has a region that spans several lines; fix it or pass --region" | The profile's region is not one value. | Pass `--region` if the task says which region; otherwise ask, and report the broken line. |
| "af-aws-exec: … not private …; the command gets an empty AWS config instead" | A warning; the run continues, still isolated. | Pass the `chmod go-w …` hint in the message on to the user. |

Inside the command:

- **"The config profile (X) could not be found"** — the tool names a profile of its own (Terraform
  `profile = "X"`, `cdk --profile X`, `AWS_PROFILE=X` in a script). Run that step under X with its
  own `af-aws-exec` (check X's account in `--list` first). Removing the setting from the project is
  a change to the user's code: only with their agreement. **Never add `--keep-aws-config` to get
  past it**: that hands the tool the user's `~/.aws` again and it runs as X — exactly the accident
  this prevents.
- **"AWS_ACCESS_KEY_ID was changed after af-aws-exec started this command"** — a script swapped in
  other credentials (after `assume-role`, say) and then used `--profile`. Use the new credentials
  without `--profile`, or split the step into its own `af-aws-exec`.

The Console login never shows a code the user did not start: the code is created only when they
press "Log in" in the Console (in the toast's dialog, or on the profile's row in Settings > AWS
profiles/SSM), and only that tab shows it. **Never run `aws sso login` or
`af-aws-exec --login` in your own shell, and never pass a login URL or code to the user**: it waits
for an approval nobody sees, and a code from you is exactly what the user is told never to approve.

## A local emulator is not the user's account

To try AWS code without an account, the user may run MiniStack (an AWS API emulator) in the
workspace; the setup is `member/10-integrations.md`, "Trying AWS code against a local emulator
(MiniStack)". Reach it only through a dedicated profile that carries its `endpoint_url` and dummy
keys (`aws --profile ministack …`), never by exporting dummy keys: a command that then forgets the
endpoint reaches real AWS instead of stopping. It never goes through `af-aws-exec`. Its RDS reports a
database that does not exist; use `af-db` for Postgres.

## Never

- Run a user-identity action with bare `aws` / an SDK / a build tool outside `af-aws-exec`, or retry
  that way after `af-aws-exec` refused — also not for a profile that is not SSO: `af-aws-exec`
  runs those with `--account`. The one exception is a read-only
  `aws --profile <Settings profile> --region <region>` lookup, under every condition above.
- Answer "Unable to locate credentials" by configuring credentials (`aws configure`, `aws login`, the user's
  `[default]` keys, `AWS_ACCESS_KEY_ID` in the shell): name the user's profile with `af-aws-exec`.
- Use `--keep-aws-config` as a workaround. It exists for tools that genuinely need other settings
  from the user's `~/.aws` files, and only the user decides that.
- Write the credentials anywhere, echo them, or paste `env` output (it holds them and `AF_*` secrets).
- Edit the managed block in `~/.aws/config` (between the `# agent-fleet` markers); it is rewritten
  from Settings.
