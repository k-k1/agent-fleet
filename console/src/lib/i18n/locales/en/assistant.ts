// English カタログ / ドメイン: assistant
// キー接頭辞: asst, assistant
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { assistant as jaAssistant } from "../ja/assistant.ts";

export const assistant: Record<keyof typeof jaAssistant, string> = {
  // --- builtin assistants (docs/log/28 P3; ids are the fixed set in assistants.go. The
  //     Agent returns Japanese, but for builtins the Console catalog resolves display) ---
  "assistant.af.name": "Agent Fleet Assistant",
  "assistant.af.desc":
    "Hi! I'll guide you through using Agent Fleet. I answer while checking real things — how to do something, and the current state of your workspace (running sessions and so on).",
  "assistant.operator.name": "Fleet Operator",
  "assistant.operator.desc":
    "The fleet's command center. I watch running sessions and, when needed, send them instructions or spin up new sessions to move work forward (including starting a task from a handoff or brainstorm). I can also review, add to, and bulk-send the memo queue. I consult other assistants for specialized calls, and I confirm before acting.",
  "assistant.sre.name": "SRE Assistant",
  "assistant.sre.desc":
    "Your partner for incident response and monitoring ops (read-only). Connect PagerDuty, Grafana, and CloudWatch and I'll help triage the situation, hypothesize root causes, and draft external updates while looking at open incidents, metrics, and logs.",

  // --- assistant settings (AssistantTab) ---
  "assistant.output_language": "Reply language",
  "assistant.note_output_language":
    "The reply language for the assistant chat. “Match input” replies in the language of the text or question you give it. Choosing Japanese/English replies in that language even for text in another language (translation assistants excluded). This changes chat replies only — the read-aloud language lives on the Read aloud tab.",
  "assistant.agent_order": "Agent priority",
  "assistant.note_agent_order":
    "Priority of the CLIs that power the assistant chat. The first connected CLI is used; disconnected ones are skipped. Choose each CLI's model below. Takes effect for new built-in-assistant conversations; an explicit custom-assistant setting wins. AI assistance (titles, reply suggestions and the like) is ranked separately on the AI assistance tab.",
  "assistant.models": "Assistant models",
  "assistant.note_models":
    "Model used by built-in assistants for new conversations. If priority falls back to another CLI, that CLI uses its row's model. “Recommended” chooses a safe model from the connected catalog and shows the current resolution.",
  "assistant.recommended_now": "Recommended (currently: {model})",
  "assistant.auto_turn": "Auto-respond to session reports",
  "assistant.note_auto_turn":
    "When a session launched or steered by the Fleet Operator (or another assistant with AF write access) reports back, the assistant runs one turn automatically to process it. As a runaway guard, unattended turns per conversation are capped at the limit below (reset whenever you send a message).",
  "assistant.auto_turn_limit": "Unattended auto-reply limit",
  "assistant.note_auto_turn_limit":
    "How many auto-replies may run in a row without a message from you (default 10, max 50). At the limit a pause notice arrives, and your next message resumes the loop. Unlimited is not available.",
  "assistant.auto_turn_model": "Auto-reply model",
  "assistant.auto_turn_model_default": "Same as the conversation",
  "assistant.note_auto_turn_model":
    "Runs only the automatic replies to session reports on a separate model. Checking and summarizing reports is routine work, so switching to a lightweight model such as haiku cuts token spend substantially. Applies to claude conversations only; replies to your own messages and compaction summaries keep the conversation's model.",
  "assistant.auto_turn_delay": "Auto-reply bundling window",
  "assistant.auto_turn_delay_off": "Immediate",
  "assistant.note_auto_turn_delay":
    "Instead of replying the moment a completion report arrives, reports from other sessions arriving within this window are processed together in one turn (each auto-reply re-reads the whole conversation, so fewer turns means fewer tokens). Report cards and notifications still arrive immediately — only the assistant's follow-up is deferred.",
  "assistant.quiet_completion": "Quiet completion reports",
  "assistant.note_quiet_completion":
    "For successful completion reports, skip the automatic reply and only deliver the report card and a notification. The reports are handed to the assistant together with your next message. Interrupted, failed, and crashed reports — and questions / plan approvals — are still handled automatically.",
  "assistant.auto_pilot": "Auto-pilot (auto-handle questions & plans)",
  "assistant.note_auto_pilot":
    "When ON, if an instructed session stops at a multiple-choice question the operator answers with the session's recommendation automatically, and when it stops at plan approval the operator has another session review the plan, feeds back findings, and approves once clean. Every decision is shared in chat. Unclear questions and choices/plans involving destructive or irreversible operations still come to you first. Default OFF.",
  "assistant.auto_resume": "Auto-resume interrupted turns",
  "assistant.note_auto_resume":
    "When an instructed session's turn is cut off part-way by a dropped connection or a temporary rate limit, the operator sends a plain \"please continue\" to resume it (matching the language the session is working in). Every resume is shared in chat. Interruptions whose cause won't clear on its own (usage limit, exhausted credit, prompt too long) would fail the same way on a re-send, so they are not auto-resumed — you get a note about fixing the cause instead. Resumes up to twice in a row; if it keeps getting cut off, it reports to you. Default ON.",
  "assistant.auto_compact": "Auto-compact chat context",
  "assistant.note_auto_compact":
    "When a chat's context usage is still above 90% as a new exchange starts, the conversation is summarized and handed to a fresh session automatically first (the summary costs one turn of tokens). The notice at 80% lets you compact manually before this fires.",
  "assistant.auto_compact_tokens": "Auto-compact threshold (tokens)",
  "assistant.note_auto_compact_tokens":
    "When a chat's context exceeds this many tokens as a new exchange starts, it is compacted automatically even before reaching 90% usage. A chat re-reads its whole context every turn, so this value effectively caps the per-turn token spend. Lower saves more but compacts (re-summarizes) more often. Default 150k.",
  "assistant.output_tail": "Session output fetch limit",
  "assistant.note_output_tail":
    "How much the operator reads when checking a session's output (get_session_output), taken from the end. What it reads accumulates in the conversation and is re-read on every later turn, so a larger limit costs more tokens. The full output is always available in the mirror. Default 32 KiB.",
  "assistant.appearance": "Appearance",
  "assistant.note_appearance":
    "“Inherit” follows the app theme. Theme and background color are saved on this device only (not synced to others).",

  // === P5 アシスタント UI（AssistantModal/AssistantSection） ===
  "asst.tool_none": "None",
  "asst.tool_none_help": "No external tools. Answers directly in chat.",
  "asst.tool_read": "AF read",
  "asst.tool_read_help": "Can read your workspace's session list, status, and output (no writing).",
  "asst.tool_write": "AF write",
  "asst.tool_write_help": "In addition to reading, can send prompts to sessions (acting on your behalf). Grant only for trusted uses.",
  "asst.voice_auto": "Auto (from character pool)",
  "asst.edit_assistant": "Edit assistant",
  "asst.create_assistant": "Create assistant",
  "asst.name": "Name",
  "asst.name_ph": "e.g. Release-note translator",
  "asst.desc_label": "Description (greeting shown at start)",
  "asst.desc_hint": "A self-introduction shown before the conversation starts. Sum up what it can do in a line.",
  "asst.desc_ph": "e.g. Send me text and I'll translate between Japanese and English.",
  "asst.icon": "Icon",
  "asst.agent": "Agent",
  "asst.model": "Model (optional)",
  "asst.model_ph": "Default model",
  "asst.voice_label": "Read-aloud voice (optional)",
  "asst.voice_hint": "The voice used when reading this assistant's replies aloud. \"Auto\" assigns a fixed voice from the read-aloud settings' character pool (when \"Use a different voice per session\" is ON; if OFF, the speaker set in settings is used).",
  "asst.persona_label": "Persona (system prompt)",
  "asst.persona_hint": "Describe the assistant's role, tone, and constraints. Leave blank to act as a general-purpose assistant.",
  "asst.persona_ph": "e.g. You are a technical-document translator. Return only the translated text.",
  "asst.tools_label": "Tool permissions",
  "asst.mcp_label": "MCP servers (optional)",
  "asst.mcp_hint": "Choose the MCP servers this assistant's chat connects to. The list holds the built-in integrations (PagerDuty / Grafana / CloudWatch) plus anything registered under Settings > MCP servers.",
  "asst.mcp_empty": "No MCP server is available to attach. Register one under Settings > MCP servers, or connect an integration on the Ops & monitoring tab.",
  "asst.mcp_disabled": "Disabled (will not attach)",
  "asst.mcp_not_ready": "Incomplete configuration (will not attach)",
  "asst.mcp_out_of_scope": "Out of scope for {agent} (will not attach)",
  "asst.knowledge_label": "Knowledge directories (optional, one per line)",
  "asst.knowledge_hint": "Specify directories (absolute paths inside the container) that hold documents you want referenced, and they can be read during the conversation.",
  "asst.cancel": "Cancel",
  "asst.save": "Save",
  "asst.create": "Create",
  "asst.update_failed": "Failed to update the assistant",
  "asst.create_failed": "Failed to create the assistant",
  "asst.delete_assistant": "Delete assistant",
  "asst.delete_confirm": "Delete \"{name}\". Existing conversations will remain.",
  "asst.delete": "Delete",
  "asst.delete_failed": "Failed to delete the assistant",
  "asst.remove_failed": "Failed to delete",
  "asst.rename": "Rename",
  "asst.rename_failed": "Failed to rename",
  "asst.title_rename_title": "Edit title",
  "asst.title_label": "Title",
  "asst.title_ph": "e.g. Billing API refactor discussion",
  "asst.ai_suggest": "Ask AI to suggest",
  "asst.proposal": "Suggestion",
  "asst.adopt": "Use this",
  "asst.suggest_fetch_failed": "Failed to get a suggestion (network error)",
  "asst.copy_id": "Copy ID ({id})",
  "asst.id_copied": "Copied chat ID \"{id}\"",
  "asst.new_chat": "New chat",
  "asst.builtin_badge": "Built-in",
  "asst.edit": "Edit",
  "asst.section_title": "Assistants",
  "asst.empty": "No chats yet. Start one from +.",
  "asst.in_progress": "Working",
  "asst.waiting": "Idle",
  "asst.focus_pane": "Focus pane {n}",
  "asst.lock": "Lock against deletion",
  "asst.unlock": "Unlock (allow deletion)",
  "asst.locked_hint": "Locked against deletion. Unlock it first.",
  "asst.lock_failed": "Failed to change the deletion lock",
  "asst.locked_on": "Locked against deletion",
  "asst.locked_off": "Deletion lock removed",
  "asst.delete_chat": "Delete this chat",
  "asst.open_new_pane": "Open in a new pane",
};
