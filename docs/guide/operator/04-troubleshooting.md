# 04. Troubleshooting and FAQ

English | [日本語](04-troubleshooting.ja.md)

This chapter organizes, by symptom, how to triage "it came up but doesn't work" and "a user
says they can't use it." **The canonical recovery commands are in the "Troubleshooting" section
of [deploy/compose/README.md](../../../deploy/compose/README.md)**; this chapter expands on it
and adds diagnostic perspectives. One-or-two-line log checks and health checks are exceptionally
included here as well. The working directory is `deploy/compose/`.

## The first 2 places to look

- **CP logs**: `docker compose logs -f control-plane` (the reason for startup failures and
  authentication rejections is almost always here).
- **CP health**: whether `curl -s http://127.0.0.1:8099/healthz` returns `ok`.

## Symptom-based checklist

| Symptom | What to check |
|------|-------------|
| CP does not start | `docker compose logs control-plane`. Whether `curl -s http://127.0.0.1:8099/healthz` returns `ok` |
| "permission denied" on docker.sock | Whether `DOCKER_GID` matches the host's docker group GID (DooD constraint C) |
| Workspace starts but home is empty | Whether `DATA_DIR` is the same absolute path inside and outside the CP. The same path at restore time too (DooD constraint B) |
| Cannot reach a started Workspace | Whether both the CP and Caddy have `network_mode: host` (DooD constraint A) |
| Logins are always rejected | The allowlist is empty (fail-closed). Set `AF_OAUTH_ALLOWED_DOMAINS` / `_EMAILS` |
| TLS certificate is not issued | Whether DNS A/AAAA point to this host. Whether 80/443 are reachable. Let's Encrypt rate limits |
| redirect URI mismatch | Whether the URI in Google Console matches `<PUBLIC_BASE_URL>/oauth2/callback` |

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

- **Always rejected** → if the allowlist (`AF_OAUTH_ALLOWED_EMAILS` / `_DOMAINS` /
  `_EMAILS_FILE`) is **entirely empty, everything is rejected** (fail-closed = a
  fail-safe design). Set at least one. `_EMAILS_FILE` is re-read on every login, so additions
  need no restart.
- **redirect URI mismatch** → check that the authorized redirect URI in Google Cloud Console
  is an **exact match** for `<PUBLIC_BASE_URL>/oauth2/callback`. If you change
  `PUBLIC_BASE_URL`, update the Google side to match ([01 §3](01-install.md)).
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

**Q. I want to use authentication other than Google (LDAP / SAML, etc.).**
A. Natively, the CP supports Google OAuth (`AUTH=oauth`). If you put an existing authentication
gateway (oauth2-proxy / ALB OIDC, etc.) in front and have the CP trust the upstream email
header with `AUTH=proxy`, other IdPs can be used indirectly
([01 §3](01-install.md) / [dev/07 §7.3](../../dev/07-security.md)).
