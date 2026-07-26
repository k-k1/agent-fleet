import {
  useSettings,
  setSetting,
  OUTPUT_LANGUAGES,
  ASSISTANT_AGENT_KINDS,
  ASSISTANT_RECOMMENDED_MODEL,
  normalizeAssistantOrder,
  REGION_THEMES,
} from "../../lib/settings.ts";
import { agentOf } from "../../agents/registry.ts";
import { SwatchGrid } from "../../ui/SwatchGrid.tsx";
import { Choice, OnOff, OrderList, Row, Select, Slider } from "./controls.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useModelOptions } from "../../lib/agentModels.ts";

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
  const recommended = utility
    ? kind === "claude"
      ? "haiku"
      : kind === "codex"
        ? cheap || ""
        : kind === "opencode" && ids.includes("opencode-go/deepseek-v4-flash")
          ? "opencode-go/deepseek-v4-flash"
          : kind === "agy"
            ? "Gemini 3.5 Flash (Medium)"
            : ""
    : kind === "claude"
      ? "sonnet"
      : kind === "codex"
        ? "gpt-5.6-luna"
        : kind === "opencode"
          ? ids.includes("opencode-go/glm-5.2")
            ? "opencode-go/glm-5.2"
            : "opencode/nemotron-3-ultra-free"
          : kind === "agy"
            ? "Gemini 3.5 Flash (Medium)"
            : "";
  const resolvedLabel = live.find(([id]) => id === recommended)?.[1] || recommended || tr("ui.default");
  const recommendedOption: [string, string] = [
    ASSISTANT_RECOMMENDED_MODEL,
    tr("assistant.recommended_now", { model: resolvedLabel }),
  ];
  const choices = [recommendedOption, ...live];
  // Preserve a configured model that temporarily disappeared from a live catalog
  // (workspace stopped, provider disconnected, upstream rename). Dropping it from
  // the select would make the visible value lie about the persisted setting.
  const options =
    value && !choices.some(([id]) => id === value) ? [...choices, [value, value] as [string, string]] : choices;
  return (
    <Row label={agentOf(kind).assistantName}>
      <Select value={value} options={options} onChange={onChange} />
    </Row>
  );
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
