// Assistant-chat domain types (docs/19). A "conversation" is a headless-CLI LLM
// chat/translation thread, distinct from a tmux session. `agent` reuses SessionKind
// values (claude/codex) to select the backend provider and its presentation.

import type { SessionKind } from "./session.ts";

// One "作業過程" item of an assistant turn (docs/19): the narration the model emitted
// right before it called a tool. Kept alongside the final answer so the UI can show the
// process separately (保持). Empty for tool-less replies.
export interface ChatStep {
  text?: string; // narration before the tool call(s)
  tools?: string[]; // tool name(s) invoked in this step
}

export interface ChatMessage {
  role: "user" | "assistant";
  content: string;
  ts: number; // unix millis
  steps?: ChatStep[]; // assistant working process, separated from the final content
}

// Light shape from GET /api/chat/conversations (no message bodies).
export interface ConversationMeta {
  id: string;
  agent: SessionKind;
  assistant_id?: string; // which assistant backs this thread (Q2)
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
  assistant_id?: string;
  title: string;
  model?: string;
  created_at: number;
  updated_at: number;
  messages: ChatMessage[];
  // Transient first-turn prompt returned only by create when a file/dir was attached
  // (docs/19 Phase C). The Console prefills the composer with it; it is never persisted.
  seed?: string;
  // Transient flag from GET only: an assistant turn is still running on the backend.
  // A client that reloaded mid-answer uses it to keep the thinking indicator up and
  // poll until the reply lands (the detached turn survives the reload). Never persisted.
  in_progress?: boolean;
}
