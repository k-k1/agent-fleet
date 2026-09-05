// Assistant-template domain types (docs/log/19 Q2). An Assistant is a configurable chat
// persona (custom-GPT style): a name, an agent backend, an optional model, a system
// prompt, a tool grant, and optional knowledge dirs. Builtins are code-injected on the
// Agent and are not editable/deletable; user-defined ones support full CRUD.

import type { SessionKind } from "./session.ts";

// Tool grant an assistant holds. "af_read" attaches the read-only fleet MCP tools
// (docs/log/19 Q1); "af_write" additionally exposes the write tools (send_to_session …),
// an explicit per-assistant opt-in (docs/log/19 Q2).
export type ToolGrant = "none" | "af_read" | "af_write";

export interface Assistant {
  id: string;
  name: string;
  icon?: string; // codicon name
  description?: string; // user-facing self-intro (greeting card), distinct from persona
  builtin: boolean;
  agent: SessionKind; // only kinds that support headless chat (ASSISTANT_AGENT_KINDS — claude/codex/opencode/cursor/agy)
  model?: string;
  persona?: string;
  tools: ToolGrant;
  knowledge?: string[];
  // Ids of the MCP servers connected to this assistant's chat, as they appear in the
  // effective registry; built-in integrations keep their fixed id ("pagerduty" and the
  // like). docs/log/48 §7.
  integrations?: string[];
  // Read-aloud voice ("vv:<speaker>" / "polly:<VoiceId>"; "" means automatic, assigned from
  // the character pool). Stored on the agent side; resolution and synthesis are entirely the
  // Console's (docs/log/24).
  voice?: string;
  created_at?: number;
  updated_at?: number;
}

// The user-editable subset sent to POST/PUT /api/assistants.
export interface AssistantInput {
  name: string;
  icon?: string;
  description?: string;
  agent: SessionKind;
  model?: string;
  persona?: string;
  tools: ToolGrant;
  knowledge?: string[];
  integrations?: string[];
  voice?: string;
}
