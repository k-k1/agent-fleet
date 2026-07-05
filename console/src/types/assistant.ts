// Assistant-template domain types (docs/19 Q2). An Assistant is a configurable chat
// persona (custom-GPT style): a name, an agent backend, an optional model, a system
// prompt, a tool grant, and optional knowledge dirs. Builtins are code-injected on the
// Agent and are not editable/deletable; user-defined ones support full CRUD.

import type { SessionKind } from "./session.ts";

// Tool grant an assistant holds. "af_read" attaches the read-only fleet MCP tools
// (docs/19 Q1); "af_write" additionally exposes the write tools (send_to_session …),
// an explicit per-assistant opt-in (docs/19 Q2).
export type ToolGrant = "none" | "af_read" | "af_write";

export interface Assistant {
  id: string;
  name: string;
  icon?: string; // codicon name
  builtin: boolean;
  agent: SessionKind; // "claude" | "codex"
  model?: string;
  persona?: string;
  tools: ToolGrant;
  knowledge?: string[];
  created_at?: number;
  updated_at?: number;
}

// The user-editable subset sent to POST/PUT /api/assistants.
export interface AssistantInput {
  name: string;
  icon?: string;
  agent: SessionKind;
  model?: string;
  persona?: string;
  tools: ToolGrant;
  knowledge?: string[];
}
