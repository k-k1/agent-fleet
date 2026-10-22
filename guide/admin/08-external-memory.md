---
audience: "a tenant administrator deciding whether members may use Agent Fleet memory from outside Agent Fleet"
updated: "2026-10"
---

# 08. Memory from outside Agent Fleet

English | [日本語](08-external-memory.ja.md)

A member can let an agent outside Agent Fleet (Claude Code or Claude Desktop on their own machine,
holding their MCP token) list, search and read their Agent Fleet memory, and with a second choice
save and forget it ([member guide](../member/14-agent-memory.md#using-agent-fleet-memory-from-an-external-agent)).
The memory text then leaves the workspace and reaches that client's model. The secret scan stops
credentials, not customer names or internal context. This page is how you switch the whole
tenant off.

## The switch

Tenant settings → **External memory access** (under Operations). **Allow** (the starting
state of every tenant) lets members opt in; each member's own setting still starts **Off**, so
nothing changes for anyone until they choose. **Block** withholds the memory tools from the
tenant altogether.

While it is blocked:

- the `/mcp` endpoint does not list `memory_projects`, `memory_index`, `memory_search`,
  `memory_read`, `memory_save` or `memory_forget` for anyone in the tenant, and a call to one by
  name is refused as an unknown tool, whatever the member's own setting or token scope says;
- it applies from the next request, with no restart, to tokens already issued;
- members' settings are kept, so allowing it again restores what each member had chosen;
- memory used inside Agent Fleet (sessions, the Console's Agent memory screens) is not affected,
  and neither are the other `/mcp` tools.

If you cannot read the switch (for example the store is unavailable), the tools are withheld
rather than offered.

Changing it is recorded in the [audit log](03-audit-usage.md) as `tenant.external_memory`, with
`enabled=true` or `enabled=false`. Only a tenant administrator or a super_admin can change it. Turning off the
whole `/mcp` endpoint for a deployment is the operator's `AF_MCP_ENABLED`, a different switch.
