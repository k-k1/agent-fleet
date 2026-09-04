// Alias (ADR 0067 rule 1: do not touch a single line of the call sites).
//
// The implementation lives in mcp/mcpWire.ts. features/chat/AssistantModal.tsx pulls the
// McpServer type from `../settings/mcpWire.ts`, which is outside FE-SETTINGS's ownership,
// so this one file keeps the old path alive.
//
// Retiring it (repointing the call sites at the new path) happens in another session at a
// wave boundary; deleting it here would collide with features/chat.
export * from "./mcp/mcpWire.ts";
