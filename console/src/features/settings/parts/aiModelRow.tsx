// AiModelRow — 「CLI ごとにモデルを選ぶ 1 行」。アシスタント・チャット（AssistantTab）と
// AI 補助生成（AiAssistTab）の両方が同じ形で使う。
//
// tier は「その呼び出しが何を必要とするか」で、置き場所（どのタブか）ではない:
//   chat  … 会話。強いモデルが要る。
//   prose … 人が読んで残す文章（ファイル編集の提案・計画の更新）。chat と同じ中位。
//   short … 短いラベル（タイトル・ブランチ名・返信候補）。安い高速モデルで足りる。
// ★ prose と short を分けているのが要点。以前は 1 つの「ユーティリティ」設定が
//   両方を兼ね、タイトル用に haiku を選ぶとファイル編集の提案まで haiku に落ちていた。
import { ASSISTANT_AGENT_KINDS, ASSISTANT_RECOMMENDED_MODEL } from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { Row, Select } from "./controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useHiddenModel, useModelOptions } from "../../../lib/agentModels.ts";

export type AiModelTier = "chat" | "prose" | "short";

export type AiAgentKind = (typeof ASSISTANT_AGENT_KINDS)[number];

// kind × tier → 推奨モデル ID の解決。catalog 依存の分岐（cheap 探索 / ids に居るかの
// 確認）があるため定数表ではなく関数で持つ。
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
  // 設定で除外したモデルはこの救済から外す（消えた ≠ 隠した）— Agent 側は除外値を
  // 未設定として扱い推奨へ退避するので、足し戻すと表示のほうが嘘になる。
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
