import { useSettings, setSetting, ASSISTANT_AGENT_KINDS, ASSISTANT_RECOMMENDED_MODEL, normalizeAssistantOrder } from "../../../lib/settings.ts";
import { AI_ASSIST_FEATURES, type AiAssistFeatureDef } from "../../../lib/aiAssistFeatures.ts";
import { useAiAssistResolution } from "../../../lib/aiAssistResolution.ts";
import { agentOf } from "../../../agents/registry.ts";
import { OnOff, OrderList, Row, Select } from "../parts/controls.tsx";
import { AiModelRow, type AiAgentKind } from "../parts/aiModelRow.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// AiAssistTab — "AI assisted generation". One place for the settings of every feature that
// runs as a single one-shot headless call (OneShotHeadless on the Agent side) rather than a
// conversation: session and chat titles, branch names, reply suggestions (✨), file-edit
// suggestions, the chat plan update and mirror translation.
//
// Why it is its own tab (docs/log/84): these features are grouped by the surface the user sees
// (sessions, mirror, the File pane) rather than by the implementation they happen to share
// with the assistant chat. Grouping them by implementation scattered the on/off switches over
// three tabs and left branch names and edit suggestions with no toggle at all.
//
// Two sections (docs/log/103 §103.7):
//   §1 (below) — the shared default: priority order plus a model per CLI, split by tier
//     (short label vs. prose). Unchanged from docs/log/84.
//   §2 (AiFeatureCard) — one card per feature. Left on "follow the default" (the common case),
//     a card is one row; a feature can also be pinned to its own agent and, once pinned, its
//     own model (docs/log/103 decision 1/4).
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

      <section className="ds-group">
        <h4 className="ds-title">{tr("aiassist.features")}</h4>
        <p className="muted ds-note">{tr("aiassist.note_features")}</p>
        {AI_ASSIST_FEATURES.map((f) => (
          <AiFeatureCard key={f.id} f={f} />
        ))}
      </section>
    </>
  );
}

function AiFeatureCard({ f }: { f: AiAssistFeatureDef }) {
  const tr = useT();
  const s = useSettings();
  const resolution = useAiAssistResolution();
  const enabled = !!s[f.enabledKey];
  const pin = s.aiFeatureAgents?.[f.id] || "";
  const row = resolution?.[f.id];

  return (
    <div className="ds-subgroup ai-feature-card">
      <Row label={tr(f.labelKey)}>
        <OnOff value={enabled} onChange={(v) => setSetting(f.enabledKey, v)} />
      </Row>
      <p className="muted ds-note">{tr(f.noteKey)}</p>
      {/* A feature turned off shows no agent/model row (docs/log/103 §103.7): a picker left
          visible under an OFF feature would answer a question that no longer applies. */}
      {enabled && (
        <>
          <Row label={tr("aiassist.feature_agent")}>
            <Select
              value={pin}
              options={[
                ["", tr("aiassist.feature_agent_auto")],
                ...ASSISTANT_AGENT_KINDS.map((k): [string, string] => [k, agentOf(k).assistantName]),
              ]}
              onChange={(v: string) => setSetting("aiFeatureAgents", { ...s.aiFeatureAgents, [f.id]: v })}
            />
          </Row>
          {pin ? (
            <AiModelRow
              kind={pin as AiAgentKind}
              tier={f.tier}
              value={s.aiFeatureModels?.[f.id]?.[pin] || ASSISTANT_RECOMMENDED_MODEL}
              onChange={(model) =>
                setSetting("aiFeatureModels", {
                  ...s.aiFeatureModels,
                  [f.id]: { ...s.aiFeatureModels?.[f.id], [pin]: model },
                })
              }
            />
          ) : (
            // decision 4's invariant: "auto" has no fixed kind to store a concrete model
            // under, so only the tier default is on offer here — never a catalog pick.
            <Row label={tr("aiassist.feature_model")}>
              <span className="muted">{tr("aiassist.feature_model_auto_note")}</span>
            </Row>
          )}
          {/* "currently uses": the Agent answers the backend (decision 5); undefined while the
              availability cache is cold reads as "not known yet", never as a guess. */}
          <p className="muted ds-note ai-feature-current">
            {row?.kind
              ? tr("aiassist.currently_using", { agent: agentOf(row.kind).assistantName })
              : tr("aiassist.currently_using_unknown")}
          </p>
          {/* Auto-fire (docs/log/97 §97.12) is its own axis, deliberately untouched by this
              round (§103.11): "press it for me" is a different question from "which agent/
              model answers it", and folding them into one card would make one card answer two
              questions. Kept here rather than promoted to its own card because it only makes
              sense once translate.mirror itself is on. */}
          {f.id === "translate.mirror" && (
            <>
              <Row label={tr("aiassist.mirror_auto_translate")}>
                <OnOff value={s.mirrorAutoTranslate} onChange={(v) => setSetting("mirrorAutoTranslate", v)} />
              </Row>
              <p className="muted ds-note">{tr("aiassist.note_mirror_auto_translate")}</p>
            </>
          )}
        </>
      )}
    </div>
  );
}
