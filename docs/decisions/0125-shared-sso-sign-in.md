# 0125. Settings AWS profiles can share one IAM Identity Center sign-in

English | [日本語](0125-shared-sso-sign-in.ja.md)

- Status: **accepted** (2026-10-30). The user took decisions A–D below, the recommended option in each. Nothing is built yet.
- Issue: #3014
- Prerequisite: [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.md) (#3050): runs shown in the
  session's mirror and per-profile "confirm before use". A shared sign-in removes the per-profile device code
  that worked as an approval, so this ADR does not ship before 0126's run rows, and the gate ships with or
  before it (0126 decision 7).
- Related: [0102](0102-aws-login-through-the-console.md) (the Console login, its requests, attempts, status,
  expiry warning and logout, all keyed by the sso-session) / [0060](0060-settings-export-import.md) (the
  settings bundle, which carries `ssmProfileDTO`) / #3010 (import from `~/.aws/config`, Phase 1 in PR #3018) /
  #3017 (export to `~/.aws/config`)

## Context

Every Settings SSO profile is rendered with an sso-session of its own, `[sso-session af-<profile>]`
(`ssm.RenderSSMConfig`). The name is the cache key: botocore stores the IAM Identity Center token at
`~/.aws/sso/cache/<sha1 of the sso-session name>.json` (`SSOTokenLoader._generate_cache_key`), and the
role credentials at `~/.aws/cli/cache/<sha1 of {accountId, roleName, sessionName}>.json`. Everything ADR 0102
built is keyed by that name as well: the login request and its cancel marker (one per sso-session), the
attempt slot and the logout gate (`cloudlogin`), the cached-login lock, the row's login state
(`profileLoginState`), the expiry warning (`readSSOExpiry`, `warnExpiringSSO`) and the logout
(`HandleProfileLogout`). The string `"af-" + name` is built in about a dozen places in
`workspace/agent/internal/awsx` and once in `ssm/config.go`.

A member whose laptop has 20 profiles under one `[sso-session]` signs in once there and 20 times here.
#3010's import makes that visible at once; its resolver already records each profile's laptop sign-in
(`NativeProfile.signIn`: `session:<sso_session>`, or `url:<start URL>` for a legacy profile).

Constraints from the issue:

- Separate logins to one portal can be intentional (another user, another permission set). Grouping cannot be
  "merge everything with the same start URL and SSO region".
- The member's own `[sso-session af-<name>]` keeps winning over ours (`SessionShadowed`).

What botocore allows, which bounds the design:

- A profile with `sso_session = X` reads the start URL and the SSO region from `[sso-session X]` only. A
  profile that also sets them must set the same values, or the CLI refuses it.
- The token loader does not compare the cached `startUrl` with the config. Under a given key it uses whatever
  is there.
- A refresh may return a new refresh token (`_attempt_create_token` keeps `refreshToken` from the response).
  Two cache files that hold copies of one refresh token can therefore invalidate each other.

## Decision

### 1. A sign-in is a stored object of its own; a profile joins at most one

The CP gets a table `ssm_sign_in`: `id`, `membership_id`, `name`, `start_url`, `sso_region`, `created_at`, with
a unique index on `(membership_id, name)`. `ssm_profile` gets `sign_in_id TEXT NOT NULL DEFAULT ''`.

- **Empty `sign_in_id` means what it means today.** The profile has its own sign-in, `af-<profile name>`, from
  its own `start_url` and `sso_region`. No existing row changes, so neither the rendered bytes nor any token
  cache of an ungrouped profile changes.
- **A non-empty `sign_in_id`** puts the profile in that shared sign-in. Only `kind = sso` profiles join one;
  an assume-role profile follows its source's, as today.
- **The sign-in row is the source of truth** for the start URL and the SSO region. The member's profile rows
  keep a copy, which the store writes in the same transaction as any change to the sign-in or to the
  membership. The copy keeps every reader that predates this ADR correct (the SSM session rewrite, the
  bundle export, an older CP binary after a rollback: it renders each grouped profile with a login of its
  own, which is the behaviour before this ADR and never a wrong account).
- **The member names the sign-in.** The name is shown in Settings and rendered as `af-<name>`. Allowed:
  `^[A-Za-z0-9._@-]{1,61}$`, so `af-<name>` fits the Agent's 64-character allowlist. No sanitizing: a name
  outside the allowlist is refused, so two names can never collapse into one.
- **A sign-in exists exactly while a profile is in it.** It is created by the first profile that names it and
  deleted in the same transaction as its last member leaving or being deleted. No route creates or deletes
  an empty one.

### 2. One sso-session name per sign-in, unique per member

The rendered sso-session of an SSO profile is `af-<sign-in name>` when it has a sign-in, `af-<profile name>`
otherwise. The two namespaces meet, so the CP keeps one invariant in the store's transaction: **no sign-in
name equals the profile name of an ungrouped SSO profile of the same member.** A write that would break it
(a new sign-in, a renamed sign-in, a new or relabelled profile, a profile leaving a sign-in) is refused with
`409 sign_in_name_taken`, naming the other side.

A sign-in may take the name of one of **its own** members. Such a member renders `af-<name>` both before and
after it joins. That is how an existing login survives grouping (decision 7).

The Agent does not trust the invariant. If two exported entries end up with the same sso-session name and
different start URLs or SSO regions (a stale cache, an older CP), it exports none of them and reports them
under a new `SyncResult.SessionConflict`, in the same way as `Conflicts`.

### 3. API: the profile names its sign-in; the sign-in's own edits have their own route

- `ssmProfileDTO` gains `signIn` (the sign-in name, omitted when empty). `POST` and `PUT /api/ssm/profiles`
  read it:
  - empty: own sign-in, as today;
  - a name the member has: join it. The DTO's `startUrl` and `ssoRegion` must be empty or equal to the
    sign-in's, otherwise `409 sign_in_mismatch`;
  - a new name: create the sign-in from the DTO's `startUrl` and `ssoRegion`, which are then required as
    today.

  Join-or-create by name is what lets the bundle import (ADR 0060) and #3010's executor create grouped
  profiles one `POST` at a time, with no new call order.
- `GET /api/ssm/sign-ins` lists `{id, name, startUrl, ssoRegion, profiles: [profile ids]}`.
  `PUT /api/ssm/sign-ins/{id}` renames the sign-in or changes its portal, and rewrites the members' copies in
  the same transaction. A rename moves the login to a new cache key, so the member signs in again; a portal
  change leaves a token for the old portal under the old key. The Console says both before saving
  (decision 9).
- The bridge's `awsProfileWire` (`GET /internal/aws-profiles`) gains `signIn` (omitted when empty). The
  SSM session rewrite (`rewriteSSMCreate`) adds `sso_session` to the body; the Agent's `SSMMeta` gains
  `SSOSession`, checked against the same allowlist. When it is absent the Agent renders `af-<profile>`, so
  an older CP keeps working.
- `TestContractFamilies` gets the new field on `ssmProfileDTO` and a new family for `ssmSignInDTO`. The
  Console types follow.

### 4. Rendering

`renderProfile` and `RenderSSMConfig` take the sso-session name from the profile instead of building it.
The managed block writes each `[sso-session af-<g>]` **once**, before the first exported member, and each
member's `[profile …]` points at it. A sign-in none of whose members is exported writes no section.

- **Shadowing.** The member's own `[sso-session af-<g>]` holds back **every** member of `g`
  (`SessionShadowed`, as now, with the reason naming the sign-in). The member's own `[profile x]` holds back
  `x` only. The other members still render, with the shared section.
- `[DEFAULT]` (`defaultClash`) compares against the written `sso_session = af-<g>`. A clash holds back that
  profile only.
- The chained source's sso-session check (`exec_nonsso.go`), `consoleEligible`, `settingsFor` and
  `notDefined` compare with the profile's own sso-session name (one helper, `ssoSessionOf(Profile)`), not
  `"af-" + name`.

### 5. Login, status, logout, expiry and lock are per sign-in

All of them are already keyed by the sso-session, so the key does not change; what changes is how many
profiles stand behind it.

- **Requests.** `af-aws-exec` for any member files or joins the one request of `af-<g>`. The request keeps the
  profile that filed it. The waiter record gains the profile each waiter asked for. `GET /aws-login` adds
  `signIn` and the profiles waiting; the toast reads "Sign in to `<g>` (N profiles)" when `signIn` is set.
  Account and role still come from Settings, never from the request.
- **Attempts.** One slot per sso-session, as today: a press on any member's row and a press on the toast
  replace each other. The device URL allowlist is built from the sign-in's start URL and SSO region, which
  every member shares.
- **Status.** `GET /aws-login/profiles` gives each profile its `signIn` and the state of its sso-session, and
  adds `signIns: [{name, state, expiresAt?, expiring?, profiles}]`. A cache whose `startUrl` or `region`
  differs from Settings counts as `none`, so a portal change (decision 3) shows as "not signed in" instead
  of as a working login. That rule applies to ungrouped profiles too.
- **Expiry.** `warnExpiringSSO` warns once per sso-session and end, for an sso-session with at least one
  exported member (key `aws-sso-expiring:<sso-session>:<end>`). The payload keeps `profile` (the first
  exported member, so an older Console still opens a modal) and adds `signIn`.
- **Confirmation (ADR 0126).** A login to the sign-in confirms no member that asks for confirmation before
  use. The gate stays per profile.
- **Logout.** The token is shared, so logging out of a member logs out the sign-in. The existing route
  `POST /aws-login/profiles/{name}/logout` stays and acts on the member's sso-session. It deletes the role
  credentials cached for **every** member (`roleCachePaths` per member), not only the named one: a
  member's role credentials left behind would keep that profile working after a "log out". The answer adds
  `profiles`, the names signed out. The Console confirms with that list before it calls the route.
- **Lock and gate.** Unchanged: `logins.LockKey` and `logins.Gate` take the sso-session.
- **SSM forced re-login** (`ssmForgetLogin`) drops the shared token. That signs every member out. The
  Console's "log in again" on an SSM host of a grouped profile says so.

No CP route changes for the login: the Agent routes stay keyed by profile name, and the CP relays them as
before.

### 6. What the Console shows

- **Settings > AWS profiles/SSM.** Profiles of a sign-in are listed under one header that names the sign-in and
  its portal, and carries the login state, **Log in** and **Log out** once. Ungrouped profiles stay as
  today's rows. A member row keeps its account, role and region, and no button of its own for login.
- **Profile form.** A "Sign-in" field replaces the start URL and SSO region inputs when it is set:
  "Own sign-in" (the default, today's fields) or one of the member's sign-ins, or "New shared sign-in" with a
  name. Sign-in rename and portal change happen on the header, with the warnings of decision 3.
- **Grouping existing profiles.** A "Share one sign-in" action on selected ungrouped profiles of the same start
  URL and SSO region. It proposes, as the name, the profile among them that is signed in now (decision 7).
- **Import (#3010).** Each `session:<name>` profile gets `signIn = <name>`, so the preview shows one sign-in
  per laptop `sso-session` and the number of Console logins becomes the number of laptop sign-ins. A name
  outside the allowlist, or one that `sign_in_name_taken` would refuse, is shown with that reason and an
  editable name. An existing sign-in of that name with another portal refuses those rows
  (`sign_in_mismatch`), named in the preview. Legacy `url:` profiles: decision B.
- **Export (#3017).** A grouped profile is written with `sso_session = <name>` and one `[sso-session <name>]`;
  an ungrouped one with `[sso-session <profile name>]`. Importing that file back gives the same grouping.

### 7. Migration and existing logins

- **No data migration.** Migrations `0117_ssm_sign_in.sql` (SQLite) and `0102_ssm_sign_in.sql` (Postgres),
  the numbers after ADR 0126's `confirm_use` column, add the table, the column (default `''`) and the index. They are forward compatible: an older binary
  ignores both, and the copied coordinates keep its output correct. The numbers are re-checked against
  `main` when the change is opened.
- **Nothing changes for a member until they group.** Every existing login keeps working.
- **A login survives grouping when the sign-in takes the name of a signed-in member.** That member's
  sso-session name does not change, so its token is the group's. The Console proposes that name. With
  another name, the group starts signed out and needs one login.
- **Leftover per-profile tokens.** A member that joins a sign-in under another name leaves its
  `af-<member>` token on disk. Nothing uses it any more. It is not moved or copied (Rejected). Logging out
  of the sign-in also revokes and deletes the leftover `af-<member>` tokens of its members. That is skipped
  for a member whose `af-<member>` name the member's own files use (decision C).

### 8. Separate logins to one portal stay possible

Nothing groups profiles automatically. Two profiles with the same start URL and SSO region and no sign-in,
or in two sign-ins, render two sso-sessions and log in separately. The only grouping by start URL is the
Console's proposal for legacy laptop profiles on import (decision B), and the member can change it in
the preview.

### 9. Documentation

`guide/member/16-cloud-accounts.md` (and `.ja.md`) explains sign-ins, the logout that covers every member,
and what a rename or portal change does. `workspace/notes/aws.md` (the `af-aws` skill) says that a login
request can name a sign-in rather than a single profile.

## Rejected

- **Merge by start URL and SSO region automatically.** It breaks intentional separate logins (the issue's
  constraint).
- **A free-text `sso_session` column on each profile, with no sign-in row.** The portal would live in N rows
  that must stay equal. Changing it means N writes, and the CP cannot change them atomically through the
  profile route.
- **Store the portal only on the sign-in, blank on its members.** Readers that predate the sign-in (the SSM
  rewrite, the bundle, an older binary after a rollback) would see a profile without a portal.
- **Copy or move a member's token to the new key on joining.** A copy holds a refresh token that a refresh of
  the other copy can rotate away, and both files then look valid until one of them fails. A move relies on
  the cache file layout and on the loader not checking `startUrl`. Taking over a member's name gives the same
  result through the CLI's own key.
- **A distinct prefix for sign-in sessions** (`af-signin-<name>`, `af:<name>`). The prefix can still collide
  with a profile name, and a prefix that cannot collide needs a character the CLI has not been measured
  with. It would also make a login unable to survive grouping.
- **Per-profile logout inside a sign-in.** There is one token. Signing one profile out without the others is
  impossible.
- **Empty sign-ins as standalone objects** (create and delete routes). They would add a third object with no
  meaning on its own, and an import would need two call kinds.

## Consequences

- A member with N profiles on one laptop sign-in logs in once in the Console.
- Logout, the SSM forced re-login and a sign-in rename affect every member, and the Console says so first.
- The Agent builds the sso-session name in one helper instead of a dozen literal concatenations.
- The SSO cache status reads `startUrl` and `region` from the cache file as well as the expiry.

## Decisions taken (2026-10-30, by the user)

- **A. Taken: the recommended option.** **Delivery.** Recommended: three PRs in order, after ADR 0126's two (0126 decision 7). CP (migration, store
  invariant, DTO, routes, bridge, contract); then Agent (render, shadowing, `ssoSessionOf`, requests, status,
  expiry, logout); then Console (grouped list, form, grouping action, import default). Every new field is
  omitted when empty, so each PR is safe on its own.
- **B. Taken: the recommended option.** **Legacy laptop profiles on import** (`sso_start_url` on the profile, no `sso_session`; the laptop shares
  one login per start URL). Recommended: propose one sign-in per start URL and SSO region in the preview,
  named after the portal's host label (`d-1234567890`, editable, can be cleared). The alternative is to
  import them ungrouped.
- **C. Taken: the recommended option.** **Leftover per-profile tokens after joining.** Recommended: clean them at the sign-in's logout only
  (decision 7). The alternative is to revoke and delete them when the profile joins, which means a network
  call from a Settings sync.
- **D. Taken: the recommended option.** **A sign-in named after a member.** Recommended: the Console proposes it whenever a member is signed in,
  so grouping costs no login. The alternative is always the laptop or typed name, with one fresh login.
