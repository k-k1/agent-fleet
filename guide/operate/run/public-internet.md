---
audience: "Whoever exposes the Control Plane on a public host name"
source_of_truth: "The Control Plane's routes outside authentication (`control-plane/routes.go` and others), `deploy/aws/ecs/cfn/PARAMETERS.md`"
---

# Expose it to the internet

English | [日本語](public-internet.ja.md)

Put the Control Plane on a public host name and vulnerability scanners arrive within hours. There is no need to panic.
This page covers what gets through, how to tell it apart in the logs, and where to narrow it.

## How it works

| What you do | What happens |
|---|---|
| Nothing | Requests without a session get 401, except at a fixed set of entry points |
| Filter the access log on `401` | You can count the probes and see whether each was refused or answered |
| For internal use only, narrow the sources you accept | Probes never reach the load balancer |
| To see or block per IP, do it outside the Control Plane | Analyse with ALB access logs, block with AWS WAF |

## What gets through without a session

Every request without a sign-in gets 401, except at these entry points.

| Entry point | Examples |
|---|---|
| Sign-in and health checks | `/healthz`, `/readyz`, `/login`, `/login/<tenant>`, `/oauth2/*`, `/brand/*` |
| Entry points with their own authentication | `/internal/*`, `/mcp`, `/git/*` and others. Each authenticates on its own and answers 404 when not enabled |

## Tell them apart in the logs

The access log carries the status code. A line such as `GET /actuator/heapdump 401 0s` tells you from the log alone whether it was refused or answered.
To count probes, filter on `401`.

## Narrow it

**For internal use only, narrowing the accepted sources is the strongest measure.** On AWS, set `AlbIngressCidr` in `00-network` to your own address range and probes never reach the ALB. It costs nothing.

To see or block per IP, do it outside the Control Plane. A per-request block inside the Control Plane would only add decisions outside authentication, so there is none.

| Want to | Use |
|---|---|
| Analyse by source | ALB access logs |
| Block | AWS WAF. `30-ingress` has a WAF that is off by default |

The AWS WAF offers only two choices:

| Parameter | Effect |
|---|---|
| `WafRateLimitPer5Min` | Requests per 5 minutes from one IP before it is blocked. Console polling counts too, so keep it well above what a whole office behind one NAT sends |
| `WafIpReputation` | Adds AWS's managed IP reputation list (known scanners and botnets). Only active when `WafRateLimitPer5Min` is greater than 0. It matches on the source, so it cannot break legitimate traffic |

> [!WARNING]
> **Signature rules (Core rule set, SQLi, XSS) are left out on purpose.** In this product, code and shell commands flow through ordinary bodies: chat, file writes, terminal input.
> `'; DROP TABLE` and `../../etc/passwd` show up as legitimate traffic, and normal actions get a 403 at random. It also takes a long time to realise the WAF is the cause.

## Related

- [Understand the threat model and residual risks](../secure/threat-model.md)
- [Control outbound traffic](../secure/egress.md)
- [Put WAF in front](../aws-ecs/options.md#put-waf-in-front)
