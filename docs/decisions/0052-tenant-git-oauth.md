# 0052. The **tenant admin** registers the git providers' (GitHub / Bitbucket) OAuth apps

English | [日本語](0052-tenant-git-oauth.ja.md)

- Status: **adopted** (2026-08-22). The record of the investigation is [docs/71](../log/71-tenant-git-oauth.md).
  Amended 2026-10-04 (issue #1667): GitHub gains built-in apps and app-kind detection — decisions 8
  and 9, which partly supersede decisions 1 and 2.
  Amended 2026-10-08 (issue #1676): the Agent now renews expiring GitHub App user tokens — see the
  dated note under decision 8's last bullet.
- See also: [0043-login-idp.md](0043-login-idp.md) decisions 29/30 (a tenant-defined IdP — the side
  that **requires approval**) and decisions 24/25 (what reaches outside the tenant belongs to the
  operator; what stays inside belongs to the tenant admin) /
  [0047-tenant-network-restriction.md](0047-tenant-network-restriction.md) decision 6 (the same line)

## Context

There was one "connect with OAuth" app per deployment: GitHub in the workspace's env
(`GITHUB_OAUTH_CLIENT_ID`) and Bitbucket in the CP's env
(`BITBUCKET_OAUTH_KEY`/`_SECRET`). But the app actually lives in each company's GitHub org or
Bitbucket workspace, and the operator held exactly one of something that naturally differs per
tenant.

## Decision 1 — the setting goes on the tenant's row. **No per-deployment setting**

`tenant_git_oauth(tenant_id, provider)`. No new operator-facing UI and no new env.

On a single-tenant deployment (native / compose) the default tenant's row effectively becomes the
deployment setting. Rather than having two layers, sharing one layer across every configuration keeps
both the explanation and the route singular.

🔴 **Amended 2026-10-04 (#1667):** for GitHub the row may now name a **built-in app** compiled into
the binary instead of a client_id, and the operator gains one switch, `AF_GITHUB_BUILTIN_APPS=off`,
that withdraws the built-in apps from every tenant. See decision 8.

## Decision 2 — the env is **not even a fallback**, and nothing is migrated

Keeping "fall back to env when the row is missing" makes *which app you are sent to* **vary by
tenant**. When someone asks why the button is missing or why they landed on a different app, there
are two places to look.

Automatic migration at startup (env → the default tenant) is not added either. It would make
"the env is not read" **a lie at startup only**, and on a deployment that forgot to delete `.env` a
row would reappear on every restart. The price for a running deployment is only "the OAuth button is
missing until the tenant admin registers it again" — pasting a token and existing connections keep
working.

🔴 **Amended 2026-10-04 (#1667):** the deployment's **default tenant** with no GitHub row now gets the
built-in OAuth App. This is not the fallback this decision rejected: the value is in the binary, not
in an env a deployment forgets about, and the tenant settings screen always shows the effective
choice, marked "(default)" — there is still one place to look. Every other tenant with no row still
has no button. See decision 8.

## Decision 3 — **no approval required** (unlike `tenant_idp`)

`tenant_idp` requires super_admin approval because registering an IdP is **the authority to declare
who someone is** (0043 decision 30). A git OAuth app does not carry that:

- It adds no identity. No button appears on the login screen, and neither user_key nor the deployment
  role moves.
- The `redirect_uri` is **fixed and owned by the CP**. Registering an attacker's app cannot send the
  grant elsewhere.
- The resulting token goes only into **the workspace of whoever pressed the button**; it never comes
  back to the admin who registered it.
- And a deployment with `AUTH=dev` has no super_admin to approve anything (decision 5). Requiring
  approval would need an exception rule for "permanently pending" in that configuration.

Stopping something should always be the faster path, so a tenant admin can delete it too.

## Decision 4 — move GitHub's device flow **from the Agent to the CP**

Two reasons for discarding the idea of distributing a per-tenant client_id via container env.

1. The env is fixed **when the container starts**, and there are **four implementations, one per
   runtime** (docker / native / ecs / ecs-ec2).
2. **Applying it requires restarting every member's workspace.** That is most likely to bite right
   after the first registration.

Running it on the CP needs no wiring, applies immediately, and matches Bitbucket's shape. The paths
(`/api/connections/git/github/oauth/{start,poll}`) stay, and the acquired token is handed to the
Agent's `PUT /connections/git/github.com` (the same entrance as pasting a PAT). The Agent's device
flow handler and `githubClientID()` are **deleted** — leaving them keeps a path that reads the env
alive, making decision 2 a lie.

## Decision 5 — treat `AUTH=dev`'s fixed user as **super_admin**

`deploy/native/af` is fixed at `AUTH=dev`, and that identity **has no email**.
`SUPER_ADMIN_EMAILS` matches on addresses, so on native / WSL a super_admin **could not exist in
principle**. That did not matter while all the settings were env, but after decision 1 it becomes "a
deployment where nobody can configure anything".

`AUTH=dev` is an unauthenticated single fixed user, i.e. the host's owner, so this is not granting a
privilege but **mirroring the mode's reality into a role**. With an empty email it is not caught by
`DemoteSuperAdmins` either (which only targets `email <> ''`), so it does not get stripped on restart.

## Decision 6 — the secret is **write-only**, but an empty value is refused the first time

A stored `client_secret` is never returned (the same contract as `tenant_idp` and `mcp_server`).
Saving with it empty means "leave it unchanged". But **the first time** for a provider that needs a
secret, empty is refused (`secret_required`) — being able to save it empty creates a row that looks
registered on screen but fails at token exchange, and the failure is only visible as Bitbucket's
`invalid_client`.

For GitHub the rule goes the other way: if a secret is supplied it is **not stored**. The device flow
authenticates with the client_id alone, so storing it would only add "a credential nobody reads and
nobody rotates".

## Decision 7 — run Bitbucket's **refresh grant on the CP too** (do not distribute the client_secret)

A refresh grant does Basic auth with the OAuth app's key:secret. Previously the CP handed the
key/secret to the Agent at connection time and the Agent ran it itself, which meant **the tenant's
client_secret was copied into every member's `secrets.enc`**. While it was the operator's app that
was merely "the operator's secret in a container the operator made", but once decision 1 made the
tenant admin the app's owner, it becomes someone else's credential sitting on someone else's disk.

`POST /internal/git-oauth/bitbucket/refresh` is added, authenticated by a per-membership
`AF_GIT_OAUTH_TOKEN` (the same shape as the other bridges, with **a separate signing key**). The
tenant is **derived from the token** (the request does not get to choose).

★ **The refresh token does not move.** It stays in the workspace and the CP does not store it. Keeping
"the CP passes secrets through and does not hold them", the split becomes **the tenant's secret on
the CP, the person's own token in the workspace**. Less is lost when something breaks than if
everything were collected on the CP.

The migration order is **confirm it works, then delete**: the key/secret in the existing store is
discarded **once the bridge has succeeded once**, and the old path is used only when the bridge fails
**and** the old values are still there. New connections have no old values, so the fallback disappears
structurally within one generation.

There are two prices, both accepted. (1) A refresh needs the CP to be reachable (the access token is
valid for ~2h, so a CP restart is invisible). (2) The Agent in a container started before docs/71
requires key/secret in its save API, so **one "stop and start the workspace" is needed once during the
upgrade window** (the CP swaps in wording that says so).

## Decision 8 — ship **two built-in GitHub apps**; the tenant picks one, its own, or none (2026-10-04, #1667)

A personal native / WSL install had no OAuth button until its owner created an OAuth App on GitHub,
ticked Device flow and pasted the client_id. Two project-owned apps now ship with the release:

| Source | client_id | Suits |
|---|---|---|
| `builtin_oauth` — built-in OAuth App | in the binary | individuals: one authorization, scopes `repo workflow` |
| `builtin_app` — built-in GitHub App | in the binary | small teams: install, choose repositories, authorize; fine-grained permissions |
| `custom` — the tenant's own | the row | organisations that run their own OAuth App **or** GitHub App |
| `none` | — | no button; members paste a token |

- **Why compiling them in is safe.** The device flow authenticates with the client_id alone, so
  there is no secret to leak (gh does the same). The token goes to the member's workspace, never to
  the app's owner.
- **Why the device flow everywhere, even where a callback would work.** A bundled app cannot register a
  callback for every deployment's own domain (GitHub allows ten per app). On native the loopback
  callback would work, but the code exchange needs the client_secret, which would then ship in the
  binary. The device flow is the one flow every topology shares.
- **Why both kinds.** A GitHub App is the narrower grant (per-repository installation, fine-grained
  permissions, org-managed), but the extra install step is a real barrier for an individual; an OAuth
  App is one click with a broad `repo` scope. Neither is right for everyone, so the tenant chooses.
- **The default** is `builtin_oauth`, for the deployment's default tenant only (amendment to decision 2).
- **Where the client_ids live.** `github_builtin_apps.go`, overridable with `-ldflags -X`. A build
  without them (a fork) offers no built-in source; a row that names one then resolves to "no button",
  and the screen says why.
- **The operator's switch** `AF_GITHUB_BUILTIN_APPS=off` removes both built-in apps from every tenant,
  for organisations that must not send members to a third-party app. It is a deployment setting
  because it reaches outside the tenant (0043 decisions 24/25).
- **A GitHub App reaches only where it is installed.** After a grant the CP asks
  `GET /user/installations`; with none, the member is told at once with the install link instead of
  meeting failing clones later. GitHub exposes no way to learn a GitHub App's install page from its
  client_id, so for a custom GitHub App the tenant admin enters `https://github.com/apps/<name>`.
- **Expiring user tokens are not renewed yet.** A GitHub App with "Expire user authorization tokens"
  on returns an 8-hour token and a refresh token. A device-flow token can be refreshed without a
  client_secret, but af stores only the access token today, so the connection is made, the member is
  warned, and the admin form says to switch expiration off. Renewal is #1676.
  🔴 **Amended 2026-10-08 (#1676):** the bullet above describes 2026-10-04 and no longer holds. The
  CP now hands the refresh token, both lifetimes and the app's client_id to the member's Agent next
  to the access token; the Agent renews the access token itself, shortly before expiry and on a 401,
  with `POST /login/oauth/access_token` (`client_id`, `grant_type=refresh_token`, `refresh_token`) —
  no client_secret is stored anywhere. Refresh tokens are single use, so the grant runs under a
  cross-process lock and the new pair is written before it is used. When GitHub refuses the refresh
  token (revoked, or the 6 months ran out) the connection is marked "reconnect needed" in
  Connections. A token with no refresh token behaves as before. The admin form no longer asks to
  switch expiration off; the warning after connecting remains only for a workspace whose Agent
  predates renewal.

## Decision 9 — the kind of a custom app is **detected**, not asked (2026-10-04, #1667)

The CP has to know whether a client_id is an OAuth App or a GitHub App (scope handling, the
installation check), and asking the admin invites a wrong answer that fails far from where it was
typed. Three signals, strongest first:

1. **At save:** `POST /login/device/code` with a scope that does not exist. Measured on github.com: an
   OAuth App answers `invalid_scope` (no device code is created); a GitHub App ignores scopes and
   returns a device code, which expires unused; an unknown client_id answers `Not Found`, and an app
   without Device flow `device_flow_disabled` — both are refused at save, because saved they would
   look configured and fail only when a member presses the button.
2. **After every grant:** the token prefix, which GitHub documents — `gho_` OAuth App, `ghu_` GitHub
   App. It corrects the row (only while the row still names that client_id).
3. **Fallback when GitHub is unreachable at save:** the client_id's shape (`Ov23…` / 20 hex → OAuth
   App, `Iv1.…` / `Iv23…` → GitHub App). GitHub does not document these, so the screen shows the
   result as an estimate.

The row records which signal decided (`app_type_by`: `probe` / `token` / `prefix`).

## Impact

- `BITBUCKET_OAUTH_KEY` / `_SECRET` stop being read. The CFN references to `BitbucketOauthKey` and
  `<SsmPrefix>/bitbucket-oauth-secret` are removed.
- `GITHUB_OAUTH_CLIENT_ID` **remains but changes meaning**. From now on it is for GitHub **sign-in**
  only and is not injected into workspaces (docs/61 §61.7's premise that "git integration is already
  using this env" ends here).
- The admin modal now opens on an `AUTH=dev` deployment (it did not before).
- The workspace gains one `AF_GIT_OAUTH_TOKEN`, and Bitbucket's `key`/`secret` disappear from
  `secrets.enc` (at the next refresh). In exchange, a refresh now depends on the CP being reachable.
- (#1667) `tenant_git_oauth` gains `source`, `app_type`, `app_type_by` and `install_url`
  (migration 0088 / pg 0073). Existing rows are `custom`. A new env `AF_GITHUB_BUILTIN_APPS`.
  Each GitHub OAuth connection writes a `git_oauth.github_connect` audit row naming the source and
  client_id.
