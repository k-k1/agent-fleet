# 0001. Delivery model — each company self-hosts (SaaS abandoned)

English | [日本語](0001-self-host-vs-saas.ja.md)

- Status: decided (2026-06-27)
- See also: [Roadmap Phase 3](../roadmap.md) / [build/07 §7.9 Risks and open work](../build/07-security.md#79-risks-and-open-work) (formerly security §4.7)

## Context

Phase 2 finished the internal MVP: one on-prem machine, several users who cannot see each
other, encryption at rest. Productising it was next — but the delivery model changes the
Anthropic ToS posture drastically. Claude runs on each user's personal subscription
(BYO `/login`), so the question becomes "whose infrastructure hosts whose employees?"

## Options considered

1. **Commercial multi-tenant SaaS (for external customers)** — rejected. BYO (running a
   personal Claude subscription on a shared, third-party-hosted service) is ToS grey area.
2. **An internal multi-tenant SaaS we operate ourselves (our company + affiliates)** —
   rejected. Hosting another legal entity's employees on our infrastructure is still on the
   grey side.
3. **Packaged product + self-hosting per company (adopted)** — each company hosts **its own
   employees on its own infrastructure**, which is the cleanest ToS posture available. We are
   the **vendor/maintainer**, not the operator.

## Decision

**Package the product; each group company self-hosts it (1 company = 1 deployment).**

The settled premises:

| Question | Decision |
|---|---|
| Delivery | Packaged product, self-hosted by each company. **No phone-home** |
| Claude auth | Stays BYO (each person runs `/login`). Company-owned Team/Enterprise seats recommended |
| Separation between companies | **Separate deployments (strongest, and free)**. Different entity = different infrastructure, database and root key |
| Multi-tenancy within a deployment | Optional (default = single tenant = the whole company). Splitting by department is an enterprise extension |
| Budget | Per deployment, set by that company's admin. No external billing |
| Deployment target | The company's choice (on-prem Docker by default, their own AWS optionally) |
| Scale | Small (1 deployment = tens to ~100 users). SQLite is the default database |

The old `platform_admin` (i.e. us) is gone. The vendor does not appear anywhere in the
runtime hierarchy ([Roadmap §12.1](../roadmap.md#121-アイデンティティ階層パッケージセルフホスト版)).

## Consequences

- **P3-10 packaging** becomes the core of the delivery model (compose/Helm + configuration +
  migrations + runbook). Done means: stand up a second deployment from scratch and pass E2E.
- Data, keys, OAuth configuration and user management are **entirely the company's**. We have
  no runtime access.
- **The residual risk, stated honestly**: within one deployment the CP holds `docker.sock`
  (equivalent to host root) and injects plaintext DEKs, so compromising the CP or the host
  collapses the separation inside that deployment all at once. **The strength of this model is
  that it does not spread between companies**, because they are different deployments. A
  department that needs stronger isolation goes to dedicated (P3-8), to its own deployment, or
  to AWS (task isolation, no shared docker.sock). Mitigations: rootless Docker, a socket proxy,
  minimal CP privileges.
