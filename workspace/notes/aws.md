---
name: af-aws
description: "Agent Fleet workspace: running AWS commands as the user - the workload-role trap, choosing the user's Settings > SSM profile with af-aws-exec --list, af-aws-exec --profile/--account/--region, what exit 3 and each refusal mean and who fixes them, the SSO device-code login the user has to approve, and why --keep-aws-config is never the fix. Read before any aws CLI, SDK, Terraform, CDK or deploy command about the user's AWS accounts or resources (reads included), or when one fails with an SSO token error, 'could not be found', or af-aws-exec exit 3."
user-invocable: false
---
# AWS as the user: `af-aws-exec`

Read when: a command touches the user's AWS accounts or resources (deploys, writes, and reads
too — anything whose account matters), or an AWS command failed with an SSO/token error, "The config profile (X)
could not be found", or `af-aws-exec` exited 3. The member-side explanation is
`member/10-integrations.md` in the user guide (section "Using the profiles from the terminal, SDKs
and build tools").

## The trap

The container can have an AWS identity of its own (a workload role). A command that names **no
profile at all** — a bare `aws …`, an SDK's default credential chain, a build tool with no profile
setting — runs as that role instead of as the user, in a different account, with no error. That
includes read-only lookups: "how many instances does prod have" answered by a bare
`aws ec2 describe-instances` is an answer about the wrong account. (A named profile that is
misspelled or logged out fails loudly instead.) Anything about the user's accounts or resources,
reads included, goes through `af-aws-exec`.

## Choose the profile

```sh
af-aws-exec --list
```

prints each of the user's Settings > SSM profiles as `name  account  role  ("label")`, and names
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
| exit 3, "… the login request was cancelled in the Agent Fleet Console; ask the member, or log in in a terminal with: aws sso login …" | Someone cancelled the request in the Console; for about a minute no new request is shown for that profile. | Ask the user whether to go on. If yes, give them the printed command for their own terminal (in Claude Code: `!` at the prompt). |
| exit 3, "SSO login required … log in with: aws sso login --profile '<name>' --use-device-code --no-browser" | The login is missing, expired or invalid and the Console could not be asked (a profile that is not a Settings profile, `--no-login`, or outside a workspace). Exit 3 is only ever a login. | You cannot log in for the user. Give them that exact command to run in their own terminal — in a Claude Code session they can type it at the prompt with a leading `!`, or run it in a Workspace terminal if that times out before they approve — and they approve a device code in their browser (only one they started themselves). Wait for them to say it is done, then rerun. |
| exit 1, "could not get credentials for profile …: <AWS error>" | Not a login problem: access denied, a broken CLI, a network error. | Report the AWS error to the user as it is; do not ask them to log in. |
| "is ambiguous: Settings labels … all map to it" | Two Settings profiles share the name. | Tell the user; they rename one in Settings > SSM. |
| "in your own AWS config is …, but the Settings profile … is …; rename one" | The user's `~/.aws` defines the name differently. | Tell the user; do not pick one for them. |
| "is not one of your Settings profiles; name the account … with --account" | A profile the user defined themselves. | Rerun with `--account` if you know the account; otherwise ask. |
| "is account X, not the Y given with --account" | Wrong profile for this account. | Stop. Recheck `--list`; ask the user. |
| "is not an SSO profile; af-aws-exec runs a role or credential_process profile only with --account" | The user's own assume-role or `credential_process` profile, run without `--account`. | Rerun with `--account` if the user or a runbook named the account (the message shows the role's account; that alone is not the user naming it); otherwise ask. |
| "assumes a role in account X, not the Y given with --account" / "resolved to …, which is account X, not the Y given with --account" / "not a session of its role" | The profile is not the account you were told. | Stop. Ask the user; do not switch `--account` to match. |
| "sets credential_source" / "sets web_identity_token_file" / "sets mfa_serial" / "without a source_profile" / "names itself as source_profile" / "loops back" / "names source_profile …, which is not defined" / "not an IAM role ARN" | The role chain would use the workspace's own credentials, needs a prompt nobody can answer, or is broken. | Report the message; the user fixes the profile. Never run it with bare `aws` instead. |
| "resolves to long-lived keys" / "not an SSO profile in the AWS config, nor a role_arn or credential_process profile" | Static keys only; `af-aws-exec` never hands them to a command. | Report it; the user sets up a role to assume from those keys (or an SSO profile). Never use the keys with bare `aws`. |
| "SSO login required for profile … (the SSO profile '<src>' its source_profile chain ends in) … log in with: aws sso login --profile '<src>' …" (exit 3) | A role assumed from an SSO profile whose login is missing; the Console is not asked for these. | Give the user that exact command for their own terminal, as for the exit 3 row above. |
| "mixes sso_* settings with role_arn or credential_process" | One profile is both. | Report it; the user splits it into two profiles. |
| "not defined in …" / "not an SSO profile" / "has no SSO account and role" / "the AWS CLI cannot read …" / "sso_session is empty" / "it sets <key> … but its sso-session …" / "… are only in [sso-session …]" | The profile or the file is not usable as is. | Report the message; the user fixes Settings or the file. |
| "not exported: no account and role in Settings" / "Settings has an account but no role" / "… a role but no account" (in `--list` or from a run) | A Settings profile without both. With neither, `aws --profile` would run as the workspace's own role, so it is never exported. | Report it; the user sets both in Settings > SSM. Never use that name with bare `aws`. |
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
press "Log in" in the Console, and only that tab shows it. **Never run `aws sso login` or
`af-aws-exec --login` in your own shell, and never pass a login URL or code to the user**: it waits
for an approval nobody sees, and a code from you is exactly what the user is told never to approve.

## Never

- Run a user-identity action with bare `aws` / an SDK / a build tool outside `af-aws-exec`, or retry
  that way after `af-aws-exec` refused — also not for a profile that is not SSO: `af-aws-exec`
  runs those with `--account`.
- Use `--keep-aws-config` as a workaround. It exists for tools that genuinely need other settings
  from the user's `~/.aws` files, and only the user decides that.
- Write the credentials anywhere, echo them, or paste `env` output (it holds them and `AF_*` secrets).
- Edit the managed block in `~/.aws/config` (between the `# agent-fleet` markers); it is rewritten
  from Settings.
