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

export function AiModelRow({
  kind,
  tier,
  value,
  onChange,
}: {
  kind: AiAgentKind;
  tier: AiModelTier;
  value: string;
  onChange: (v: string) => void;
}) {
  const tr = useT();
  const live = useModelOptions(kind) || [["", tr("ui.default")]];
  const ids = live.map(([id]) => id);
  const cheap = ids.find((id) =>
    ["mini", "flash", "lite", "small", "nano", "haiku"].some((x) => id.toLowerCase().includes(x)),
  );
  const recommended = recommendedModelId(kind, tier, ids, cheap);
  const resolvedLabel = live.find(([id]) => id === recommended)?.[1] || recommended || tr("ui.default");
  const recommendedOption: [string, string] = [
    ASSISTANT_RECOMMENDED_MODEL,
    tr("assistant.recommended_now", { model: resolvedLabel }),
  ];
  const choices = [recommendedOption, ...live];
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
