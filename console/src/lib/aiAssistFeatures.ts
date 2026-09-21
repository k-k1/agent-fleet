// lib/aiAssistFeatures.ts — the catalog of the 8 features that run through the Agent's
// OneShotHeadless (docs/log/103 §103.2), shared by the AI assist settings tab (AiAssistTab)
// and the usage view's per-feature breakdown so the same feature carries the same label in
// both places.
//
// Before this file, the usage view named a row "セッション件名の提案" while the settings tab
// called the same feature "セッションのタイトル提案" (docs/log/103 §103.3-4) — two labels for
// one row, because each screen kept its own translation of the same idea. The label key here
// is drawn from the USAGE view's existing catalog (usage.val.feature.<id>), which already had
// the finer session/chat split this table needs (the settings tab used to run reply
// suggestions through one shared toggle — see FEATURE_ENABLED_KEY.suggest.chat below).
//
// Feature ids match the usage ledger's `feature` values verbatim (usagex/ledger.go).
import type { Settings } from "./settings.ts";
import type { MsgKey } from "./i18n/index.ts";

export type AiAssistTier = "short" | "prose";

// Sentinel for a pinned feature's model picker meaning "no per-feature override — follow the
// tier default above" (docs/log/103 中6). Never stored: the caller maps it to DELETING
// aiFeatureModels[feature][kind] rather than writing this string, so the unset state stays
// truly unset (not a third real value the Agent would have to recognize).
export const AI_FEATURE_MODEL_FOLLOW_DEFAULT = "__follow_default__";

/** The subset of Settings keys that are plain on/off switches — the only shape an AI-assist
 *  feature's own toggle can be. */
type BooleanSettingsKey = {
  [K in keyof Settings]: Settings[K] extends boolean ? K : never;
}[keyof Settings];

export interface AiAssistFeatureDef {
  id: string;
  tier: AiAssistTier;
  /** i18n key for the feature's name — the SAME key the usage view renders (§103.3-4). */
  labelKey: MsgKey;
  /** i18n key for the settings tab's longer description of what the feature does. */
  noteKey: MsgKey;
  /** The Settings key this feature's ON/OFF toggle reads and writes. */
  enabledKey: BooleanSettingsKey;
}

// Display order (§103.7's card list) — not otherwise significant.
export const AI_ASSIST_FEATURES: AiAssistFeatureDef[] = [
  {
    id: "title.session",
    tier: "short",
    labelKey: "usage.val.feature.title.session",
    noteKey: "aiassist.note_session_title",
    enabledKey: "autoTitleSuggest",
  },
  {
    id: "title.chat",
    tier: "short",
    labelKey: "usage.val.feature.title.chat",
    noteKey: "aiassist.note_chat_title",
    enabledKey: "assistantTitleSuggest",
  },
  {
    id: "branch.suggest",
    tier: "short",
    labelKey: "usage.val.feature.branch.suggest",
    noteKey: "aiassist.note_branch_name",
    enabledKey: "branchSuggestEnabled",
  },
  {
    id: "suggest.session",
    tier: "short",
    labelKey: "usage.val.feature.suggest.session",
    noteKey: "aiassist.note_reply_suggest_session",
    enabledKey: "replySuggestEnabled",
  },
  {
    id: "suggest.chat",
    tier: "short",
    labelKey: "usage.val.feature.suggest.chat",
    noteKey: "aiassist.note_reply_suggest_chat",
    // A separate key from suggest.session's (docs/log/103 §103.3-3): sharing replySuggestEnabled
    // silently gated the chat's own ✨ on the mirror's setting.
    enabledKey: "assistantReplySuggestEnabled",
  },
  {
    id: "suggest.edit",
    tier: "prose",
    labelKey: "usage.val.feature.suggest.edit",
    noteKey: "aiassist.note_edit_suggest",
    enabledKey: "editSuggestEnabled",
  },
  {
    id: "plan.update",
    tier: "prose",
    labelKey: "usage.val.feature.plan.update",
    noteKey: "aiassist.note_plan_update",
    enabledKey: "planUpdateEnabled",
  },
  {
    id: "translate.mirror",
    tier: "prose",
    labelKey: "usage.val.feature.translate.mirror",
    noteKey: "aiassist.note_mirror_translate",
    enabledKey: "mirrorTranslateEnabled",
  },
];
