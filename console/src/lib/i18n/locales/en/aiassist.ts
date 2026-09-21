// English カタログ / ドメイン: aiassist（設定 > AI補助）
// キー接頭辞: aiassist
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { aiassist as jaAiassist } from "../ja/aiassist.ts";

export const aiassist: Record<keyof typeof jaAiassist, string> = {
  "aiassist.agent_order": "Agent priority",
  "aiassist.note_agent_order":
    "Priority order of the CLIs that run AI assistance. The first CONNECTED CLI from the top is used (unconnected ones are skipped). This is ranked separately from the assistant chat: the chat wants the strongest model, while assistance runs constantly and wants the cheapest one that works. Changes apply to the very next generation.",

  "aiassist.short_models": "Model for short labels",
  "aiassist.note_short_models":
    "Used for session and chat titles, branch names, and AI reply suggestions. These return a single short line, so a fast, cheap model is enough. The CLI follows the priority above. \"Recommended\" picks an available fast, low-cost model and falls back safely to the CLI default when there is none.",
  "aiassist.prose_models": "Model for prose",
  "aiassist.note_prose_models":
    "Used for File pane edit suggestions and chat plan updates. These write text you read and accept, so the default sits above the short-label tier. The CLI follows the priority above. These used to share one setting with short labels, so choosing a lightweight model for titles quietly downgraded edit suggestions too.",

  "aiassist.features": "Features that use AI assistance",
  "aiassist.note_features":
    "Each can be turned off individually. A feature that is off hides its button entirely — you never press it only to be refused. Turning one off affects neither the others nor the assistant chat. Each feature can also have its own agent and model override — left unset, it follows the priority order and default model above.",
  "aiassist.note_session_title":
    "After a few exchanges in a session with no title, the AI proposes a short title above the session chat. This setting also enables/disables the \"Ask AI\" button in a session's rename dialog.",
  "aiassist.note_chat_title":
    "Enables/disables the \"Ask AI\" button in the assistant chat's rename dialog. Unlike sessions, a chat has no banner that proposes a title on its own — it generates only when you press the button.",
  "aiassist.note_branch_name":
    "When creating a worktree or renaming a branch, suggests a short git-safe branch name from the conversation. It used to be silently gated by \"Session title suggestion\" — which no label ever said.",
  "aiassist.note_reply_suggest_session":
    "Shows a ✨ button on the session mirror's composer that drafts replies from the recent exchange. Tokens are spent only when you press it. Quick replies learned from your own input history (Settings > Keys) use no LLM and are a separate feature, not covered here.",
  "aiassist.note_reply_suggest_chat":
    "Shows a ✨ button on the assistant chat's composer that drafts replies from the recent exchange. Tokens are spent only when you press it. This used to read the session mirror's own setting under a misspelled key that never actually matched, so the chat's ✨ ran unconditionally regardless of this switch. It is its own setting from here on.",
  "aiassist.note_edit_suggest":
    "While editing a file in the File pane, proposes a replacement for the selection from your instruction (you review it before accepting or discarding). This previously had no setting and was always on.",
  "aiassist.note_plan_update":
    "Enables/disables the assistant chat's \"refresh\" button, which re-derives the work plan from the recent exchange. Separate from the automatic summary carry-forward (context compaction) — this is the button you press explicitly. This previously had no setting and was always on.",
  "aiassist.note_mirror_translate":
    "Shows a \"Translate\" button on mirror answers that came back in another language. Tokens are spent only when you press it, and no session turn is used. A translation is kept until that session is deleted, so the same text with the same agent/model is free from the second press on. Changing this feature's agent or model means there is no translation yet for that combination, so the next press generates a fresh one (the earlier agent/model's translation is not lost — it comes back if you switch back).",
  "aiassist.mirror_auto_translate": "Translate automatically",
  "aiassist.note_mirror_auto_translate":
    "Presses that button for you the moment a turn finishes, for answers that arrive while you are watching that session. Off by default: this is the one translation setting that spends without being asked. Answers already on screen when you open a session are never translated on their own — the button is still there for them.",

  "aiassist.feature_agent": "Agent",
  "aiassist.feature_agent_auto": "Auto (priority order)",
  "aiassist.feature_model": "Model",
  "aiassist.feature_model_auto_note": "Auto follows the default model. To pick a concrete model, pin the agent above to one CLI first.",
  "aiassist.feature_model_follow_default": "Default (follow the setting above)",
  "aiassist.currently_using": "Currently uses: {agent} / {model}",
  "aiassist.currently_using_unknown": "Currently uses: not yet known",
};
