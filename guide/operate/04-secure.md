---
audience: "someone responsible for the deployment's security posture"
source_of_truth: "the scripts under `deploy/` — a command here that contradicts the script it describes is a bug in this page"
---

# 04. Security Operations

English | [日本語](04-secure.ja.md)

This chapter has moved to pages organised by task.

| What you want to do | Page |
|---|---|
| Where the isolation boundary is, how to handle the remaining risks, reporting a vulnerability | [Understand the threat model and residual risks](secure/threat-model.md) |
| What `AF_MASTER_KEY` and `AF_COOKIE_SECRET` must be, a deployment without a key, values stored before the key, per-purpose cookie keys | [Handle the deployment keys](secure/deployment-keys.md) |
| Observe outbound traffic from workspaces and narrow it with an allowlist | [Control outbound traffic](secure/egress.md) |
| OAuth for `/mcp`, trusted client hosts, members' OAuth MCP servers, the `/mcp` log | [Operate MCP and external-connection sign-in](secure/mcp.md) |
| `AUTH=dev` and the development OIDC IdP | [Use development sign-in](secure/dev-signin.md) |
| The preview domain, the sign-in allowlist, the audit log, the host's cloud identity, handing over the browser | [Other operational controls](secure/controls.md) |
| Remove someone, end everybody's sessions, hand over super_admin | [Revoke access and hand over super_admin](secure/offboarding.md) |
