---
name: af-aws
description: "Agent Fleet workspace: running AWS commands as the user - the workload-role trap, choosing the user's Settings > SSM profile with af-aws-exec --list, af-aws-exec --profile/--account/--region, what exit 3 and each refusal mean and who fixes them, the SSO device-code login the user has to approve, and why --keep-aws-config is never the fix. Read before any aws CLI, SDK, Terraform, CDK or deploy command that must act as the user, or when one fails with an SSO token error, 'could not be found', or af-aws-exec exit 3."
user-invocable: false
---
# AWS as the user: `af-aws-exec`

Read when: a command has to act on the user's AWS accounts (deploys, writes, anything whose
principal matters), or an AWS command failed with an SSO/token error, "The config profile (X)
could not be found", or `af-aws-exec` exited 3. The member-side explanation is
`member/10-integrations.md` in the user guide (section "Using the profiles from the terminal, SDKs
and build tools").

## The trap

The container can have an AWS identity of its own (a workload role). A bare `aws …`, an SDK or a
build tool that relies on the default credential chain — no profile, a misspelled one, an expired
login — can run as that role instead of as the user, in a different account, with no error.
Whatever must be done as the user goes through `af-aws-exec`.

## Choose the profile

```sh
af-aws-exec --list
```

prints each of the user's Settings > SSM profiles as `name  account  role  ("label")`, and names
the ones that are not exported (the user's own definition wins, a name two labels share, a label
`default`). Choose by account and role, not by the name alone. If the task does not say which
account, ask the user — do not guess from a name like `prod`.

## Run

```sh
af-aws-exec --profile <name> --account <id> [--region <region>] -- <command> [args...]
```

- Always pass `--account` when you know the account (the user named it, a runbook states it). It is
  required for a profile that is not one of the Settings profiles.
- Pass `--region` for deploys. Without it a region already exported in the shell (`AWS_REGION`, then
  `AWS_DEFAULT_REGION`) wins over the profile's, and a stale one is how commands land in the wrong
  region. The "running as … in region …" line on stderr shows principal and region; check it.
- The command gets the profile's short-lived credentials in its environment, an AWS config that
  defines only that profile, no credentials file, no `AWS_ENDPOINT_URL*`. Credentials last as long as
  the SSO role session (often one hour); a longer command fails rather than switching identity.

## When it stops

| What you see | Meaning | What to do |
|---|---|---|
| exit 3, "SSO login required … log in with: aws sso login --profile '<name>' --use-device-code --no-browser" | The user's SSO login is missing or expired. | You cannot log in for the user. Give them that exact command to run in their own terminal (they approve a device code in their browser — only one they started themselves), wait for them to say it is done, then rerun. Never run `aws sso login` or `af-aws-exec --login` in your own shell: it waits for an approval nobody sees. |
| "is ambiguous: Settings labels … all map to it" | Two Settings profiles share the name. | Tell the user; they rename one in Settings > SSM. |
| "in your own AWS config is …, but the Settings profile … is …; rename one" | The user's `~/.aws` defines the name differently. | Tell the user; do not pick one for them. |
| "is not one of your Settings profiles; name the account … with --account" | A profile the user defined themselves. | Rerun with `--account` if you know the account; otherwise ask. |
| "is account X, not the Y given with --account" | Wrong profile for this account. | Stop. Recheck `--list`; ask the user. |
| "not defined in …" / "not an SSO profile" / "has no SSO account and role" / "the AWS CLI cannot read …" | The profile or the file is not usable as is. | Report the message; the user fixes Settings or the file. |
| "af-aws-exec: … not private …; the command gets an empty AWS config instead" | A warning; the run continues, still isolated. | Pass the `chmod go-w …` hint in the message on to the user. |

Inside the command:

- **"The config profile (X) could not be found"** — the tool names a profile of its own (Terraform
  `profile = "X"`, `cdk --profile X`, `AWS_PROFILE=X` in a script). Remove that setting, or run the
  step under X with its own `af-aws-exec`. **Never add `--keep-aws-config` to get past it**: that
  hands the tool the user's `~/.aws` again and it runs as X — exactly the accident this prevents.
- **"AWS_ACCESS_KEY_ID was changed after af-aws-exec started this command"** — a script swapped in
  other credentials (after `assume-role`, say) and then used `--profile`. Use the new credentials
  without `--profile`, or split the step into its own `af-aws-exec`.

## Never

- Run a user-identity action with bare `aws` / an SDK / a build tool outside `af-aws-exec`, or retry
  that way after `af-aws-exec` refused.
- Use `--keep-aws-config` as a workaround. It exists for tools that genuinely need other settings
  from the user's `~/.aws` files, and only the user decides that.
- Write the credentials anywhere, echo them, or paste `env` output (it holds them and `AF_*` secrets).
- Edit the managed block in `~/.aws/config` (between the `# agent-fleet` markers); it is rewritten
  from Settings.
