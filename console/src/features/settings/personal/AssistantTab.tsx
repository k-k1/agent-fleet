import {
  useSettings,
  setSetting,
  OUTPUT_LANGUAGES,
  ASSISTANT_AGENT_KINDS,
  normalizeAssistantOrder,
  REGION_THEMES,
} from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { SwatchGrid } from "../../../ui/SwatchGrid.tsx";
import { Choice, OnOff, OrderList, Row, Select, Slider } from "../parts/controls.tsx";
import { AiModelRow } from "../parts/aiModelRow.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useHiddenModel, useModelOptions } from "../../../lib/agentModels.ts";

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

// AssistantTab — **アシスタント・チャットだけ**の設定：挙動（回答言語 / モデル /
// 報告への自動応答 / コンテキスト自動圧縮）と 外観（テーマ / 背景色）。外観は以前
// DisplayTab にあったが、アシスタントの挙動と同じタブに置いた方が見つけやすいので
// ここへ移設。
//
// ★ かつてここに在った「タイトルのAI提案」「エージェント優先順位」「タイトル・
// サジェストのモデル」は AiAssistTab へ移した（docs/log/84）。実装をチャットと共有して
// いるだけで、効く先はセッション・ミラー・File ペイン＝アシスタントではなかった。
// このタブに残すのは「アシスタントとの会話そのもの」を変える設定に限る。
//
// すべてクライアント側の設定（settings store）なので、ワークスペースの起動状態に
// 依らず表示・変更できる。
export function AssistantTab() {
  const tr = useT();
  const s = useSettings();
  return (
    <>
      <section className="ds-group">
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
          <AiModelRow
            key={`assistant-${kind}`}
            kind={kind}
            tier="chat"
            value={s.assistantModels?.[kind] || ""}
            onChange={(model) => setSetting("assistantModels", { ...s.assistantModels, [kind]: model })}
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
