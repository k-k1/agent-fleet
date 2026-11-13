---
audience: "anyone checking exactly how AWS profiles behave"
source_of_truth: "the AWS profiles/SSM tab of the Console's Settings, and af-aws-exec in the workspace"
---

# AWS profiles reference

English | [日本語](aws-profiles.ja.md)

The detailed rules for the AWS profiles registered under "AWS profiles/SSM" in Settings. How to use them is in
[Use AWS SSO](../user/integrations/aws-sso.md).

## Login and states

Each profile row has **Log in**, which signs you in to IAM Identity Center for that profile without leaving the
Console. It opens a login window; the sign-in code is created only when you press **Log in** there, and only that
window shows it. The login serves SSM sessions and `af-aws-exec` for that profile. The button is off for a profile
without both an account and a role, and for one whose name another label also maps to ([af-aws-exec](cli/af-aws-exec.md)).
Beside the label, a badge shows the login state: **Signed in**, **Renews on use** (the access token has expired;
while the portal session is open, the next use renews it) or **Not signed in**. It shows no time left: the
workspace knows only the access token's expiry (about an hour), not when the portal session ends.

**Before a login ends: only for a login that cannot renew.** A normal login from Settings renews itself on use
until the portal session ends, and that end is recorded nowhere the workspace can read, so such a login is **not
warned about in advance**: when the portal ends it, the next command asks for a login ([Use AWS SSO](../user/integrations/aws-sso.md#ask-an-agent)). Only when the cached
login has nothing to renew it with (no refresh token, or its sign-in client registration has expired) is its end
known; then the Console warns once per profile, about 10–15 minutes before: a toast "Your AWS login ends at …" (and
a notification), counting down the time left, with **Log in** opening the same login window. The warning goes once you log in again; closing the
toast hides it for that end in that tab. A profile that is not in the managed block (see `af-aws-exec --list`) is
never warned about.

**Logging out of one profile.** A row that is signed in (or renews on use) has **Log out**, both here and in the
popover of the WS bar's AWS badge. After you confirm, the workspace ends that profile's login with AWS and deletes
its cached login and role credentials; profiles that do not follow it stay signed in. If chained profiles follow this
one ([assume role](#assume-role)), the confirmation names them first: they are signed out with it. Credentials a running command already
received stay valid until they expire (AWS cannot recall them), so stop that command if it matters. An expired
access token (a **Renews on use** row) is renewed first so that the logout reaches AWS; if AWS answers that the
login has already ended, the logout is a normal one. If AWS cannot be reached, the renewal fails for any other
reason, AWS refuses the logout itself, or the login has nothing to renew it with, the workspace is signed out all the
same and the Console says so; the login may then stay valid at AWS until it ends. Do not use `aws sso logout` for this: it signs out every profile at once, whatever `--profile` says.

## Import rules

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
  `[DEFAULT]`), a role assumed from another role, an SSO profile without both account and role, a key its `sso-session` gives
  differently (the AWS CLI refuses that too), a value Settings
  cannot hold, or a name that is taken or turns into the same AWS profile name as another.

You can untick a row, change its display name (needed for `[default]`: `default` cannot be a Settings name) and
fix a role's session length (900–3600 seconds). Renaming a source keeps the roles that use it attached.

- **Secrets are never imported.** If the text holds access keys, the import says so: do not paste that text
  anywhere else. Ask for `~/.aws/config` only, never `~/.aws/credentials`.
- A file the AWS CLI cannot read (a section or key given twice, a key before any section) is refused with its line
  number.
- **Sign-ins follow your machine's.** Each SSO profile row has a **Sign-in** name, proposed so that profiles
  sharing one login on your machine share one here (see [shared sign-ins](#shared-sign-ins)): a profile that uses
  `[sso-session <name>]` gets the sign-in `<name>`; an older profile with `sso_start_url` inline gets one sign-in per
  portal (start URL and SSO region), named after the first part of the portal's host (`d-1234567890` for
  `https://d-1234567890.awsapps.com/start`). Type another name to change it for every row that shared the same login on your machine, or clear it
  to give the profile a login of its own. A name that is not 1–61 letters, digits and `.` `_` `@` `-`, one an
  existing sign-in of yours holds for another start URL or SSO region, and one that another profile of yours with a
  login of its own already logs in under, are each shown on the row with the reason; fix the name or untick the row.
  The preview counts the logins needed: one per new sign-in, plus one per profile with a login of its own. When your
  sign-ins cannot be read, the preview says so and every profile is imported with a login of its own.
- A profile without a region uses its SSO region (a role: its source's); on your machine an environment variable
  may have set it. The preview shows which region each row gets.
- Importing again with the same choices creates nothing new. If a run stops halfway, read the file again: what was
  created shows as already registered and the rest can be imported.

## Export rules

**Export as ~/.aws/config** (next to the import) builds the text of an AWS config file from your Settings profiles
and downloads it as `agent-fleet-aws-profiles.aws-config-fragment.txt`. It is built in your browser and nothing is
sent. **It is a fragment to merge into your own `~/.aws/config` by hand, not a replacement for it**: it holds only what
Agent Fleet can express (SSO profiles, each with its own `sso-session`, and the roles assumed from them), so replacing
your file with it would drop your access keys, MFA and `credential_process` profiles and your own `[default]`. That is
also why the file is not called `config`.

The dialog lists every profile that was **left out**, with the reason:

- the name `default` (reserved);
- two profiles whose display names become the same AWS profile name (`prod app` / `prod-app`): both are left out until
  one is renamed, as in the workspace's own `~/.aws/config`;
- an SSO profile without both an account and a role;
- a value the AWS config cannot hold (every value is checked against the same rules the workspace uses before it is
  written, so a stored value cannot add a setting or a section of its own);
- profiles whose `sso-session` would also serve another sign-in, another profile or another portal (a sign-in and a
  profile of the same name, or members whose portals differ): all of them, until the names or portals agree;
- a role whose source profile is itself left out, missing, or not an SSO profile.

Each SSO profile is written with its own `sso-session` (`af-<name>`), so each needs its own login on your machine too.
The members of a [shared sign-in](#shared-sign-ins) share one `[sso-session af-<sign-in>]`, so one login there serves them all.

## Shared sign-ins

Several profiles often reach different accounts or roles through **one** IAM Identity Center portal, the way your
laptop shares one `[sso-session]` between them. Put them in one **shared sign-in** and the workspace logs in to that
portal **once** for all of them, instead of once per profile.

**Creating and joining.** In a profile's form, **Sign-in** chooses:

| Choice | What it does |
|---|---|
| Its own sign-in (the default) | Today's behaviour: the profile has a login of its own, with its own start URL and SSO region. |
| One of your sign-ins | The profile joins it. The start URL and SSO region are the sign-in's and are not entered again. |
| New shared sign-in… | Creates a sign-in with the name you type and the start URL and SSO region you enter, and puts the profile in it. |

A sign-in name is 1–61 letters, digits and `.` `_` `@` `-`; the workspace calls its login `af-<name>`. Saving can be
refused with a reason in words:

- **another start URL or SSO region**: the sign-in you chose uses another portal. A profile in a sign-in always uses
  the sign-in's portal; change the portal on the sign-in's header if it really moved;
- **the name is taken**: another profile of yours with a login of its own has that name. Choose another sign-in
  name, or rename that profile;
- **a sign-in of that name already exists**: pick it from the list to join it.

**The list.** The profiles of a sign-in are listed under one header that names the sign-in and its portal and
carries the login state, **Log in** and **Log out** once. A member row keeps its account, role and region and has no
login button of its own. Profiles with a sign-in of their own keep their own rows.

**Logging out** of a sign-in (on its header, or on a member in the WS bar's AWS badge) signs **every** member out,
and every assume-role profile that follows one of them; the confirmation lists them first.

**Renaming a sign-in or changing its portal.** **Edit** on the header changes both. Either one signs every member
out: a rename moves the login to a new name, and a token from the old portal is never used for the new one. The
confirmation says so and names the members; afterwards they show **Log in again**.

**Grouping profiles you already have.** Tick the profiles with a login of their own (the box at the start of each
row; **SSO profiles only**), then press **Share one sign-in** under the list. They must have the same start URL and
SSO region; otherwise the button stays off and says why. The dialog proposes the name of a member that is signed in
now, so the group keeps that login (below); you can type another name. It checks the name before saving: a name that
is not valid, one of an existing sign-in on another portal, or one of a profile you did not tick that keeps its own
login is refused in words. Each profile is saved with the same save as the form, the first one creating the
sign-in. **If a save fails part way, the dialog stays open** and lists every profile as moved or not, with the
reason; the ones that failed keep the sign-in they had, and **Retry** moves only them. Nothing is left half done
without showing it.

**Keeping a login when you group.** When the sign-in takes the name of a profile that is signed in now, that
profile's login becomes the sign-in's and nobody has to log in again. With any other name the sign-in starts
signed out and needs one login. Leaving a sign-in (choose **Its own sign-in**) gives the profile a login of its own
again, so it needs a login too.

Limits:

- Nothing is grouped behind your back. Apart from the import's proposal, which you see and can change in the
  preview, two profiles with the same portal and no sign-in, or in two sign-ins, still log in separately.
- An SSM host's **Log in again** on a member drops the shared login, so every member is signed out.
- If your own `~/.aws/config` defines `[sso-session af-<sign-in>]`, every member of that sign-in is held back
  (`af-aws-exec --list` says why), as with a profile name of your own.
- A cached login is tied to the sign-in (or profile) and portal it was made for. One made for another owner or
  portal is never used: the workspace moves it aside, deletes the role credentials taken through it, and asks
  for a new login.
- Logging out of a sign-in also removes a member's own login from before it joined, but only when it is certain
  that login was that member's and nothing else uses its name.
- The import from `~/.aws/config` puts profiles in a sign-in as proposed above; a profile it creates joins the
  sign-in of that name if you have one on the same portal.

## Assume role

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

## AWS badge

While the workspace is running and you have at least one AWS profile in Settings, the WS bar shows a badge with
the AWS mark. It names the profile when exactly one is logged in, and otherwise counts them ("1/3", one of three
logged in; a **Renews on use** login counts as logged in). A profile that assumes a role from another has no login
of its own, so the count leaves it out. The badge is **green** when something is logged in, **plain** when nothing
is, and **amber** while any login ends soon (the warning in [login and states](#login-and-states)); its
tooltip then reads "AWS profiles: a login ends soon". Press it for the list: each profile with its state
(**Signed in**, **Renews on use**, **Not signed in**), its name, account and role, **Ends {time}** on a row whose
end is known (amber when it ends soon), **Log in** on a row that is not signed in or ends soon, and **Log out** on
a signed-in row ([login and states](#login-and-states)). A chained profile's row reads **"Follows {source}'s login"**, or **Source
unavailable** when the source is missing or not exported; its **Log in** signs in to the source, and its **Log out**
ends the source's login (after a confirmation naming it). There is no default profile: every logged-in profile can be used at the same time, and each command
picks one with `af-aws-exec --profile <name>`. **AWS profile settings** at the bottom opens the Settings tab.
The list is refreshed when the workspace comes up, when you open it, after a login or logout, and when a known
login end passes.
