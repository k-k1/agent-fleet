# 0047. Let a tenant admin restrict where their tenant may be connected from (**at the application layer, after authentication**)

English | [日本語](0047-tenant-network-restriction.ja.md)

- Status: **adopted** (2026-08-17). The record of the investigation is [docs/66](../log/66-tenant-network-restriction.md).
- See also: [0043-login-idp.md](0043-login-idp.md) decisions 24/25 (what reaches outside the tenant
  belongs to the operator, what stays inside belongs to the tenant admin) and decision 14 (the gate
  goes on the resolution path, not on the screen) /
  [0044-workspace-sizing.md](0044-workspace-sizing.md) decision 3 (the precedent of **shipping
  something off by default that then never fired once**) / [docs/64](../log/64-ec2-persistent-workspace.md) §64.25 (the
  decision not to use signature rules in the WAF — the same line of "do not make something that does
  not work look as though it does")

## Context

Restricting where connections come from is **only** `AlbIngressCidr` in `00-network.yaml`
(defaulting to `0.0.0.0/0`) — deployment-wide, requiring a CFN re-apply, and AWS-only. Tenant admins
do not touch AWS (and are not let near it). Meanwhile what actually happens as an incident is
"someone with credentials gets in from a place they are not allowed to be", and that can only be
expressed per tenant.

## Decision 1 — adopt it, but present it as "access restriction", not network defence

The request passes the ALB, reaches the CP, TLS is terminated and the session is validated, and only
**then** is it refused. So it is **ineffective** against pre-authentication vulnerabilities, DoS,
bandwidth and probing.

- **It does not replace the two layers above (`AlbIngressCidr` / the WAF).** For a private
  deployment, the security group is still the cheapest and strongest thing. The Console's
  explanatory text says so.
- What it does cover is only "**who touches the data, and from where**". That is the whole of this
  feature.
- **State what it cannot do on screen** — the login screen is visible and signing in succeeds.
  Hiding that gets read as "I restricted by IP and could still log in, so it is broken".

## Decision 2 — take the source IP as **the Nth from the right of XFF**, with N declared by the deployment

The CP has never read even `RemoteAddr`. The rule for identification is fixed here.

- **`AF_TRUSTED_PROXY_HOPS` (default 0)**. 0 = no proxy (`RemoteAddr` is genuine). ALB only = 1
  (passed into the CP's task environment by `30-ingress.yaml`). compose + Caddy is also 1.
- **The client = `XFF[len-N]`** (with N=1, the rightmost). A proxy **appends** "whoever it received
  from", so only as many as you count from the right can be trusted. **Only an implementation that
  reads the leftmost is forgeable**, and that is the one way to get it wrong.
- **Only one place — the outermost middleware — reads the header**, and it puts the result into the
  context. The same reason `authGate` does `r.Header.Del` on the identity headers before inserting
  its own: never let downstream trust a raw header.
- The startup banner prints `trusted-proxy-hops=N`.

## Decision 3 — the check goes next to `checkTenantProvider`. **PAT/MCP and git are out of scope**

The first point at which the tenant is known is after `selectMembership`. `checkTenantIP` goes there
(`resolveFull` / `resolveMembership`). 403 `ip_not_allowed`.

⚠️ **Do not put it in `resolveByMembership` (PAT).** The source on that path is **the person's own
workspace container** and represents nothing about where the person is (`AF_MCP_TOKEN` is injected
into the container at startup, and git hits the CP from inside the container too). Putting an IP
check there means a tenant that allowed the office CIDR **blocks all MCP and git from inside its own
workspaces**. The tool for stopping a person already exists — disabling the membership (which
expires the PAT).

For the same reason `/internal/*` (the callback from the Agent) is out of scope.

## Decision 4 — three escape hatches against locking yourself out, one of which is a refusal rather than a display

1. **super_admin is exempt.** The operator can always undo it. The last resort.
2. **On save, the editor's own current IP must be included.** If it is not, refuse with a 400 and
   **put the IP the CP can see straight into the message**.
3. **Refuse a misconfiguration on the spot.** If `hops==0` but an XFF arrived (i.e. behind a proxy
   with no declaration), or the XFF is too short to index, **nothing is saved at all**.

⚠️ Why 3 is needed: **displaying "your current IP" is not enough.** On a misconfiguration you can see
the ALB's private address `10.20.10.5`, think "that must be my IP", register it as is, and end up
**letting everyone through while believing you narrowed it**. Refuse rather than display.

## Decision 5 — do not add an operator switch. "Off" means the list is empty

No default-off gate such as `AF_TENANT_IP_RULES=on/off`. [ADR 0044](0044-workspace-sizing.md)
decision 3 **shipped one off by default and it never fired once**. Whether the feature is enabled is
expressed solely by **whether the tenant wrote one CIDR line**.

## Decision 6 — it lives on the tenant's login-rules row, owned by the tenant admin

- **`tenant.allowed_cidrs` (CSV, one column)**. It is read per request, so it rides the existing
  30-second cache (`tenantLoginCache`, invalidated immediately on write). Not `tenantLimits` (JSON) —
  those are caps held by super_admin, a different owner.
- **Editing is `PUT /api/admin/tenants/{slug}/network` (`tenantAdminFor`)**. `setTenantLogin` stays
  super_admin-only and is **untouched** (those three items reach outside the tenant).
- The notation accepts both prefixes and bare IPs, IPv4 and IPv6. **A prefix with host bits set is
  rounded before saving, and the rounding is reported in the response** (never silently change the
  meaning).
- Refusals are summarised into the audit log. **The source IP is not put in the access log for every
  request** (that changes how personal data is handled; if it becomes necessary, decide it separately
  along with a retention period).

## Impact

- `control-plane/clientip.go` (new), `resolver.go`, `tenant_login.go`, `tenants.go`, `routes.go`,
  `main.go` (the middleware and the banner)
- One column, `tenant.allowed_cidrs`, added to `migrations/` and `migrations-pg/` (the same shape as
  `0042_tenant_hidden_providers.sql`)
- One tenant-settings panel plus wording (en/ja) in `console/src/features/settings/`
- `AF_TRUSTED_PROXY_HOPS=1` in `deploy/aws/ecs/cfn/30-ingress.yaml`, and guidance for 1 under a Caddy
  setup in `deploy/compose/.env.example`
- **To confirm on real hardware**: that the CP, behind the ALB, sees the genuine global IP (this is
  the premise for all of it, and cannot be confirmed on paper).
