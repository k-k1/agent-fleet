import { useSettings, setSetting, ASSISTANT_AGENT_KINDS, normalizeAssistantOrder } from "../../../lib/settings.ts";
import { AI_ASSIST_FEATURES, AI_FEATURE_MODEL_FOLLOW_DEFAULT, type AiAssistFeatureDef } from "../../../lib/aiAssistFeatures.ts";
import { useAiAssistResolution, type AiAssistResolutionRow } from "../../../lib/aiAssistResolution.ts";
import { agentOf } from "../../../agents/registry.ts";
import { OnOff, OrderList, Row, Select } from "../parts/controls.tsx";
import { AiModelRow, useResolvedModelLabel, type AiAgentKind } from "../parts/aiModelRow.tsx";
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
  // Called ONCE here, not once per card (103-impl-review 重大2: a per-card call fanned one GET
  // /ai-assist/resolution out into 8 identical requests per mount — measured 48 req/min while
  // the tab stayed open — even though the Agent already answers all 8 features in one
  // response). Passed down as a prop instead.
  const resolution = useAiAssistResolution();
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
          <AiFeatureCard key={f.id} f={f} row={resolution?.[f.id]} />
        ))}
      </section>
    </>
  );
}

function AiFeatureCard({ f, row }: { f: AiAssistFeatureDef; row: AiAssistResolutionRow | undefined }) {
  const tr = useT();
  const s = useSettings();
  const enabled = !!s[f.enabledKey];
  const pin = s.aiFeatureAgents?.[f.id] || "";

  // Y (the model name — decision 5): the Agent only ever answers X (kind); the Console draws Y
  // from the catalog it already fetches (docs/log/103-impl-review 中9). Pinned, this is the
  // feature's own override; unpinned, it is the shared tier default for whatever kind the
  // Agent says is actually resolved right now — never a guess at a kind nothing has confirmed,
  // which is why the hook always runs (hooks can't be conditional) but its result is only
  // RENDERED once row?.kind makes the fallback below moot.
  const tierModels = f.tier === "prose" ? s.aiProseModels : s.aiShortModels;
  const modelKind = (pin || row?.kind || "claude") as AiAgentKind;
  // No per-feature override (undefined, key never written) falls through to §1's tier default
  // for the resolved kind — same as an unpinned feature, just anchored to the pinned kind
  // instead of row?.kind (103-final-review 中3: collapsing this to "" made a pinned-but-
  // unconfigured card claim "推奨" while the Agent actually ran the tier default). An override
  // explicitly cleared to "" stays "" here on purpose — useResolvedModelLabel reads that as "CLI
  // default", not "recommended".
  const override = pin ? s.aiFeatureModels?.[f.id]?.[pin] : undefined;
  const modelValue = override ?? tierModels?.[modelKind];
  const modelLabel = useResolvedModelLabel(modelKind, f.tier, modelValue);

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
              value={s.aiFeatureModels?.[f.id]?.[pin] || AI_FEATURE_MODEL_FOLLOW_DEFAULT}
              extraOption={[AI_FEATURE_MODEL_FOLLOW_DEFAULT, tr("aiassist.feature_model_follow_default")]}
              onChange={(model) => {
                // "follow default" is never stored — it maps to DELETING the override, not to
                // writing a third sentinel value the Agent would have to know about
                // (docs/log/103-impl-review 中6: the picker used to have no way back to "follow
                // §1" once touched, and showed "推奨" for a value that was actually unset).
                const byFeature = { ...s.aiFeatureModels?.[f.id] };
                if (model === AI_FEATURE_MODEL_FOLLOW_DEFAULT) {
                  delete byFeature[pin];
                } else {
                  byFeature[pin] = model;
                }
                const next = { ...s.aiFeatureModels };
                if (Object.keys(byFeature).length === 0) {
                  delete next[f.id];
                } else {
                  next[f.id] = byFeature;
                }
                setSetting("aiFeatureModels", next);
              }}
            />
          ) : (
            // decision 4's invariant: "auto" has no fixed kind to store a concrete model
            // under, so only the tier default is on offer here — never a catalog pick.
            <Row label={tr("aiassist.feature_model")}>
              <span className="muted">{tr("aiassist.feature_model_auto_note")}</span>
            </Row>
          )}
          {/* "currently uses": the Agent answers X (decision 5); undefined while the
              availability cache is cold reads as "not known yet", never as a guess. */}
          <p className="muted ds-note ai-feature-current">
            {row?.kind
              ? tr("aiassist.currently_using", { agent: agentOf(row.kind).assistantName, model: modelLabel })
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
