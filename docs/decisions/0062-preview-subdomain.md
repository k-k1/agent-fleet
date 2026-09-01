# 0062. Serve previews from a random subdomain minted on every start, with the port prefixed into the label — and keep the path route

English | [日本語](0062-preview-subdomain.ja.md)

- Status: **accepted — P0 to P3 implemented** (2026-08-31). What is left is **an end-to-end check
  against real apps** (Next.js and Vite, HMR and Server Actions in particular — docs/81 §13). The
  design and the history are in [docs/81](../log/81-preview-subdomain.md).
- Related: [0018-container-browser-pane.md](0018-container-browser-pane.md) (the workspace's own
  Chromium — the "look from inside" route; this is the "look from outside" route, and the two stand
  side by side) / [0047-tenant-network-restriction.md](0047-tenant-network-restriction.md) (a
  tenant's CIDR restriction applies to previews too) /
  [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md) (a
  connection left hanging open is not activity) /
  [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) (the ecs-ec2 profile)

## Background

The ordinary two-port setup — React on 3000, Spring Boot on 8080 — should be viewable as-is on an
ecs-ec2 deployment. The current lightweight preview is path-based (`/preview/{port}/…`) and fails
that shape in three places: ① the absolute paths an app emits land outside the sub-path, ② two
ports share one origin, ③ WebSocket does not pass through.

A premise arrived later: **the front end is usually Next.js.** Next insists harder on being served
at the root (the Server Actions origin check, `/_next/*`, App Router streaming), which only
strengthens the case for the host route. Its own constraints are reflected in decisions 3, 9 and 11.

## Decision

### 1. **Add** the host route. Do not remove the path route

The host route cannot exist on a deployment without wildcard DNS and a certificate (local / docker /
native, and an ecs deployment with no `PreviewDomain`). **Do not create a state where only one of
them exists** — "a feature that is silently missing depending on the deployment profile" is a shape
this product has walked into many times.

### 2. The URL is `{slug}-{port}.{PreviewDomain}`

The port is **prefixed inside the label**, because an ACM wildcard certificate only covers **one
label**. `{port}.{slug}.…` would require `*.*.…`, which cannot be issued, and a per-workspace
certificate adds minutes to a start and runs into quotas.

**State the price:** `{slug}-3000` and `{slug}-8080` are siblings, so there is nowhere to put a
cookie shared by just that workspace's previews (their common parent is the common ancestor of every
workspace). The auth cookie is therefore one per host (decision 7).

### 3. The slug is minted on every workspace start and expires on stop

No long-lived URLs. The previous URL returns 404 the moment the workspace starts again. It is 20
characters of `[a-z0-9]` (no `-`), with a unique constraint in the database.

★ **Do not mix the tenant name, the member name or the workspace id into the slug.** These URLs get
pasted into Slack and into tickets. Being unguessable matters as much as not revealing whose it is.

**There is, however, a per-workspace opt-in to pin the slug (off by default).** Re-minting on every
start has exactly one real cost, and it is **registering redirect URIs with an external IdP** (a
NextAuth / Auth.js setup trying Google or GitHub sign-in). A redirect URI accepts neither a prefix
match nor a wildcard, so there is nothing to work around on our side. Turning it on does not make
the slug any less random — the only thing it changes is whether it is re-minted each start.

★ **Not the other way round (pinned by default, re-minted on request)** — the accident of forgetting
only happens on the "I forgot it was pinned" side.

### 4. The certificate is one `*.{PreviewDomain}` wildcard, attached to the listener **separately**
from the default certificate

Adding it as a SAN on the existing `Cert` (for the Console's FQDN) means **replacing the
certificate**. There is no reason to re-issue the TLS the Console is already serving just because we
are adding previews. An ALB listener can pick an additional certificate by SNI, so it goes on as a
second one via `AWS::ElasticLoadBalancingV2::ListenerCertificate`. Removing it later is just
detaching it; the default certificate never moves.

On a deployment with an empty `PreviewDomain`, the certificate is not created at all (a `Condition`).

### 5. Neither DNS nor the certificate is touched per mint

One wildcard A alias plus one wildcard certificate covers every workspace and every port. **Zero
minting cost and zero propagation delay** is one of the few outright wins of the host route, so the
design does **not** write a Route53 record per workspace (that would put an API round trip and a
propagation wait into every start, and hit the record-count quota).

### 6. The exposed ports are listed by the user in the workspace settings (3000, 8080 by default)

A subdomain for a port that is not on the list returns **404** — not "not permitted"; it does not
even admit it exists (nobody probes from outside for which ports are allowed).

**All ports are not opened by default.** The point is to avoid exposing services that happen to be
running (a database console, a debugger, an MCP server), not to save the user from typing a port
number.

### 7. Authentication is required by default, and the Console's session cookie is never handed to the preview origin

What runs in a preview is arbitrary code the user wrote. Handing the Control Plane's session to it
would hand that code the right to call the API as the user. **It is a separate origin, so the
browser does not send it in the first place** — the problem the path route was papering over by
stripping headers disappears structurally.

Instead, a handshake through the Console's origin issues an HttpOnly cookie **scoped to the preview
host**. The cookie is **signed with the slug included**, so it invalidates itself as soon as a
restart changes the slug. `SameSite=Lax` by default.

### 8. On a preview host, neither the Control Plane API nor the Console is served (everything is 404)

Since one process owns both, loosening this reopens from the back door the hole decision 7 closed.
Only the single handshake path and the proxy itself answer. The split sits **outside `authGate`**
(previews carry their own authentication) and **outside `gzip` / `etag`** (the payload is not the
Control Plane's JSON).

### 9. Keep sending `Host: 127.0.0.1:{port}` upstream and carry the public name in `X-Forwarded-*`

This passes host checks such as Vite's `server.allowedHosts` **without asking the user to configure
anything**. If the app builds an absolute URL by trusting Host, that URL is an internal address and
never leaks outside. **`X-Forwarded-Prefix` is not sent** — on the host route, the app is at the
root.

★ **This combination is not a preference; there is exactly one point where both frameworks work.**
Next.js validates that **`Origin` matches `x-forwarded-host`** for Server Actions and returns 403
otherwise (the best-known accident with Next behind a reverse proxy). Forget `X-Forwarded-Host` and
Next breaks; rewrite `Host` to the public name and Vite's host check breaks. **Any combination other
than "Host internal, X-Forwarded-Host public" breaks one framework or the other.**

### 10. Turn the Control Plane's proxy into a ReverseProxy so WebSocket and SSE pass through

Even on the host route, "view the React app" is only half-met if HMR does not work. The existing
path route rides the same implementation (we do not keep two copies of the same weakness).

### 11. Require no AF-specific code in the app. "The same configuration as on a local PC" is the acceptance criterion

The first recommendation is **routing `/api` to 8080 through the dev server's `server.proxy`**,
which works **from the same configuration file** on a local PC and in the preview alike (the browser
knows one origin, so neither CORS nor SameSite ever enters the picture).

Hard-coding `http://localhost:8080` **does not work in a preview** (the browser's `localhost` is the
machine of the person looking at the screen). For apps that read their environment, the minted
values are injected into the container as `AF_PREVIEW_URL_{port}` and friends.

**In Next.js, `rewrites()` in `next.config.js` plays the same role and works under `next start`**
(the production build) as well, which is a better position than Vite's dev-only `server.proxy`.

★ **The env injection is P0, not decoration to add later.** Next.js apps commonly **learn their own
public URL from the environment** (`NEXTAUTH_URL`, `AUTH_URL`, `metadataBase`), and since the slug
changes on every start, missing env produces the half-broken state where "the URL is there but the
app is wrong about where it is".

For setups that really do call across origins, `SameSite=None` plus a CORS completion on the Control
Plane side **restricted to sibling origins of the same slug** is **opt-in and off by default** —
allowing cross-origin by default would mean that, by default, a third party's page that knows the
URL can drive the preview through the user's browser.

### 12. Public mode (openable without signing in) is per workspace and always returns to off on stop / restart

Showing something to an outsider is a real need, so it exists. But it is **fail-closed** — almost
the only accident this feature has is "forgetting it was left public". The toggle is written to the
audit log, `X-Robots-Tag: noindex` is set while it is public, and the Console shows the public state
at all times.

**A tenant's CIDR restriction ([0047](0047-tenant-network-restriction.md)) applies in public mode
too.** If a tenant has narrowed its network, previews are on the narrowed side.

### 13. Recommend a `PreviewDomain` that is a **sibling** of the Console's FQDN, not a child

As a child (`*.af.example.com`), an app running in a preview could write a domain cookie on
`.af.example.com` and so overwrite or fixate the Console's cookie. The current code strips the
Control Plane's and the auth gateway's cookie names out of responses, which limits the damage, but
**it is better not to have the structure at all**. Full separation needs a different registered
domain (as long as the registrable domain is shared, a `.example.com` cookie can be written).

⚠️ **When the Console's zone IS the Console's own name** (`af.example.com` is itself a delegated
zone), the sibling falls *outside* that zone, so 30-ingress carries `PreviewHostedZoneId` (empty =
`HostedZoneId`) and points **only the preview certificate's validation and wildcard A record** at
another zone — otherwise the shape this decision recommends could not be expressed by the template
at all. A sibling then needs its own hosted zone plus an `NS` delegation from the parent, which is
a request someone else has to complete when the parent is managed elsewhere
([docs/81 §10.2](../log/81-preview-subdomain.md)). Decision 14 widened who browses a member's own
code, so choosing the child shape now means accepting that cookie reach knowingly.

### 14. Sharing within a tenant is a per-workspace toggle, not a table of named grantees

There was nothing between "only the owner" and "anyone, unauthenticated", so people were using
public mode to show something to colleagues. `previewTenantShare` (off by default) is added: while
it is on, **every active member of the same tenant as that workspace** can open it **while staying
authenticated** ([docs/81 §14](../log/81-preview-subdomain.md)).

Putting it in [docs/59](../log/59-session-sharing.md)'s `session_share` as `scope_type='preview'`
was considered and rejected. The row would fit, but (a) that list deletes rows whose
`scope_type != 'session'` unless `scope_key` is a live working copy, so **it would be removed as
fast as it is added**, (b) there is no concept matching `permission`'s `ro | rw` (**a web app is
operable the moment it opens**), and (c) naming grantees brings revocation with it, while the people
being shown are colleagues inside the tenant. ★ The toggle is a one-line ACL that says "everyone",
so it can move to a table the day granularity turns out to be needed.

⚠️ **Unlike decision 12, it does not return to off on every start.** Public mode's fail-closed
posture guards against "leaving it open to the world and forgetting"; that does not apply when the
audience is **a colleague who can already sign in**. The use case (letting someone look over a few
days) necessarily spans restarts, so resetting it every time would make the feature unusable. The
URL itself keeps changing per start (decision 3), which is the safety floor.

### 15. A viewer's permission is not baked into the cookie; it is resolved on every request

The preview cookie (`af_pv`) carries only **who** is trying to open that slug/port — never "is
allowed to". Every request re-resolves whether `previewTenantShare` is still on (settings) and
whether that membership is still active and in the same tenant (`GetMembershipByID` only returns
active rows).

★ **Bake it in, and turning sharing off — or removing someone from the tenant — leaves the cookie's
12-hour lifetime running.** The cost is one local database query, two orders of magnitude below the
Control Plane → Agent → app round trip behind it.

### 16. A viewer's access counts as activity (and the cost lands on the owner), but nobody can start someone else's workspace

- **It counts** — a screen someone is watching must not drop out from under them (no separate
  decision; the extension of docs/81 §9). ⚠️ The cost of keeping it alive lands on the owner. That
  is not a new property — **public mode already worked this way** — and all this changes is who may
  pass. The brake is unchanged from §9: **a WebSocket left hanging open does not count**, so a page
  opened and abandoned eventually falls to the idle stop.
- **It cannot start one** — a stopped workspace has no slug, so it stays a 404 and is never wired to
  an auto-start. Wiring it would mean "**someone else can start someone else's container on someone
  else's bill**", which is not a permission included in showing a preview.

### 17. The URL you share is served by a fixed redirector on the Console origin

The slug changes on every start (decision 3), so **pasting a raw URL is guaranteed to go stale, and
it goes stale as a 404 — the least legible failure there is**.
`GET /preview-open?owner={userKey}&port={n}` sits on the Console origin (inside authGate) and
302s to the **current** `/preview-auth` only when the ACL passes.

- Token minting stays in exactly one place, `/preview-auth`. The redirector only resolves "owner →
  current slug"; it does not hold a second copy of the authentication judgement.
- Alongside it, `GET /api/preview/shared` (workspaces shared within the same tenant) feeds the
  Console's preview popover. **Whether it is up is judged by the presence of the `preview_slug`
  column** — what we want here is not "is the container running" but "does a URL exist right now".

## Rejected

- **Mint an unrelated slug per port** — a leak of one would not spread to the other, but a human
  would have to handle two unrelated strings. Workspace-level granularity is enough for a leak.
- **`{slug}.{PreviewDomain}` with the port in the path** — every reason for the host route
  (absolute paths, origin separation) survives untouched.
- **Issue an ACM certificate per workspace** — issuance and DNS validation land in the start path,
  and it hits quotas.
- **Drop authentication and rely on the random URL as the key** (as the default) — these URLs get
  pasted into Slack and tickets and travel in Referer. The default is authentication; public is
  something you choose (decision 12).

## Consequences

- Control Plane: the host-routing layer, minting and expiring slugs, the handshake, and the move to
  ReverseProxy. Migrations for **both sqlite and Postgres** (only one dialect passing is a known
  accident shape).
- Console: the preview popover, the allowed ports and slug pinning in the workspace settings, the
  public-mode indication, and the tenant-share toggle plus the "shared with you" list (decisions 14
  and 17).
- Deployment: a `PreviewDomain` parameter in `30-ingress.yaml`, the `*.{PreviewDomain}` ACM
  certificate, the `ListenerCertificate` attaching it to Listener443, and the wildcard A alias in
  Route53. ⚠️ **The existing `Cert` is not touched** (adding a SAN replaces it — decision 4).
- Guidance: `docs/guide` (member/08, 09, README, the glossary) updated in both languages.
  ⚠️ At the same time, **the stale claim that "the lightweight preview does not support WebSocket /
  SSE" was corrected** — moving it onto the ReverseProxy in decision 10 means the path route passes
  them too.
- Deployment procedure: a `PreviewDomain` section added to `deploy/aws/ecs/README.md`.
