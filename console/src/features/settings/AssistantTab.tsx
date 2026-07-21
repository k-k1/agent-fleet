import type { ReactNode } from "react";
import {
  useSettings,
  setSetting,
  OUTPUT_LANGUAGES,
  ASSISTANT_AGENT_KINDS,
  normalizeAssistantOrder,
} from "../../lib/settings.ts";
import { agentOf } from "../../agents/registry.ts";
import { Choice, OnOff, OrderList } from "./controls.tsx";
import { useT } from "../../lib/i18n/index.ts";

// AssistantTab — アシスタント・チャットの挙動設定（タイトルAI提案 / 回答言語 /
// エージェント優先順位 / 報告への自動応答 / コンテキスト自動圧縮）。もとは AgentsTab の
// 「セッション」グループに同居していたが、アシスタント固有の項目が増えたため TtsTab と
// 同様に独立タブへ分離。タイトル提案の ON/OFF もセッション用（autoTitleSuggest、
// AgentsTab 残置）とチャット用（assistantTitleSuggest、ここ）に分かれている。
// すべてクライアント側の設定（settings store）なので、ワークスペースの起動状態に
// 依らず表示・変更できる。
export function AssistantTab() {
  const tr = useT();
  const s = useSettings();
  return (
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
      <Row label={tr("assistant.auto_turn")}>
        <OnOff value={s.assistantAutoTurn} onChange={(v) => setSetting("assistantAutoTurn", v)} />
      </Row>
      <p className="muted ds-note">{tr("assistant.note_auto_turn")}</p>
      <Row label={tr("assistant.auto_compact")}>
        <OnOff value={s.assistantAutoCompact} onChange={(v) => setSetting("assistantAutoCompact", v)} />
      </Row>
      <p className="muted ds-note">{tr("assistant.note_auto_compact")}</p>
    </section>
  );
}

// A labeled settings row (mirrors DisplayTab's Row).
function Row({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}
