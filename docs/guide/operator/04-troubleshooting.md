# 04. Troubleshooting and FAQ

English | [日本語](04-troubleshooting.ja.md)

This chapter organizes, by symptom, how to triage "it came up but doesn't work" and "a user
says they can't use it." **The canonical recovery commands are in the "Troubleshooting" section
of [deploy/compose/README.md](../../../deploy/compose/README.md)**; this chapter expands on it
and adds diagnostic perspectives. One-or-two-line log checks and health checks are exceptionally
included here as well. The working directory is `deploy/compose/`.

## The first 2 places to look

- **CP logs**: `docker compose logs -f cp` (the reason for startup failures and
  authentication rejections is almost always here).
- **CP health**: whether `curl -s http://127.0.0.1:8099/healthz` returns `ok`.

## Symptom-based checklist

| Symptom | What to check |
|------|-------------|
| CP does not start | `docker compose logs cp`. Whether `curl -s http://127.0.0.1:8099/healthz` returns `ok` |
| "permission denied" on docker.sock | Whether `DOCKER_GID` matches the host's docker group GID (DooD constraint C) |
| Workspace starts but home is empty | Whether `DATA_DIR` is the same absolute path inside and outside the CP. The same path at restore time too (DooD constraint B) |
| Cannot reach a started Workspace | Whether both the CP and Caddy have `network_mode: host` (DooD constraint A) |
| Logins are always rejected | The allowlist is empty **and nobody is invited yet** (fail-closed). Set `AF_OAUTH_ALLOWED_DOMAINS` / `_EMAILS`, or invite somebody |
| One person is refused with "this tenant needs a different sign-in" | That tenant's **Login rules → Sign-in methods** does not include the IdP they used. Send them `/login/<slug>` |
| Somebody you removed can still work | Removing them at the IdP does not end the session. Take them off the roster (Admin → tenant → member → **Remove member**) or out of the allowlist |
| TLS certificate is not issued | Whether DNS A/AAAA point to this host. Whether 80/443 are reachable. Let's Encrypt rate limits |
| redirect URI mismatch | Whether the URI registered at the IdP matches `<PUBLIC_BASE_URL>/oauth2/callback` |
| A sign-in button is missing | That provider was disabled at startup. `docker compose logs cp \| grep -i "login provider"` names the missing setting |
| CP exits with a message about a multi-tenant issuer | An Entra `/common/` or `/organizations/` issuer with no `AF_OIDC_<ID>_ALLOWED_TIDS`. Pin the issuer to your tenant GUID |
| Every GitHub sign-in is rejected | The org has not approved the OAuth app (`grep "returned 403"` in the CP log), or the person's primary verified address is outside `AF_GITHUB_ALLOWED_DOMAINS` |
| GitHub users must sign in again after a CP restart | Expected. The org-membership cache is in memory; they are re-verified, not rejected |
| A tenant registered a sign-in method but no button appears | It is still *waiting for approval*, or it was approved and its settings are incomplete. Admin → the tenant → **Sign-in methods** says which, and so does the register under the tenant list (which also carries the approve/suspend buttons, so you do not have to walk into the tenant); `grep -i "tenant login provider"` in the CP log names the fault |
| "This email address is already used by another sign-in method" | A tenant-defined method asserted an address that already belongs to an account someone has signed in as. This is deliberate — that method may not take over an existing account. They sign in the way they normally do. The **same GitHub account** (deployment-wide GitHub ⇄ a tenant's GitHub) resolves to one person, so this only appears for a *different* IdP. If they hold **both accounts**, have them sign in the way they normally do and add the other method under **Settings → Personal → Account → Add a sign-in method** (only a method asserting the same address, and its own org / domain rules still apply). If somebody has no account on the other side at all, keep their method on that tenant's allowed list and, if the button is in the way, add it to "methods to keep off the sign-in page" |

## Diagnosing the 3 DooD constraints ("starts but silently doesn't work")

The CP is a container, but it drives the host's Docker daemon from the outside
(docker-out-of-docker). This approach has 3 constraints that, if broken, **fail silently
without producing errors**; the compose definition keeps them contained. Look here when you
have customized compose yourself, or when you want to narrow things down from symptoms. The
background on how this works is in [dev/09](../../dev/09-deploy.md).

- **(A) host network** — the CP publishes workspaces on `127.0.0.1:<port>` via the host daemon,
  so they are unreachable unless the host's loopback is shared. Both the CP and Caddy must have
  `network_mode: host`. **Symptom: the browser cannot connect to a started Workspace.**
- **(B) identical absolute path bind for `DATA_DIR`** — the CP passes host paths to the host
  daemon to create the Workspace's `-v` mounts, so `DATA_DIR` must resolve to the same absolute
  path inside the CP too. If they diverge, **an empty home gets mounted**. **Symptom: the
  Workspace starts but home is empty / the work is missing.** If this symptom appears after a
  restore, check whether the destination `DATA_DIR` diverges from the original in path (at
  least in basename) ([02](02-operations.md)).
- **(C) `user: "1000:1000"` + `group_add: <DOCKER_GID>`** — homes are created owned by uid 1000
  (the Workspace's `dev` user), and the CP needs the host's docker group to use the docker
  socket. With the wrong `DOCKER_GID`, you get **permission denied on the socket**. **Symptom:
  trying to start a Workspace yields permission denied, or the start itself fails.**

## Cannot log in

> Setting an IdP up in the first place — what to create at Google / Entra ID / GitHub / another
> OIDC IdP, which value goes where, and how to confirm it — is
> [05-login-idp.md](05-login-idp.md). Come here when it is configured and still refuses.

- **Always rejected** → if the allowlist (`AF_OAUTH_ALLOWED_EMAILS` / `_DOMAINS` /
  `_EMAILS_FILE`) is **entirely empty and nobody has been invited to a tenant, everything is
  rejected** (fail-closed = a fail-safe design). Set at least one, or add the person as a
  member. `_EMAILS_FILE` is re-read on every login, so additions need no restart; an invitation
  takes effect immediately too.
- **"This tenant needs a different sign-in"** (`provider_required`) → the tenant's **Login
  rules → Sign-in methods** does not list the IdP that session came from. This is not a fault:
  a session carries one provider, so moving to a tenant that requires another one means signing
  in again. The Console offers that link; `https://<PUBLIC_DOMAIN>/login/<slug>` shows exactly
  the methods that tenant accepts. If it is the *rule* that is wrong, fix it in the Admin panel.
- **A person you removed is still working** → removing their account at the IdP does not end
  the session they already hold; the signed cookie stays valid for up to `AF_SESSION_TTL`
  (7 days by default) and cannot be revoked individually. Take them off the roster (Admin →
  tenant → member → **Remove member**) or out of the allowlist — either takes effect on their
  very next request. To cut every session at once, rotate `AF_COOKIE_SECRET`
  ([03 §Offboarding](03-security.md)).
- **redirect URI mismatch** → check that the authorized redirect URI registered at the IdP
  (Google Cloud Console, the Entra app registration, …) is an **exact match** for
  `<PUBLIC_BASE_URL>/oauth2/callback`. If you change `PUBLIC_BASE_URL`, update the IdP side to
  match ([05 §1](05-login-idp.md)). There is only ever this one URI, however many providers you
  enable.
- **A sign-in button you configured is not on the login page** → that provider was disabled at
  startup because its settings were incomplete (one broken IdP must not lock everyone out).
  `docker compose logs cp | grep -i "login provider"` names the missing variable —
  most often `AF_OIDC_<ID>_TRUST`, which has no default on purpose.
- **The CP exits complaining about a multi-tenant issuer** → an Entra ID issuer of `/common/`
  or `/organizations/` with no `AF_OIDC_<ID>_ALLOWED_TIDS`. On those endpoints every Microsoft
  account on earth reaches your login, and personal accounts can change their own email address,
  so the allowlist would stop meaning anything. Pin the issuer to your tenant GUID
  (`https://login.microsoftonline.com/<tenant-guid>/v2.0`), or list the tenants you accept.
- **Every GitHub sign-in is rejected, but the settings look right** → most often the
  organization restricts third-party OAuth apps and nobody has approved yours yet; until an
  owner does, the membership check sees nothing.
  `docker compose logs cp | grep "returned 403"` says so explicitly. The other cause
  is the address: GitHub hands over the account's *primary verified* email, which may be a
  personal one and therefore outside `AF_GITHUB_ALLOWED_DOMAINS`. The person can make their
  company address primary and verify it on GitHub, or sign in with a different button.
- **GitHub users are asked to sign in again after a CP restart** → expected, not a fault. The
  org-membership answer and the token used to refresh it are held in memory only, so a restart
  leaves nothing to re-verify them with. They are asked to sign in again rather than told they
  are not allowed, and with a live GitHub session the round trip is usually invisible.
- **Cookie not saved / bounced back right after login** → `AUTH=oauth` uses Secure cookies, so
  **HTTPS is required**. Over plain HTTP (no TLS termination) they are not saved. Check that
  `PUBLIC_BASE_URL` is `https://`, and that TLS is actually being issued (below).

## TLS is not issued

When Caddy cannot obtain a certificate from Let's Encrypt, the usual causes are these 3: DNS
A/AAAA do not point to this host, 80/443 are not reachable from outside (firewall), or you hit
Let's Encrypt rate limits. In environments where public DNS is not available, such as air-gapped
networks, don't use ACME at all — switch to `tls internal` (self-signed)
([01 §4](01-install.md)).

## Triage flow for user inquiries

When someone says "it doesn't work," first determine **whether it is an individual member's
problem or a CP/deployment-wide problem**.

1. **Are other users having trouble at the same time?**
   - Yes → suspect the **CP/deployment side**. Check the CP's logs and health, TLS, the login
     allowlist, and the host's load (memory). If nobody can log in, look at the allowlist or
     the OAuth configuration; if nobody can connect, look at the entry point (Caddy/TLS) or
     DooD (A).
   - No (just that one person) → suspect a **member-specific issue**. Continue below.
2. **Triaging a single person's problem:**
   - Cannot log in → is that person on the allowlist, and if `AF_PROVISION=invite`, have they
     been added?
   - Can log in but their Workspace misbehaves → the state of that person's Workspace
     (`af-ws-<user>`). If home is empty, it's DooD (B) (though that should hit everyone); if
     Claude won't connect, it's a problem with that person's own Claude login (BYO), and the
     person themselves — not the operator — re-logs-in from the Console.
   - How to operate the Console itself → the scope of the member volume / lite volume (outside
     the operator's remit).
3. When you just cannot narrow it down, the CP's logs show what is happening for that user
   under their email (the sanitized `user_key`).

## FAQ (edge cases and common questions)

**Q. What happens if I lose `AF_MASTER_KEY`?**
A. All stored credentials and **every past backup become permanently undecryptable**
(crypto-shred). There is no recovery. That is precisely why you store it in a vault separate
from the data and back it up independently ([03](03-security.md)).

**Q. What goes into a backup, and what does not?**
A. Included: the DB, each user's home, plaintext Claude state, and Caddy certificates. Not
included: `shared/jvm` (re-fetchable) and **`AF_MASTER_KEY`**. Details in
[02](02-operations.md).

**Q. The Workspace starts but home is empty.**
A. Almost certainly DooD constraint (B). Check that `DATA_DIR` is the same absolute path inside
and outside the CP, and that the basename matched at restore time (the DooD diagnosis in this
document).

**Q. Can it be installed into an air-gapped network (no internet)?**
A. Yes, with caveats. Releases no longer ship an image tar, so either mirror the GHCR images
into an internal registry and point `REGISTRY` at it, or carry them in with
`release.sh --save` + `load-images.sh`. Use `tls internal` for TLS, and a baked-in image with
`CLAUDE_INSTALL=0` for Claude (the air-gap section of [02](02-operations.md)). Note that the
fleet will start but the agents cannot work without reaching their model endpoints.

**Q. I want to downgrade.**
A. Not supported. Migrations are forward-compatible and applied automatically, and an older CP
cannot understand the new schema. Rolling back is done not by "going back to the old image" but
by "restoring from the backup taken before the upgrade" ([02](02-operations.md)).

**Q. I ran `docker compose down` but Workspaces are still there.**
A. That is normal. Workspaces (`af-ws-*`) are outside compose management; the CP started them
with `docker run`. To stop them for sure, use force-stop in the Admin panel; or, if bringing the
whole host down, `docker stop` the remaining `af-ws-*` separately ([02](02-operations.md)).

**Q. Can it be distributed across multiple hosts (HA / horizontal scaling)?**
A. The delivery model is one company = one deployment = one host. The CP is premised on driving
the host's Docker daemon, and distribution across multiple hosts or HA configurations are out
of scope for now. For the design direction toward larger scale, see
[dev/09](../../dev/09-deploy.md) (the aws target is implemented but has no production track
record).

**Q. I want to use authentication other than Google (Microsoft 365 / LDAP / SAML, etc.).**
A. Natively (`AUTH=oauth`) the CP speaks OIDC, so **Microsoft Entra ID, Okta, Keycloak, Auth0,
Cognito and GitLab work with configuration alone** — `AF_OIDC_PROVIDERS` plus a few
`AF_OIDC_<ID>_*` variables, and one redirect URI at the IdP ([05](05-login-idp.md)). You can
enable several at once; the login page then shows one button per provider.
SAML-only IdPs (HENNGE One / TrustLogin / CloudGate, etc.) and LDAP are not implemented in the
CP: put an existing gateway (oauth2-proxy / Keycloak / ALB OIDC) in front and have the CP trust
the upstream email header with `AUTH=proxy` ([dev/07 §7.3](../../dev/07-security.md)).
