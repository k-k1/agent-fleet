import { useSettings, setSetting, ASSISTANT_AGENT_KINDS, normalizeAssistantOrder } from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { OnOff, OrderList, Row } from "../parts/controls.tsx";
import { AiModelRow } from "../parts/aiModelRow.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// AiAssistTab — 「AI 補助生成」。会話ではなく **1 回きりのヘッドレス呼び出し**
// （Agent 側 OneShotHeadless）で回っている機能の設定を、ここ 1 箇所に集める:
// セッション/チャットのタイトル、ブランチ名、返信候補（✨）、ファイル編集の提案、
// チャットの計画更新。
//
// なぜ独立タブなのか（docs/log/84）。これらは実装をアシスタント・チャットと共有して
// いるという理由だけで「設定 > アシスタント」に置かれていたが、利用者から見える面は
// セッション・ミラー・File ペインで、アシスタントではない。おまけに ON/OFF は
// エージェントタブ（セッションのタイトル）・アシスタントタブ（チャットのタイトル）・
// キー操作タブ（返信候補）の 3 箇所に散り、ブランチ名とファイル編集提案には
// そもそもトグルが無かった。分類の軸を「実装の共有先」から「利用者が見る面」へ
// 移し、散っていた分をここへ集約している。
//
// 優先順位とモデルがアシスタントと別系統なのも同じ理由で、こちらは常時走るので
// 「安くて速い、動くもの」を選びたい（チャットは強いものを選びたい）。
export function AiAssistTab() {
  const tr = useT();
  const s = useSettings();
  return (
    <>
      <section className="ds-group">
        <Row label={tr("aiassist.agent_order")}>
          <OrderList
            value={normalizeAssistantOrder(s.aiAssistOrder)}
            labels={Object.fromEntries(ASSISTANT_AGENT_KINDS.map((k) => [k, agentOf(k).assistantName]))}
            onChange={(v) => setSetting("aiAssistOrder", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("aiassist.note_agent_order")}</p>

        <h4 className="ds-title">{tr("aiassist.short_models")}</h4>
        <p className="muted ds-note">{tr("aiassist.note_short_models")}</p>
        {ASSISTANT_AGENT_KINDS.map((kind) => (
          <AiModelRow
            key={`short-${kind}`}
            kind={kind}
            tier="short"
            value={s.aiShortModels?.[kind] || ""}
            onChange={(model) => setSetting("aiShortModels", { ...s.aiShortModels, [kind]: model })}
          />
        ))}

        <h4 className="ds-title">{tr("aiassist.prose_models")}</h4>
        <p className="muted ds-note">{tr("aiassist.note_prose_models")}</p>
        {ASSISTANT_AGENT_KINDS.map((kind) => (
          <AiModelRow
            key={`prose-${kind}`}
            kind={kind}
            tier="prose"
            value={s.aiProseModels?.[kind] || ""}
            onChange={(model) => setSetting("aiProseModels", { ...s.aiProseModels, [kind]: model })}
          />
        ))}
      </section>

      {/* 機能ごとの ON/OFF。1 機能 1 キー — 以前はセッションのタイトル提案の設定が
          ブランチ名の提案まで黙って止めていた。 */}
      <section className="ds-group">
        <h4 className="ds-title">{tr("aiassist.features")}</h4>
        <p className="muted ds-note">{tr("aiassist.note_features")}</p>
        <Row label={tr("aiassist.session_title")}>
          <OnOff value={s.autoTitleSuggest} onChange={(v) => setSetting("autoTitleSuggest", v)} />
        </Row>
        <p className="muted ds-note">{tr("aiassist.note_session_title")}</p>
        <Row label={tr("aiassist.chat_title")}>
          <OnOff value={s.assistantTitleSuggest} onChange={(v) => setSetting("assistantTitleSuggest", v)} />
        </Row>
        <p className="muted ds-note">{tr("aiassist.note_chat_title")}</p>
        <Row label={tr("aiassist.branch_name")}>
          <OnOff value={s.branchSuggestEnabled} onChange={(v) => setSetting("branchSuggestEnabled", v)} />
        </Row>
        <p className="muted ds-note">{tr("aiassist.note_branch_name")}</p>
        <Row label={tr("aiassist.reply_suggest")}>
          <OnOff value={s.replySuggestEnabled} onChange={(v) => setSetting("replySuggestEnabled", v)} />
        </Row>
        <p className="muted ds-note">{tr("aiassist.note_reply_suggest")}</p>
        <Row label={tr("aiassist.edit_suggest")}>
          <OnOff value={s.editSuggestEnabled} onChange={(v) => setSetting("editSuggestEnabled", v)} />
        </Row>
        <p className="muted ds-note">{tr("aiassist.note_edit_suggest")}</p>
      </section>
    </>
  );
}
