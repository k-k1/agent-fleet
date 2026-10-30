---
audience: "anyone using AWS or Google Cloud from the workspace"
---

# 16. Cloud accounts — AWS profiles, SSM hosts and Google Cloud

English | [日本語](16-cloud-accounts.ja.md)

## Logging in to another in-house host (SSM)

You can log in to EC2 instances in your company's AWS via AWS SSM Session Manager. Configuration lives in
**⚙ Settings → the "AWS profiles/SSM" tab**, split into **two layers**.

- **Profile (shared settings)**: the access portal (IAM Identity Center) and account/role. A bundle of SSO settings reused across multiple hosts. Create one of these first.
- **SSM host (individual)**: an alias for the login target → instance ID. For authentication you just pick a profile.

Each profile row has **Log in**, which signs you in to IAM Identity Center for that profile without leaving the
Console. It opens a login window; the sign-in code is created only when you press **Log in** there, and only that
window shows it. The login serves SSM sessions and `af-aws-exec` for that profile. The button is off for a profile
without both an account and a role, and for one whose name another label also maps to (see below).
Beside the label, a badge shows the login state: **Signed in**, **Renews on use** (the access token has expired;
while the portal session is open, the next use renews it) or **Not signed in**. It shows no time left: the
workspace knows only the access token's expiry (about an hour), not when the portal session ends.

**Before a login ends: only for a login that cannot renew.** A normal login from Settings renews itself on use
until the portal session ends, and that end is recorded nowhere the workspace can read, so such a login is **not
warned about in advance**: when the portal ends it, the next command asks for a login as below. Only when the cached
login has nothing to renew it with (no refresh token, or its sign-in client registration has expired) is its end
known; then the Console warns once per profile, about 10–15 minutes before: a toast "Your AWS login ends at …" (and
a notification), with **Log in** opening the same login window. The warning goes once you log in again; closing the
toast hides it for that end in that tab. A profile that is not in the managed block (see `af-aws-exec --list`) is
never warned about.

**Logging out of one profile.** A row that is signed in (or renews on use) has **Log out**, both here and in the
popover of the WS bar's AWS badge. After you confirm, the workspace ends that profile's login with AWS and deletes
its cached login and role credentials; profiles that do not follow it stay signed in. If chained profiles follow this
one (see below), the confirmation names them first: they are signed out with it. Credentials a running command already
received stay valid until they expire (AWS cannot recall them), so stop that command if it matters. An expired
access token (a **Renews on use** row) is renewed first so that the logout reaches AWS; if AWS answers that the
login has already ended, the logout is a normal one. If AWS cannot be reached, the renewal fails for any other
reason, AWS refuses the logout itself, or the login has nothing to renew it with, the workspace is signed out all the
same and the Console says so; the login may then stay valid at AWS until it ends. Do not use `aws sso logout` for this: it signs out every profile at once, whatever `--profile` says.

Every profile and host row has **Edit**, which opens the same form filled in and saves it in place. Edit rather
than delete and re-add: a host refers to its profile by an internal ID, so a re-added profile is a new one.
A profile that hosts still use cannot be deleted: the page names those hosts; edit them to pick another
profile, or delete them, first. A host that was left without a profile before this rule (its row says so) is
fixed the same way: edit it and pick a profile.
A profile's workspace name comes from its label, and the login belongs to that name: changing the label, or the
start URL / SSO region, means logging in again: the form warns you, and the row shows **Log in again** until you log in from it. The workspace's `~/.aws/config` picks up
the change within 5 minutes, or at once when you press **Log in**; sessions already open keep the old settings.

**No AWS secrets are stored in Agent Fleet.** Login happens at session start via the device-code flow: you
approve the **`aws sso login`** URL shown in the terminal in your browser, and short-lived credentials are held
only inside the workspace.

Once registered, connect from the workspace action bar via **"Start" → "SSM — log in to another host"**,
choosing the **target host**.
If authentication is needed, the `aws sso login` URL appears on a confirmation screen; approve it in another tab
(never enter a code / URL you don't recognize).

### Importing profiles from ~/.aws/config

Moving from your own machine? **Import from ~/.aws/config** (under the profile list) reads that file and
creates the profiles for you instead of retyping them. Choose the file or paste its contents; it is read **in
your browser only**. What is sent is the settings of the profiles you tick, through the same save as the form.
It works while the workspace is stopped.

The preview lists every profile in the file with what will happen to it:

- **Create (SSO)**: a profile with a portal, SSO region, account and role (`sso_session` style or the older inline
  `sso_*` keys).
- **Create (assume a role)**: `role_arn` + `source_profile` whose source is one of those, in the file or already in
  Settings (only when its portal, SSO region, account and role are the same). When the source is not in the file,
  pick the Settings profile to use; the row then says it is your choice, not something that was checked.
- **Already registered**: the same profile is in Settings; nothing is changed.
- **Not importable**, with the reason: access keys, `credential_process`, `mfa_serial`, `credential_source`,
  `web_identity_token_file`, `login_session`, more than one way to sign in (including keys inherited from
  `[DEFAULT]`), a role assumed from another role, an SSO profile without both account and role, a value Settings
  cannot hold, or a name that is taken or turns into the same AWS profile name as another.

You can untick a row, change its display name (needed for `[default]`: `default` cannot be a Settings name) and
fix a role's session length (900–3600 seconds). Renaming a source keeps the roles that use it attached.

- **Secrets are never imported.** If the text holds access keys, the import says so: do not paste that text
  anywhere else. Ask for `~/.aws/config` only, never `~/.aws/credentials`.
- A file the AWS CLI cannot read (a section or key given twice, a key before any section) is refused with its line
  number.
- **Each Settings profile signs in on its own.** Twenty profiles that shared one `sso-session` on your machine need
  twenty logins here; the preview shows the count (#3014 tracks sharing one login).
- A profile without a region uses its SSO region (a role: its source's); on your machine an environment variable
  may have set it. The preview shows which region each row gets.
- Importing again with the same choices creates nothing new. If a run stops halfway, read the file again: what was
  created shows as already registered and the rest can be imported.

### Profile type: assume a role from another profile

When an account is reached by a role that trusts your IAM Identity Center sign-in, you do not need long-lived keys
in `~/.aws/credentials`. Add a profile of the type **"Assume a role from another profile"** in
Settings > AWS profiles/SSM:

| Field | Meaning |
|---|---|
| Source profile (`sourceProfileId`) | The SSO profile whose sign-in assumes the role. Required. It must be an SSO profile, never another assume-role profile. |
| Role ARN (`roleArn`) | `arn:aws:iam::<account>:role/<name>`. Required. The profile's account is taken from it. |
| External ID (`externalId`), Session name (`sessionName`), Duration (`durationSeconds`, 900-3600: AWS limits a chained role to one hour) | Optional assume-role parameters. |
| Default region (`region`) | Optional. Falls back to the source profile's region. |

Nothing secret is stored or exported. The profile is written into the managed block of `~/.aws/config` as
`role_arn` + `source_profile = <the source's profile name>`, so `aws --profile <name>`, SDKs and `af-aws-exec` use it
like any other Settings profile, and an SSM host can point at it too.

- The target role's trust policy must allow your Identity Center role (`AWSReservedSSO_<permission set>_*`) in the
  source account.
- It has no sign-in of its own. **Log in** on its row signs in to the source profile, and its state follows the source's.
  Its row's **Log out** ends the source's login (a chain has none of its own), so it signs the source out and every chain
  following it; the confirmation names the source and those chains first, and nothing is logged out unless you confirm. In the popover of the WS bar's AWS badge it reads
  **"Follows <source>'s login"**, or **Source unavailable** when the source is missing or not exported. The badge
  counts logins, so its count of signed-in profiles (such as "1/3") leaves chained profiles out.
  `af-aws-exec --profile <name> -- <command>` needs no `--account` (the role's account is checked against what AWS
  reports); when the source's login is missing the Console is asked for it, under the source's name.
- It is exported only while its source is exported. If you define the source's name in your own `~/.aws` files, or a
  `[DEFAULT]` line breaks it, the chained profile is held back too (`af-aws-exec --list` says why), so the chain never
  starts from keys of your own.
- A source profile cannot be deleted, or turned into an assume-role profile, while another profile uses it.
- Settings export carries the chained profile with its source's label. Import adds it only when a profile with
  exactly that label and the same sign-in (start URL, SSO region, account and role) exists or is in the same file;
  otherwise it is skipped.

### Using the profiles from the terminal, SDKs and build tools

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

**Logging in from a terminal.** The profile row's **Log in** (above) does this in the Console. In a terminal, plain
`aws sso login` opens a callback on `127.0.0.1` inside the workspace, which
your browser cannot reach. Use the device-code flow instead:

```sh
aws sso login --profile <name> --use-device-code --no-browser
```

Open the URL it prints and approve the code. Approve only a code you started yourself just now. The login is shared with
SSM sessions of the same profile, so logging in once covers both.

**Running one command as you: `af-aws-exec`.** The workspace can have an AWS identity of its own (a *workload
role*), and the machine underneath can have one too. In a container workspace (docker or AWS ECS, Agent Fleet
0.26.0 or later) your sessions and terminals do not get either: the Agent keeps the workspace's credentials variables
out of everything it starts, and the SDKs' instance metadata lookup is switched off (`AWS_EC2_METADATA_DISABLED=true`).
A tool that ignores that variable is stopped only by the host's network block, which holds once your administrator has
finished the 0.26.0 migration ([operator guide 04](../operate/04-secure.md), "Other operational controls"). (A
workspace that runs directly on your own machine is left as it is: an instance role there is your machine's, and the
SDKs still find it.) So a
command that names no profile at all (a bare `aws …`, an SDK's default credential chain, a build tool with no
profile setting) fails with "Unable to locate credentials" (or its SDK's wording) instead of running as the
workspace. (A named profile that is misspelled or logged out fails with its own error.) Your administrator can let
the workspace use its own task role again (on AWS ECS); then such a command quietly runs as that role, in another
account. Either way, "Unable to locate credentials" means "name your profile", not "configure credentials": do not
answer it with `aws configure`, `aws login` (the CLI's own hint) or keys in a `[default]` section of `~/.aws`, and do not look for credentials elsewhere.
For deployments, writes and anything else whose account matters, pass your credentials explicitly:

```sh
af-aws-exec --profile <name> -- ./gradlew deploy
af-aws-exec --profile <name> -- npx cdk deploy
```

**Example: a build tool with an S3 upload plugin.** A Gradle deploy task built on an AWS plugin (an S3 upload
task, for instance) with no profile in the build script asks the SDK's default chain: environment variables, JVM
system properties, the `default` profile in `~/.aws`, then the container credentials and instance metadata. In a
session, `./gradlew uploadArtifact` therefore stops with "Unable to load AWS credentials from any provider in the
chain" rather than uploading the artifact as the workspace's role. Run it as you, naming the account:

```sh
af-aws-exec --profile <name> --account <id> --region <region> -- ./gradlew uploadArtifact
```

The plugin then finds the profile's short-lived credentials in its environment, the first place the chain looks,
and the "running as" line shows who uploads. If the build script names a profile itself (`profileName = "prod"`,
say), see "could not be found" below.

**A read-only lookup with `aws --profile`.** `af-aws-exec` stays the way to run deployments, writes, build tools,
SDK programs and scripts: it checks the account (`--account`), hands short-lived credentials to tools whose SDK cannot
read an SSO profile (older SDKs such as the AWS SDK for Java v1, common in Gradle/Maven plugins), asks you in the
Console when a login is missing, refuses a profile name that means different identities to different tools, pins the
region with `--region` and removes endpoint overrides. A quick look-up with the AWS CLI itself (`describe-*`,
`list-*`, `s3 ls`) may name one of your Settings profiles directly, but only in a shell where this check prints
`isolated`. It prints one word and nothing of the environment or of the error:

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

`isolated` means `AWS_EC2_METADATA_DISABLED=true` is exported, so the CLI does not ask instance metadata, the environment holds no workload credentials and no AWS
config-file or endpoint overrides, and the CLI's default chain (with `AWS_PROFILE` set aside and configured endpoints
ignored) ended in its own "no credentials" error (exit 253), so a misspelled or logged-out profile fails instead of answering for another account. Anything else (your
own default credentials, an expired session, a failing `credential_process`, a network error) prints `not-isolated`, and so normally do a workspace
on your own machine, a deployment that hands the task role back and a workspace not started again since the upgrade.
The check reads what this shell would hand the CLI; it does not prove the runtime, the version or the host's network
block. Where it prints `not-isolated`, run lookups through `af-aws-exec` too. Where it prints `isolated`, a direct
lookup still needs all of these, or it goes through `af-aws-exec`:

- The profile is one `af-aws-exec --list` shows as exported, chosen there by its account and role (not by the name
  alone). If you do not know which account, find out first. When `--list` warns that its names come "from an earlier
  sync; not checked against Settings now", nothing is verified: use `af-aws-exec`. A profile `--list` does not show, marks as not exported,
  or one you defined yourself (`role_arn`, `credential_process`) → `af-aws-exec`.
- Name the region and ignore configured endpoints:
  `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true aws --profile <name> --region <region> ec2 describe-instances`, with no
  `--endpoint-url`. A stale `AWS_REGION` in your shell beats the profile's region, and an `endpoint_url` in your
  `~/.aws/config` (left by an emulator setup, say) would send the request somewhere other than AWS.
- Run it in the same shell you checked.
- An SSO token error means the login is missing; plain `aws --profile` does not ask the Console. Rerun with
  `af-aws-exec`, which does.
- A profile `af-aws-exec` refused is refused here too; plain `--profile` is no way around it.

Agents in the workspace follow the same rule.

- It passes the profile's **short-lived** credentials to that one command through its environment only.
  `af-aws-exec` itself writes them nowhere and prints nothing but the identity the command runs as. (The AWS CLI
  keeps its own login and role caches under `~/.aws`, as it always does.)
- The profile must have an **account and role** set in Settings. A profile that also carries `role_arn` (even
  empty) or a `web_identity_token_file` path is refused because the AWS CLI would then not use its SSO login. One that also carries
  `source_profile`, `credential_source`, `credential_process`, static keys or an empty `web_identity_token_file`
  (next to `role_arn` or not) (in `~/.aws/config` or
  `~/.aws/credentials`) is refused too: the AWS CLI would still use SSO, but other SDKs and tools use those first,
  so the one name would mean different identities to different tools. A tool that syncs credentials into
  `~/.aws/credentials` under the same name (yawsso, for example) causes this; sync to another name. Keys under a
  `[DEFAULT]` section count, since the CLI applies them to every profile, and so does an empty value. The credentials
  are obtained through the profile's SSO login alone (from a minimal config holding only its SSO settings, with
  endpoint overrides ignored) and then checked with AWS to be a session of that profile's permission-set role in
  that account. `--profile default` is refused: name the SSO profile.
- **A profile that is not SSO.** Some accounts are reached only through a profile of your own in `~/.aws`: one that
  assumes a role from a `source_profile` (`role_arn` + `source_profile`), or one with a `credential_process`.
  `af-aws-exec` runs such a profile when you name its account with `--account`, which is required here:

  ```sh
  af-aws-exec --profile deploy-target --account <id> --region <region> -- ./deploy.sh
  ```

  The AWS CLI resolves the profile from your own files, with its own ways to the workload role (the container
  credentials variables, IMDS) and endpoint overrides switched off, and the command runs only if AWS reports the
  credentials in that account (and, for a role, as a session of that role). A `credential_process` is your own
  program and runs as you wrote it: `af-aws-exec` checks the account of what it returns, not where it came from. It
  is refused, before anything is fetched, when the profile or any profile in its `source_profile` chain (a
  `[DEFAULT]` section included) sets `credential_source` (that takes the workspace's own credentials),
  `web_identity_token_file`, `login_session` or `mfa_serial` (nobody can answer the MFA prompt when an agent runs the
  command), when a profile in the chain names more than one way to get credentials (for example keys, even only a
  session token or keys in `~/.aws/config`, beside a `credential_process` or on a role profile: the AWS CLI and
  other SDKs would not agree on which to use), when the SSO profile a chain ends in is incomplete, or when the chain
  is broken. Keep one way per profile: keys go in a profile of their own, named as `source_profile`. The one
  exception is a role profile you name that is its own `source_profile` and holds the keys itself. When a
  `credential_process` fails, its output is not shown; run it yourself to see why. Only temporary credentials are passed: a profile that resolves to long-lived keys is
  refused, so use the keys to assume a role instead. If the chain ends in an SSO profile whose login is missing, the
  command exits with code 3 and the `aws sso login` command for that SSO profile (at a terminal it starts the login
  itself); the Console is not asked. These profiles do not appear in `af-aws-exec --list`, and they cannot share a
  name with a Settings profile. The source keys stay in your `~/.aws` files as before; `af-aws-exec` never hands them
  to the command.
- The workload role is **blocked** for that command: if the login is missing or expired, it fails instead of
  falling back. Inside a workspace, at a terminal too, it asks you in the Console (next item); `--login` runs the
  device-code login in that terminal instead.
- **When a command needs the login** (an agent's, or yours in a shell session's terminal), it asks you in the Console: a toast at the bottom of the
  screen names the profile, its account and role from Settings, and which session asks. Press **Log in** to open the
  login window, check what it is for, and press **Log in** there; only then is a sign-in code created, and only
  that window shows it. Check the code, press **Sign in and approve**, and approve it on the page that opens. The
  agent's command waits about a minute and a half (a few seconds when it is not run by an agent session, such
  as a script in a shell session) and continues once you approve; if it has given up by then, the agent runs it again. **Close** keeps the request: it stays in the Console on your other devices too, so on a device
  whose browser cannot sign in, close it and press **Log in** in the Console on another one. **Cancel the request**
  is for a login you do not want: it withdraws the request, and for about a minute that profile is not asked for
  again. Closing the toast only hides it in that tab. **Log in** on the profile's row in Settings works at any time, also
  during that minute. This covers your Settings profiles; for a profile you defined
  yourself, or with `--no-login`, the command exits with code 3 and the login command to run in a terminal.
  **At your own terminal** (a shell or SSM session) the command prints that it asked the Console and waits up to ten
  minutes, as long as the device code lives, for you to approve there, then carries on without a rerun. **Ctrl-C**
  stops waiting: the command exits with code 3 and the request stays in the Console. Add `--login` to run the
  device-code login in that terminal instead (it prints the URL and code there); that is also what happens when
  the Console cannot be asked at all, and for the profiles above that the Console does not cover. Outside a
  workspace nothing changes: the terminal login starts at a terminal and the command exits with code 3 without one.
- The command gets an AWS config that defines **only the profile you chose** (it hands back the same short-lived
  credentials), no credentials file, and no `AWS_ENDPOINT_URL*` overrides. A tool that names that same profile
  works. A tool that names a different one (Terraform's `profile = "staging"`, `cdk deploy --profile staging`,
  `AWS_PROFILE=staging` in a script) fails with "The config profile (staging) could not be found" instead of
  quietly running as that other profile. If `af-aws-exec` cannot keep that config private (a home directory other
  users can write to, for example) it gives the command an empty AWS config instead, with a warning: still isolated,
  only a tool naming the same profile will not find it. If a script inside the command
  replaces `AWS_ACCESS_KEY_ID` (after an `assume-role`, say), the chosen profile stops resolving there rather than
  quietly meaning the new account; use the new credentials without `--profile`.
- **When you see "could not be found"**, the tool is asking for another profile. Remove that profile setting from
  the tool, or run it under that profile (`af-aws-exec --profile staging …`). Do not add `--keep-aws-config` to get
  past it: that hands the tool your own `~/.aws` files and endpoint settings again, and it would then run as the
  profile it names. Keep `--keep-aws-config` for tools that need other settings from those files.
- **Region**: `--region` if you give it, otherwise a region already exported in your shell (`AWS_REGION`, then
  `AWS_DEFAULT_REGION`), otherwise the profile's. The command gets it in both `AWS_REGION` and
  `AWS_DEFAULT_REGION`, and the "running as" line shows it. A stale
  `AWS_REGION` in your shell beats the profile's region, so give `--region` for deployments.
- `--account <id>` refuses to run unless the profile is that AWS account. Put it in scripts, runbooks and agent
  instructions for anything that deploys, so a wrong profile name stops before anything happens. For a profile that
  is **not** one of your Settings profiles (one you defined yourself) `--account` is required.
- A name that means two things is refused: two Settings labels that map to it, or your own `~/.aws` definition of
  a Settings profile's name with a different account, role or sign-in portal.
- Credentials last as long as the SSO role session, or the assumed role's session (often one hour). A longer
  command fails when they expire rather than switching identity.

### The AWS badge in the WS bar

While the workspace is running and you have at least one AWS profile in Settings, the WS bar shows a badge with
the AWS mark. It names the profile when exactly one is logged in, and otherwise counts them ("1/3", one of three
logged in; a **Renews on use** login counts as logged in). A profile that assumes a role from another has no login
of its own, so the count leaves it out. The badge is **green** when something is logged in, **plain** when nothing
is, and **amber** while any login ends soon (the warning described under "Before a login ends" above); its
tooltip then reads "AWS profiles: a login ends soon". Press it for the list: each profile with its state
(**Signed in**, **Renews on use**, **Not signed in**), its name, account and role, **Ends {time}** on a row whose
end is known (amber when it ends soon), **Log in** on a row that is not signed in or ends soon, and **Log out** on
a signed-in row (see above). A chained profile's row reads **"Follows {source}'s login"**, or **Source
unavailable** when the source is missing or not exported; its **Log in** signs in to the source, and its **Log out**
ends the source's login (after a confirmation naming it). There is no default profile: every logged-in profile can be used at the same time, and each command
picks one with `af-aws-exec --profile <name>`. **AWS profile settings** at the bottom opens the Settings tab.
The list is refreshed when the workspace comes up, when you open it, after a login or logout, and when a known
login end passes.

## Running commands in Google Cloud as you (af-gcloud-exec)

Your Google Cloud profiles live in **⚙ Settings → the "Google Cloud" tab**. A profile says which project a
command points at and as whom it acts:

- **Label**: the display name. The profile's **name**, the one commands use, is made from it and shown next to
  it: lowercase letters, digits and `-` (every other run of characters becomes `-`, a name that would start with
  a digit gets `p` in front, and a label with nothing usable in it, such as a Japanese one, gets `p-` and a short
  code). If two labels make the same name (`Prod` and `prod`), **neither is available** in the workspace and the
  row says so; rename one.
- **Login method**: a Google account, including Google Workspace and Cloud Identity accounts.
- **Project**: the project ID (not the number) commands point at by default.
- **Quota project** (optional): the project billed for API quota; the project itself when left blank. Some
  APIs need one with a personal login, and your account needs permission to use services on it.
- **Account** (optional): the Google account to sign in as; a sign-in as anyone else is refused. Left blank,
  you choose at the first login.
- **Impersonate service account** (optional): commands act as this service account through your login. Your
  account needs the Service Account Token Creator role on it. This is the way to act as a service account:
  service-account keys are not accepted anywhere.
- **Region / Zone** (optional).

**No Google credentials are stored in Agent Fleet.** The login lives inside your workspace, in a gcloud store
of the workspace's own. It is separate from the `gcloud` you run in a terminal: logging in to one does not log
in to the other, and a profile never reads or changes your own gcloud configuration, logins or application
default credentials. A change in Settings reaches the workspace within about five minutes, or at once when you
run `af-gcloud-exec`. Changing a profile's account, changing its label so that its name changes, or deleting
and adding it again, resets which account it uses: unless the account it names is already logged in in the workspace, the next run asks for a
login. Export and import carry the profiles (see [20 Settings](20-settings.md#export-import)).

### Running a command as a profile

```sh
af-gcloud-exec --list
af-gcloud-exec --profile <name> --project <project-id> -- gcloud compute instances list
af-gcloud-exec --profile <name> --project <project-id> -- terraform plan
af-gcloud-exec --profile <name> --project <project-id> -- kubectl get pods
```

- `--list` prints each profile's name, project, account ("chosen at the first login" until then), the service
  account it impersonates and its label, and names the profiles that are not available, with the reason. Choose
  by project and account, not by the name alone.
- `--project` must be the profile's project, or the command is refused. A Google login is not bound to a
  project, so this only checks that you and the profile agree on where the command points by default: a
  command's own `--project`, or a project written in a Terraform configuration, still wins.
- The command gets a short-lived **access token** of the profile and nothing else of yours: no gcloud login, no
  application default credentials, and nothing from the machine's own Google identity. `af-gcloud-exec` prints
  "profile … runs as <account> in project …; the token is valid for N more minutes" (`-q` leaves it out); the
  token itself is never printed.
- The token lasts what remained when it was handed over: at least ten minutes, at most about an hour. While the
  command runs, `af-gcloud-exec` stays running beside it and **renews the token file** about eight minutes before the
  token ends. Every `gcloud`, `bq` or GKE auth plugin process the command starts after that reads the new token, so
  a script that calls them again keeps working. A process that holds the token it read at its start keeps that one:
  a single long-running `gcloud` (SDK 587.0.0 reads the file once), and a program that reads
  `GOOGLE_OAUTH_ACCESS_TOKEN` once, such as **a long `terraform apply`**, fail when that token ends. A `kubectl`
  watch or an open connection depends on the plugin being run again and the connection being made again (not
  measured). Split such work into shorter runs.
- If the token cannot be renewed (the login was revoked or has to be done again, a permission, the network),
  `af-gcloud-exec` says so, keeps trying until the token ends, and then stops the command (SIGTERM, and SIGKILL
  after 30 seconds if it does not end). The exit status is 3 when you have to log in again and 1 otherwise. If the
  Console could be asked when the run started, a login request appears there as it does at the start; with
  `--login`, `--no-login` or outside a workspace the message gives the command to run in your terminal, and a request
  cancelled in the Console is reported as cancelled.
- Stopping `af-gcloud-exec` sends SIGTERM to the command it started (the command may ignore it), and the same happens
  when `af-gcloud-exec` is killed. Programs the command itself started are not reached. When `af-gcloud-exec` is
  killed, the token file stays until its token has expired and a later run removes it.
- The first run installs the Google Cloud SDK (one pinned version, with the GKE auth plugin) into your home: about
  85 MB to download and about 510 MB on disk, kept across stops and a Recreate. The first run of each gcloud
  command after that is a few seconds slower once. The **Toolchain** tab's table of tool versions then shows
  gcloud. A plain `gcloud` in a terminal is that same program, but with your own login, if any, not a profile's.
- For `kubectl`, fetch the cluster's entry once through the profile
  (`af-gcloud-exec --profile <name> --project <project-id> -- gcloud container clusters get-credentials <cluster> --location <location>`),
  then run `kubectl` through `af-gcloud-exec` too: the GKE auth plugin asks gcloud for the token, and only inside
  `af-gcloud-exec` does gcloud have one.

**Which tools use the token.**

| Tool | With `af-gcloud-exec` |
|---|---|
| `gcloud`, including `gcloud storage` | yes |
| `kubectl` against a GKE cluster | yes, through the GKE auth plugin |
| Terraform's Google provider | yes, with the quota project; the token is not renewed during a run |
| `bq` | yes. It comes with the SDK but is not on the path: run `~/.local/share/agent-fleet/google-cloud-sdk/bin/bq` |
| `gsutil` | **no**. It ignores the token: without a gsutil (boto) configuration of your own it sends its requests without any login, so a public bucket answers; with one, it can act as whatever identity that configuration holds. Use `gcloud storage` |
| Google's client libraries (Go, Python, Node, …) | only when the program hands them the token |

A program built on a client library stops with an error such as "File … was not found" or "no such file or
directory" for its default credentials. That is deliberate: it would otherwise find another identity (your own
gcloud login, or the machine's). The program has to take the token itself: in Python
`google.oauth2.credentials.Credentials(os.environ["GOOGLE_OAUTH_ACCESS_TOKEN"])`, in Go
`option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN")}))`
(Go's libraries then use the profile's quota project, which `af-gcloud-exec` sets in `GOOGLE_CLOUD_QUOTA_PROJECT`;
in Python pass `quota_project_id=os.environ["GOOGLE_BILLING_PROJECT"]` to `Credentials`). In Node, create the `OAuth2Client` from the same version of `google-auth-library` the client library uses; one
from another major version is accepted without an error and sends no login at all. Agents ask you before changing
your code that way.

### Logging in

**In the Console.** Each profile row in **⚙ Settings → Google Cloud** has **Log in**; once the workspace holds a
login for the profile, the row says **"Logged in as <account>"** and the button becomes **Log in again**. The WS
bar's Google Cloud badge has the same buttons ([below](#the-google-cloud-badge-in-the-ws-bar)). The button opens a
login window that shows the profile, its project and its account and starts nothing yet: the sign-in starts only
when you press **Log in** (or **Log in again**) in that window, and only that window then shows the sign-in.

1. Press **Open Google sign-in**. Google's sign-in page opens in a new tab (the window also shows its address,
   which is always on `accounts.google.com`: the workspace refuses to show any other). Sign in as the profile's
   account, if it names one; a sign-in as anyone else is refused.
2. Google shows a **verification code**. Paste it into **Verification code** in the same login window and press
   **Submit code**. The window says "Logged in" once the workspace has checked that the login gives a token.

**Paste a code only into a login you started yourself, here, just now.** A code works only for the sign-in whose
page produced it, and the Console shows the code field only in the window where you pressed **Log in**, but
nothing proves who started a sign-in: any program in your workspace, an agent included, can start one. So never
paste a code into a field, a terminal or a chat message someone else put in front of you.

If the workspace already holds a usable login for the account, **Log in** finishes at once without a sign-in page.
**Log in again** always signs you in afresh instead of reusing what is stored; use it when you know the login was
revoked (the workspace cannot see that until a command is refused). If you press **Log in** again in another
window or device, the first sign-in stops and its code no longer works. A sign-in that is not finished within
15 minutes ends; **Start again** begins a new one.

**Log out** on a logged-in row (in Settings or in the badge) deletes the login the workspace stored for the
profile's Google account. The workspace keeps one login per account, so every profile using that account is
logged out with it; the confirmation names them before anything is deleted. Your sign-in at Google, and gcloud on
your other machines, are not touched, and a token a command already received stays valid until it expires (at most
an hour). A sign-in waiting for its code in one of those profiles' login windows stops. To use the profile again,
log in again. If the profile's account changed between the confirmation and the logout (another login finished
meanwhile), nothing is logged out and the Console says so.

**When an agent's command needs the login**, it asks you in the Console: `af-gcloud-exec` prints "Google Cloud
login for profile … requested in the Agent Fleet Console", and a toast at the bottom of the screen says **"An agent
is waiting for a Google Cloud login"**, with the profile, its project and which session and command ask. Press
**Log in** on the toast to open the login window and continue as above. When Google refused the stored login, the
window says so, and the sign-in starts afresh. The agent's command waits about a minute and a half (a few seconds
when it is not run by an agent session, such as a script in a shell session) and continues once you are logged in;
if it has given up by then, it exits with code 3 saying the login is waiting in the Console, and the agent runs it
again after you tell it you are done. **Close** keeps the request: it stays in the Console on your other devices
too. **Cancel the request** is for a login you do not want: it withdraws the request, the command exits with
code 3, and for about a minute that profile is not asked for again. Closing the toast only hides it in that tab.
**Log in** on the profile's row in Settings, or in the badge, works at any time; a sign-in there settles the
request too.

**At a terminal** in a workspace, `af-gcloud-exec` asks the Console like an agent's command, and waits up to ten
minutes for you to finish the login there; Ctrl-C stops waiting with exit code 3 and leaves the request in the
Console. With `--login` it starts the Google sign-in in that terminal itself (and does so outside a workspace, or when
the Console cannot be asked): it prints a URL, you open it in your browser, sign in, and paste the verification code
the page shows back into **that** terminal. The same rule holds: paste only a code from a sign-in you started yourself
just now. A command that
cannot ask the Console (with `--no-login`, or outside a workspace) exits with code 3 and prints the command to run
in a terminal of your own (a shell session, or in Claude Code type it after `!` at the prompt):

```sh
af-gcloud-exec --profile <name> --project <project-id> --login -- true
```

**On a Compute Engine VM.** On a workspace that runs directly on a Google Cloud VM (the `native` runtime), gcloud
first asks whether to use a personal account on that VM. The Console login does not answer that question for you:
the login window ends saying so, and you log in with the terminal command above instead.

**When a login ends.** Google can refuse a stored login: it was revoked, or your organisation requires you to
sign in again after a set time (session length). The next run that needs a fresh token then asks for a login as
above, and that login really signs you in again instead of reusing what was stored. The workspace **cannot warn
you before** such an end: the time is not recorded anywhere it can read. Until the token already handed out has
less than ten minutes left, runs keep working, so the request for a login can come up to about 50 minutes after
the end.

### The Google Cloud badge in the WS bar

While the workspace is running and you have at least one Google Cloud profile, the WS bar shows a badge with the
Google mark beside the AWS one. It names your logged-in profile when there is exactly one, and otherwise counts
them ("1/3", one of three logged in). It is green when something is logged in, plain when nothing is, and amber
while an agent's command is waiting for a profile's login. Press it for the list: each profile with **Logged in**
or **Not logged in**, its name, project and account, **"An agent is waiting for this login"** on the row an agent
is waiting for, **Log in** / **Log in again**, which open the login window above, and **Log out** on a logged-in
row (see above). There is no default profile:
every logged-in profile can be used at the same time, and each command picks one with
`af-gcloud-exec --profile <name>`. **Google Cloud settings** at the bottom opens the Settings tab.

**Logged in** means only that a Google login for the profile's account is stored in the workspace. The workspace
does not check it with Google, and keeps it when Google refuses it, so a revoked login stays **Logged in**, even
after a command has found out. While that command's login request waits, the badge is amber; once the request is
cancelled or expires (after 15 minutes), the badge is green again although the login no longer works.
**Log in again** signs in afresh and fixes it. The badge does not poll: it asks the workspace when it comes up,
when you open the list, when a login request comes or goes, after a login, when you come back to the tab, and
after you change a profile in Settings. A profile you add or delete shows there once the workspace has picked up
the change, within about five minutes.

### When it stops

| What you see | What it means |
|---|---|
| exit code 3, "the login was requested in the Agent Fleet Console and is waiting …" | The profile has no login yet, or Google refused the stored one, and the command stopped waiting. Log in from the toast or Settings, then have the command run again. Exit 3 always means a login. |
| exit code 3, "the login request was cancelled in the Agent Fleet Console" | You cancelled the request. Log in from Settings if you do want it. |
| exit code 3, "Google Cloud login required … log in from a terminal with:" | The Console could not be asked (`--no-login`, or outside a workspace). Run the printed command in a terminal. |
| "is for project X, not Y; --project must be the profile's project" | The wrong profile for this project. Check `--list`. |
| "no Google Cloud profile …" or "not exported: …" | The name is not one of your available profiles: add or fix it in Settings. |
| "gcloud could not mint a token: …" | Not a login problem: no permission (also on the service account to impersonate), an API not enabled, or the network. The message says which. |
| "the token gcloud minted is valid for only …" | gcloud could not hand out a token with ten minutes left. Try again in a minute. |
| "this deployment does not export Google Cloud profiles" | Your deployment does not offer Google Cloud profiles. |
| "could not renew the token of profile … trying again until it ends in N minutes" | The token the command is using could not be renewed yet. The run keeps trying; if the token ends first, the command is stopped. |
| "the profile's login is now X, not Y, which the command started as; run it again" | Someone logged the profile in as another account during the run. Run the command again. |
| "the profile changed in Settings while this run started; run it again" | Run it again; check `--list` if the account or project now differs. |
| "waiting for another af-gcloud-exec or a profile sync …" | A login in a terminal, or another run, is using the workspace's gcloud store. It continues when that ends. |

**What it does not guarantee.** The command runs as you and can read your home, your own gcloud directory
included: `af-gcloud-exec` keeps Google's standard ways of finding credentials (gcloud's and the client
libraries') from finding anything but the token it hands over; it does not make your files unreadable, and a tool
that looks elsewhere, such as `gsutil` with its own configuration, is not covered. On a workspace that runs directly on a Google Cloud VM, a
program that ignores those standard ways could still reach the VM's own identity. The same holds for a pod on Kubernetes: the
workspace is started with `AWS_EC2_METADATA_DISABLED=true`, but that variable does not block the cluster's metadata server (the
node's service account, or Workload Identity on GKE), so a program that ignores the standard ways could still reach that
identity unless your administrator blocks the route in the cluster's network.
