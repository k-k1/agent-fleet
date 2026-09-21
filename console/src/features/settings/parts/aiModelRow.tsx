// AiModelRow — one row for picking a model per CLI. Both the assistant chat tab
// (AssistantTab) and AI-assisted generation (AiAssistTab) use it in the same shape.
//
// A tier says what the call needs, not where the row sits:
//   chat  … conversation; needs a strong model.
//   prose … text a person reads and keeps (file-edit proposals, plan updates); mid, as chat.
//   short … short labels (titles, branch names, reply suggestions); a cheap fast model does.
// Splitting prose from short is the point: one shared "utility" setting covered both, so
// choosing haiku for titles silently downgraded file-edit proposals to haiku as well.
import { ASSISTANT_AGENT_KINDS, ASSISTANT_RECOMMENDED_MODEL } from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { Row, Select } from "./controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useHiddenModel, useModelOptions } from "../../../lib/agentModels.ts";

export type AiModelTier = "chat" | "prose" | "short";

export type AiAgentKind = (typeof ASSISTANT_AGENT_KINDS)[number];

// Resolves kind x tier to a recommended model id. A function rather than a constant table
// because it branches on the live catalog (the cheap-model search, the presence check on ids).
function recommendedModelId(kind: AiAgentKind, tier: AiModelTier, ids: string[], cheap: string | undefined): string {
  const short = tier === "short";
  switch (kind) {
    case "claude":
      return short ? "haiku" : "sonnet";
    case "codex":
      return short ? cheap || "" : "gpt-5.6-luna";
    case "opencode":
      if (short) return ids.includes("opencode-go/deepseek-v4-flash") ? "opencode-go/deepseek-v4-flash" : "";
      return ids.includes("opencode-go/glm-5.2") ? "opencode-go/glm-5.2" : "opencode/nemotron-3-ultra-free";
    case "agy":
      return "Gemini 3.5 Flash (Medium)";
    default:
      return "";
  }
}

// useResolvedModelLabel answers "what does this kind x tier x stored value actually show/run
// as", for both AiModelRow's own "推奨（現在: X）" option label and AiFeatureCard's "いま使う
// のは" line (docs/log/103 decision 5's Y — the Console draws it from the catalog it already
// has, never from a guess the Agent cannot confirm).
//
// 103-impl-review (a): the recommended label used to fall back to the RAW recommended id when
// hidden models excluded it from `live` (`|| recommended`) — showing "推奨（現在: haiku）" for a
// user who put haiku in "models not to use", while the Agent's own visibleModel() falls through
// to the CLI default in that exact case (chat_providers.go's recommendedUtilityModel). Fixed
// here: a recommended id absent from the VISIBLE catalog resolves to ui.default, matching what
// actually runs.
// value is string | undefined, not just string, because the two carry different meanings a
// caller can produce (103-final-review 中3): undefined ⇒ nothing decided this at any level, so
// show what "推奨" resolves to; "" ⇒ something in the chain explicitly picked the CLI's own
// default (assistantModelPref's `ok` marks this the same way on the Agent side), which is a
// different answer and must not fall into the "推奨" branch just because it is falsy.
export function useResolvedModelLabel(kind: AiAgentKind, tier: AiModelTier, value: string | undefined): string {
  const tr = useT();
  const live = useModelOptions(kind) || [["", tr("ui.default")]];
  const ids = live.map(([id]) => id);
  const cheap = ids.find((id) =>
    ["mini", "flash", "lite", "small", "nano", "haiku"].some((x) => id.toLowerCase().includes(x)),
  );
  const recommended = recommendedModelId(kind, tier, ids, cheap);
  const recommendedVisible = live.some(([id]) => id === recommended);
  const recommendedLabel = recommendedVisible ? live.find(([id]) => id === recommended)![1] : tr("ui.default");
  if (value === undefined || value === ASSISTANT_RECOMMENDED_MODEL) {
    return tr("assistant.recommended_now", { model: recommendedLabel });
  }
  if (value === "") return tr("ui.default");
  return live.find(([id]) => id === value)?.[1] || value;
}

export function AiModelRow({
  kind,
  tier,
  value,
  onChange,
  extraOption,
}: {
  kind: AiAgentKind;
  tier: AiModelTier;
  value: string;
  onChange: (v: string) => void;
  /** An extra choice prepended before "推奨" (docs/log/103 中6 — AiFeatureCard's "既定（上の
   *  設定に従う）", mapped by the caller's onChange to deleting the per-feature override rather
   *  than storing this sentinel literally). Omitted by every other caller. */
  extraOption?: [string, string];
}) {
  const tr = useT();
  const live = useModelOptions(kind) || [["", tr("ui.default")]];
  // Same resolution AiFeatureCard's "currently uses" line draws Y from (useResolvedModelLabel) —
  // one function decides what "推奨" resolves to, so the two can never drift apart again the way
  // the pre-fix duplicate here did (103-impl-review (a)).
  const recommendedOption: [string, string] = [
    ASSISTANT_RECOMMENDED_MODEL,
    useResolvedModelLabel(kind, tier, ASSISTANT_RECOMMENDED_MODEL),
  ];
  const choices = extraOption ? [extraOption, recommendedOption, ...live] : [recommendedOption, ...live];
  // Preserve a configured model that temporarily disappeared from a live catalog
  // (workspace stopped, provider disconnected, upstream rename). Dropping it from
  // the select would make the visible value lie about the persisted setting.
  // A model hidden in settings is excluded from that rescue (gone is not hidden): the Agent
  // treats a hidden value as unset and falls back to the recommendation, so adding it back
  // would make the display the thing that lies.
  const hidden = useHiddenModel(kind, value);
  const options =
    value && !hidden && !choices.some(([id]) => id === value)
      ? [...choices, [value, value] as [string, string]]
      : choices;
  return (
    <Row label={agentOf(kind).assistantName}>
      <Select value={value} options={options} onChange={onChange} />
    </Row>
  );
}
