# 0052. The **tenant admin** registers the git providers' (GitHub / Bitbucket) OAuth apps

English | [日本語](0052-tenant-git-oauth.ja.md)

- Status: **adopted** (2026-08-22). The record of the investigation is [docs/71](../log/71-tenant-git-oauth.md).
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

## Decision 2 — the env is **not even a fallback**, and nothing is migrated

Keeping "fall back to env when the row is missing" makes *which app you are sent to* **vary by
tenant**. When someone asks why the button is missing or why they landed on a different app, there
are two places to look.

Automatic migration at startup (env → the default tenant) is not added either. It would make
"the env is not read" **a lie at startup only**, and on a deployment that forgot to delete `.env` a
row would reappear on every restart. The price for a running deployment is only "the OAuth button is
missing until the tenant admin registers it again" — pasting a token and existing connections keep
working.

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

## Impact

- `BITBUCKET_OAUTH_KEY` / `_SECRET` stop being read. The CFN references to `BitbucketOauthKey` and
  `<SsmPrefix>/bitbucket-oauth-secret` are removed.
- `GITHUB_OAUTH_CLIENT_ID` **remains but changes meaning**. From now on it is for GitHub **sign-in**
  only and is not injected into workspaces (docs/61 §61.7's premise that "git integration is already
  using this env" ends here).
- The admin modal now opens on an `AUTH=dev` deployment (it did not before).
- The workspace gains one `AF_GIT_OAUTH_TOKEN`, and Bitbucket's `key`/`secret` disappear from
  `secrets.enc` (at the next refresh). In exchange, a refresh now depends on the CP being reachable.
