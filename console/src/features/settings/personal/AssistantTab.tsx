import {
  useSettings,
  setSetting,
  OUTPUT_LANGUAGES,
  ASSISTANT_AGENT_KINDS,
  ASSISTANT_RECOMMENDED_MODEL,
  normalizeAssistantOrder,
  REGION_THEMES,
} from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { SwatchGrid } from "../../../ui/SwatchGrid.tsx";
import { Choice, OnOff, OrderList, Row, Select, Slider } from "../parts/controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useHiddenModel, useModelOptions } from "../../../lib/agentModels.ts";

// kind → 推奨モデル ID の解決。utility（タイトル提案などの軽量用途）と本回答とで別。
// catalog 依存の分岐（cheap 探索 / ids に居るかの確認）があるため定数表ではなく関数で持つ。
function recommendedModelId(
  kind: (typeof ASSISTANT_AGENT_KINDS)[number],
  utility: boolean,
  ids: string[],
  cheap: string | undefined,
): string {
  switch (kind) {
    case "claude":
      return utility ? "haiku" : "sonnet";
    case "codex":
      return utility ? cheap || "" : "gpt-5.6-luna";
    case "opencode":
      if (utility) return ids.includes("opencode-go/deepseek-v4-flash") ? "opencode-go/deepseek-v4-flash" : "";
      return ids.includes("opencode-go/glm-5.2") ? "opencode-go/glm-5.2" : "opencode/nemotron-3-ultra-free";
    case "agy":
      return "Gemini 3.5 Flash (Medium)";
    default:
      return "";
  }
}

function AssistantModelRow({
  kind,
  utility,
  value,
  onChange,
}: {
  kind: (typeof ASSISTANT_AGENT_KINDS)[number];
  utility: boolean;
  value: string;
  onChange: (v: string) => void;
}) {
  const tr = useT();
  const live = useModelOptions(kind) || [["", tr("ui.default")]];
  const ids = live.map(([id]) => id);
  const cheap = ids.find((id) => ["mini", "flash", "lite", "small", "nano", "haiku"].some((x) => id.toLowerCase().includes(x)));
  const recommended = recommendedModelId(kind, utility, ids, cheap);
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

// AutoTurnModelSelect — 自動応答（セッション報告への自動ターン）専用モデル。対象は
// claude の会話のみなので catalog も claude 固定。空 = 会話のモデルのまま。
// AssistantModelRow と同じく、catalog から消えた設定値も選択肢に残して表示が嘘を
// つかないようにする。
function AutoTurnModelSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const tr = useT();
  const live = useModelOptions("claude") || [];
  const hidden = useHiddenModel("claude", value);
  const choices: [string, string][] = [["", tr("assistant.auto_turn_model_default")], ...live];
  const options =
    value && !hidden && !choices.some(([id]) => id === value)
      ? [...choices, [value, value] as [string, string]]
      : choices;
  return <Select value={value} options={options} onChange={onChange} />;
}

// AssistantTab — アシスタント・チャットの設定：挙動（タイトルAI提案 / 回答言語 /
// エージェント優先順位 / 報告への自動応答 / コンテキスト自動圧縮）と 外観（テーマ /
// 背景色）。外観は以前 DisplayTab にあったが、アシスタントの挙動と同じタブに置いた方が
// 見つけやすいのでここへ移設。もとは AgentsTab の「セッション」グループに同居していたが、
// アシスタント固有の項目が増えたため TtsTab と同様に独立タブへ分離。タイトル提案の
// ON/OFF もセッション用（autoTitleSuggest、AgentsTab 残置）とチャット用
// （assistantTitleSuggest、ここ）に分かれている。すべてクライアント側の設定
// （settings store）なので、ワークスペースの起動状態に依らず表示・変更できる。
export function AssistantTab() {
  const tr = useT();
  const s = useSettings();
  return (
    <>
      <section className="ds-group">
        <Row label={tr("assistant.title_suggest")}>
          <OnOff value={s.assistantTitleSuggest} onChange={(v) => setSetting("assistantTitleSuggest", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_title_suggest")}</p>
        <Row label={tr("assistant.output_language")}>
          <Choice
            value={s.outputLanguage}
            options={OUTPUT_LANGUAGES.map(([id, k]) => [id, tr(k)])}
            onChange={(v) => setSetting("outputLanguage", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_output_language")}</p>
        <Row label={tr("assistant.agent_order")}>
          <OrderList
            value={normalizeAssistantOrder(s.assistantAgentOrder)}
            labels={Object.fromEntries(ASSISTANT_AGENT_KINDS.map((k) => [k, agentOf(k).assistantName]))}
            onChange={(v) => setSetting("assistantAgentOrder", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_agent_order")}</p>
        <h4 className="ds-title">{tr("assistant.models")}</h4>
        <p className="muted ds-note">{tr("assistant.note_models")}</p>
        {ASSISTANT_AGENT_KINDS.map((kind) => (
          <AssistantModelRow
            key={`assistant-${kind}`}
            kind={kind}
            utility={false}
            value={s.assistantModels?.[kind] || ""}
            onChange={(model) => setSetting("assistantModels", { ...s.assistantModels, [kind]: model })}
          />
        ))}
        <h4 className="ds-title">{tr("assistant.utility_models")}</h4>
        <p className="muted ds-note">{tr("assistant.note_utility_models")}</p>
        {ASSISTANT_AGENT_KINDS.map((kind) => (
          <AssistantModelRow
            key={`utility-${kind}`}
            kind={kind}
            utility
            value={s.assistantUtilityModels?.[kind] || ""}
            onChange={(model) =>
              setSetting("assistantUtilityModels", { ...s.assistantUtilityModels, [kind]: model })
            }
          />
        ))}
        <Row label={tr("assistant.auto_turn")}>
          <OnOff value={s.assistantAutoTurn} onChange={(v) => setSetting("assistantAutoTurn", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_turn")}</p>
        <Row label={tr("assistant.auto_turn_limit")}>
          <Slider
            value={s.assistantAutoTurnLimit}
            min={1}
            max={50}
            step={1}
            format={(v) => String(v)}
            onChange={(v) => setSetting("assistantAutoTurnLimit", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_turn_limit")}</p>
        <Row label={tr("assistant.auto_turn_model")}>
          <AutoTurnModelSelect
            value={s.assistantAutoTurnModel}
            onChange={(v) => setSetting("assistantAutoTurnModel", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_turn_model")}</p>
        <Row label={tr("assistant.auto_turn_delay")}>
          <Slider
            value={s.assistantAutoTurnDelay}
            min={0}
            max={300}
            step={15}
            format={(v) => (v ? `${v}s` : tr("assistant.auto_turn_delay_off"))}
            onChange={(v) => setSetting("assistantAutoTurnDelay", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_turn_delay")}</p>
        <Row label={tr("assistant.quiet_completion")}>
          <OnOff value={s.assistantQuietCompletion} onChange={(v) => setSetting("assistantQuietCompletion", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_quiet_completion")}</p>
        <Row label={tr("assistant.auto_pilot")}>
          <OnOff value={s.assistantAutoPilot} onChange={(v) => setSetting("assistantAutoPilot", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_pilot")}</p>
        <Row label={tr("assistant.auto_resume")}>
          <OnOff value={s.assistantAutoResume} onChange={(v) => setSetting("assistantAutoResume", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_resume")}</p>
        <Row label={tr("assistant.auto_compact")}>
          <OnOff value={s.assistantAutoCompact} onChange={(v) => setSetting("assistantAutoCompact", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_compact")}</p>
        <Row label={tr("assistant.auto_compact_tokens")}>
          <Slider
            value={s.assistantAutoCompactTokens}
            min={50000}
            max={500000}
            step={10000}
            format={(v) => `${Math.round(v / 1000)}k`}
            onChange={(v) => setSetting("assistantAutoCompactTokens", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_auto_compact_tokens")}</p>
        <Row label={tr("assistant.output_tail")}>
          <Slider
            value={s.assistantOutputTailKiB}
            min={8}
            max={256}
            step={8}
            format={(v) => `${v} KiB`}
            onChange={(v) => setSetting("assistantOutputTailKiB", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_output_tail")}</p>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("assistant.appearance")}</h4>
        <Row label={tr("display.assistant_theme")}>
          <Choice
            value={s.assistantTheme}
            options={REGION_THEMES.map((x) => [x.id, tr(x.labelKey)])}
            onChange={(v) => setSetting("assistantTheme", v)}
          />
        </Row>
        <Row label={tr("surface.assistant.long")}>
          <SwatchGrid theme={s.theme} value={s.assistantColor} onChange={(v) => setSetting("assistantColor", v)} />
        </Row>
        <p className="muted ds-note">{tr("assistant.note_appearance")}</p>
      </section>
    </>
  );
}
