---
audience: "the deployment administrator removing people and handing over the role (whoever edits the host's `.env`)"
source_of_truth: "the Control Plane's per-request check and PAT revocation (`control-plane/resolver.go`, `control-plane/pat.go`), how `SUPER_ADMIN_EMAILS` is read"
---

# Revoke access and hand over super_admin

English | [日本語](offboarding.ja.md)

Stop the access of someone who left or was removed, at the place where it actually stops. This page also covers ending everybody's sessions at once and handing over the deployment administrator (super_admin) role.

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Remove them from the allowlist or the tenant membership | Shuts them out on the next request and revokes their PATs and MCP OAuth connections |
| Stop the workspace and clean the home | Clears what is left behind |
| When it is urgent, replace `AF_COOKIE_SECRET` | Invalidates everybody's sessions on the spot |
| Rewrite `SUPER_ADMIN_EMAILS` and restart | Removes super_admin from anyone not listed, in the database too |

## Where access actually stops

There are two ways in, so there are two ways out. Which one applies depends on how the deployment admits people.

| How they got in | How to remove them | When it takes effect |
|---|---|---|
| The allowlist (`AF_OAUTH_ALLOWED_*`) | Delete them from the list | The next request (checked on every request) |
| A tenant membership (invitation) | Admin → the tenant → the member → "**Remove member**" | The next request |

**There is no way to revoke a single session.** A session cookie holds only a signed `{email, exp}` and the server keeps no session store, so there is no "sign out everywhere" either.
The cookie itself stays valid for up to `AF_SESSION_TTL` (7 days by default); what actually stops access is the check on every request.

> [!WARNING]
> **Disabling the account at the IdP is not enough.** Stopping the Microsoft or Google account prevents new sessions, but
> **a cookie already in a browser keeps working until you remove them from the allowlist or the tenant.**

## API tokens end with it

Personal access tokens (PATs) and MCP OAuth connections are not sessions, so the per-request check does not cover them. They end another way.

| How you remove them | What happens to the tokens |
|---|---|
| Remove the membership | The PATs and OAuth connections issued under that membership are **all revoked** by the same action. Inviting them again does not bring them back |
| Delete them from the allowlist | The next time a token is used, the Control Plane asks the sign-in methods they used whether they may still get in. If none lets them, it **revokes all** their PATs and connections and returns 401 |

- Only a definite refusal revokes. When it cannot tell (a GitHub adapter right after a restart or during an outage, an unreadable allowlist file, and so on), nothing is revoked
- **Using** a PAT on `/mcp` is outside the tenant's allowed networks, because workspaces call it from outside the office. Ending it is revocation's job

### How long a PAT lives

| Rule | Detail |
|---|---|
| No PAT is forever | The longest is `AF_PAT_MAX_DAYS`: 365 days by default, 1 to 3650. Any other value stops the Control Plane from starting |
| Tokens made before the cap | A token issued with no expiry is treated as expiring the cap's number of days after it was created, so old ones stop working at once |
| A tenant can lower it | The deployment administrator sets "**Max PAT lifetime**" in the tenant's limits (1 to 3650 days; 0 means no tenant cap). The **lower** of it and `AF_PAT_MAX_DAYS` applies at issue and on every use, counted from creation |

- Lowering it stops tokens older than the new cap at once
- The cap that applies is the issuing tenant's; a super_admin's token follows that tenant's too. A token tied to no membership gets `AF_PAT_MAX_DAYS` only
- When the tenant's value is unset, unreadable or out of range, the deployment-wide cap applies, never "no cap". When the tenant itself cannot be read, the token is refused
- A tenant cannot go longer than `AF_PAT_MAX_DAYS`

## Remove someone

**Only step 1 cuts access.** Everything after it is cleanup.

1. Remove the membership (on an allowlist-run deployment, delete them from the list)
2. Stop the workspace (Admin → the member's details → "**Force-stop the workspace**")
3. Clean the home

The steps in the member's details, and what cleaning the home leaves, are in [Remove a member](../../admin/members/remove.md).

- Have them push before you clean the home. `~/repos` cannot come back after the cleaning
- On a deployment with no home cleaning, discarding the workspace is this step ([Deployment targets](../../ref/deploy-targets.md))

Two differences are too late to discover after someone has left. Know them **in advance**.

| What | Owned by | When the person leaves |
|---|---|---|
| Schedules | The person (tied to the membership) | **They stop.** Whoever takes over recreates them |
| Internal git repositories | The tenant | They stay |

## End everybody's sessions now

When you need to invalidate everybody's sessions right now (a leaked cookie, a lost device, an account you cannot reach), the only switch that works on the spot is changing the cookie signing key.

1. Generate a new random value ([Deploy with Docker Compose](../compose/install.md#1-configure-and-start-from-source) has a one-liner for `AF_COOKIE_SECRET`)
2. Put it in `AF_COOKIE_SECRET` (`.env`, `oauth.env`, the SSM parameter)
3. Restart the Control Plane

Every cookie signed with the old key fails verification: **everybody is signed out and signs in again**. It is blunt, but it is the only thing that works in seconds.

**It does not take access away.** Anyone still on the allowlist or holding a membership can sign in again. Use it **together with** the removal steps above.

## Hand over super_admin

`SUPER_ADMIN_EMAILS` (the host's environment) is the one place that decides who administers this deployment. It is read **once, at start**, so a change needs a Control Plane restart.
There is deliberately no way to make someone super_admin from the Console: the power to change the whole deployment should belong only to people who can touch the host's files.

1. Rewrite `SUPER_ADMIN_EMAILS` (add the successor, remove the predecessor) and restart the Control Plane
2. The Control Plane **removes the role in the database from anyone not listed** and logs `super_admin revoked (not in SUPER_ADMIN_EMAILS): …`
3. The successor gets the role on their first sign-in
4. Remove the predecessor with the steps above (membership → stop → home)

Step 2 exists because syncing at sign-in would never reach a leaver who no longer signs in.

> [!NOTE]
> Even if the only super_admin leaves without handing over, you can recover: someone who can edit the host's environment adds themselves and restarts.
> The only precondition is **at least one person in the company who can touch the host**. Confirm that before you need it.

## Related

- [Remove a member](../../admin/members/remove.md): the tenant administrator's steps
- [Cannot log in](../diagnose/cannot-login.md): when someone you removed can still get in
- [Other operational controls](controls.md#keep-the-sign-in-allowlist): the allowlist rules
- [Handle the deployment keys](deployment-keys.md): what `AF_COOKIE_SECRET` must be
