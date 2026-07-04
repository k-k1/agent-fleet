// Assistant-chat domain types (docs/19). A "conversation" is a headless-CLI LLM
// chat/translation thread, distinct from a tmux session. `agent` reuses SessionKind
// values (claude/codex) to select the backend provider and its presentation.

import type { SessionKind } from "./session.ts";

export interface ChatMessage {
  role: "user" | "assistant";
  content: string;
  ts: number; // unix millis
}

// Light shape from GET /api/chat/conversations (no message bodies).
export interface ConversationMeta {
  id: string;
  agent: SessionKind;
  title: string;
  model?: string;
  created_at: number;
  updated_at: number;
  message_count: number;
}

// Full conversation from GET/POST /api/chat/conversations/{id}.
export interface Conversation {
  id: string;
  agent: SessionKind;
  title: string;
  model?: string;
  created_at: number;
  updated_at: number;
  messages: ChatMessage[];
}
