// Assistant-chat domain types (docs/log/19). A "conversation" is a headless-CLI LLM
// chat/translation thread, distinct from a tmux session. `agent` reuses SessionKind
// values (claude/codex) to select the backend provider and its presentation.

import type { SessionKind } from "./session.ts";
import type { ToolGrant } from "./assistant.ts";

// One "作業過程" item of an assistant turn (docs/log/19): the narration the model emitted
// right before it called a tool. Kept alongside the final answer so the UI can show the
// process separately (保持). Empty for tool-less replies.
export interface ChatStep {
  text?: string; // narration before the tool call(s)
  tools?: string[]; // tool name(s) invoked in this step
}

export interface ChatMessage {
  role: "user" | "assistant" | "report" | "notice";
  content: string;
  ts: number; // unix millis
  agent?: SessionKind; // backend that actually generated this assistant turn
  // Model that drove THIS turn: what the CLI reported (claude/opencode) or what was
  // passed on its command line (codex/cursor/agy, which name no model). Absent on
  // legacy turns and whenever the backend ran on its own default — the UI then shows
  // no model instead of standing in the conversation's current setting.
  model?: string;
  steps?: ChatStep[]; // assistant working process, separated from the final content
  // role==="report" (docs/log/30): the reporting session's name — rendered as a
  // session-origin card, not a user/assistant bubble.
  session?: string;
  // role==="notice" (docs/log/30): a system notice (e.g. the operator's auto-turn budget
  // ran out and the loop paused) — rendered as a centered informational card.
  // report only: whether the report has been fed into the provider's context yet.
  delivered?: boolean;
  // role==="notice" (ADR 0033): the catalog key + arguments the card is rendered from,
  // so the text follows settings.locale instead of the language it was stored in.
  // Absent on notices written before the change — those fall back to `content`, which
  // still holds the same sentence in the source language. See features/chat/notice.ts.
  // role==="notice" and, since docs/log/28 P6, role==="report": the catalog key +
  // arguments the card is rendered from (see features/chat/report.ts).
  notice_key?: string;
  notice_args?: Record<string, string>;
  // role==="report" (docs/log/28 P6): the event the card stands for. The Agent keeps the
  // operator's marching orders OUT of the stored body and re-renders them when it
  // builds the prompt, so the card can follow the display language; report_reason
  // qualifies the kind (turn-failed / turn-aborted / oom …) and names the exit label.
  report_kind?: string;
  report_reason?: string;
}

// Current context-window fill, captured server-side from the provider's usage events
// after each turn (workspace/agent/chat_usage.go) — same wire shape as the session
// usage the mirror's ContextBar renders.
export interface ChatContextUsage {
  tokens: number; // read + create + fresh
  read?: number;
  create?: number;
  fresh?: number;
  window?: number; // context-window size the pct is against
  windowSource?: "recorded" | "estimated";
  pct?: number; // 0–100
  model?: string;
}

// Light shape from GET /api/chat/conversations (no message bodies).
export interface ConversationMeta {
  id: string;
  // Short addressable identity ("a"+6 chars — the assistant twin of session slugs),
  // used by schedules (docs/log/38 アシスタント発火) and shown in the row tooltip.
  slug?: string;
  agent: SessionKind;
  active_agent?: SessionKind; // backend used by the latest successful turn
  assistant_id?: string; // which assistant backs this thread (Q2)
  title: string;
  model?: string;
  created_at: number;
  updated_at: number;
  message_count: number;
  context?: ChatContextUsage; // current context fill (chat_usage.go)
  locked?: boolean; // 削除ロック（docs/log/45）: true の間 DELETE は 403 で拒否される
}

// Full conversation from GET/POST /api/chat/conversations/{id}.
export interface Conversation {
  id: string;
  agent: SessionKind; // preferred backend snapshotted at creation
  active_agent?: SessionKind; // actual backend used by the latest successful turn
  assistant_id?: string;
  title: string;
  model?: string;
  created_at: number;
  updated_at: number;
  messages: ChatMessage[];
  // Transient first-turn prompt returned only by create when a file/dir was attached
  // (docs/log/19 Phase C). The Console prefills the composer with it; it is never persisted.
  seed?: string;
  // Transient flag from GET only: an assistant turn is still running on the backend.
  // A client that reloaded mid-answer uses it to keep the thinking indicator up and
  // poll until the reply lands (the detached turn survives the reload). Never persisted.
  in_progress?: boolean;
  // Tool grant snapshot (assistant.ts の ToolGrant と同じ値域; legacy 会話は未設定).
  // af_write conversations can receive server-pushed session reports (docs/log/30), so
  // ChatView keeps them fresh with a light poll while the pane is active.
  tools?: ToolGrant;
  // Current context fill (chat_usage.go): drives the ContextBar under the chat header.
  context?: ChatContextUsage;
  // Compaction summary waiting to ride the next prompt (docs/log/33 第2段). Display-only
  // here (the notice message already shows it); cleared server-side after it carries.
  pending_handoff?: string;
  // Standing work plan (docs/log/33 第5段): carried into every fresh provider session
  // verbatim — never summarized, never consumed. Edited from the 計画 panel, refreshed
  // from the recent conversation, and diff-updated by compaction.
  plan?: string;
  plan_updated_at?: number;
}
