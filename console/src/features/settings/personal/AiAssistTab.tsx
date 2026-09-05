import { useSettings, setSetting, ASSISTANT_AGENT_KINDS, normalizeAssistantOrder } from "../../../lib/settings.ts";
import { agentOf } from "../../../agents/registry.ts";
import { OnOff, OrderList, Row } from "../parts/controls.tsx";
import { AiModelRow } from "../parts/aiModelRow.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// AiAssistTab — "AI assisted generation". One place for the settings of every feature that
// runs as a single one-shot headless call (OneShotHeadless on the Agent side) rather than a
// conversation: session and chat titles, branch names, reply suggestions (✨), file-edit
// suggestions and the chat plan update.
//
// Why it is its own tab (docs/log/84): these features are grouped by the surface the user sees
// (sessions, mirror, the File pane) rather than by the implementation they happen to share
// with the assistant chat. Grouping them by implementation scattered the on/off switches over
// three tabs and left branch names and edit suggestions with no toggle at all.
//
// The priority order and models are a separate set from the assistant's for the same reason:
// these run all the time, so the choice is "cheap, fast and good enough", where the chat wants
// the strongest model.
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

      {/* On/off per feature, one key per feature: sharing a key makes one setting silently
          disable another (the session title suggestion used to stop branch names too). */}
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
