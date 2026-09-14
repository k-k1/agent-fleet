// GenerateForm — the left half of the studio (ADR 0081 decisions 4, 7, 8, 9, 11).
//
// Two rules shape almost every control here:
//
//   1. **What a family reads comes from the Agent, never from a table in the browser**
//      (decision 4). `knobs` is the status's word; this file only asks whether a name is in
//      it. An ABSENT `knobs` means "an Agent that predates this ADR", and then every field
//      stays enabled — greying the whole form out on an old workspace would make the pane
//      useless, while a knob the family ignores merely produces the Agent's own warning.
//   2. **Nothing is inserted into the user's text on its own** (decision 7, and the option
//      the ADR rejects by name). Quality prefixes and trigger words are chips that append
//      when pressed; the administrator's negative is a chip that cannot be removed and is
//      never merged into the textarea, so what is in the box is what the person wrote.
import { useMemo } from "react";
import type { KeyboardEvent as RKeyboardEvent } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Slider } from "../../settings/parts/controls.tsx";
import { loraTriggers, loraWeight, type ImagegenLora, type ImagegenModel, type ImagegenProvider, type Knob } from "../wire.ts";
import { familyCard, sizeOptions } from "../families.ts";
import { MAX_BATCH, MAX_JOBS, OPS, type ImagegenDraft } from "../draft.ts";
import { InputPicker } from "./InputPicker.tsx";

interface Props {
  draft: ImagegenDraft;
  patch: (p: Partial<ImagegenDraft>) => void;
  /** Every READY fleet row (ADR 0082 P1). The picker below renders only when there is more
   *  than one — a single-engine deployment sees exactly what it always did. */
  fleetProviders: ImagegenProvider[];
  /** The RESOLVED row driving the pane — ImagegenView's resolveFleetProvider(fleetProviders,
   *  draft.providerId), the same "parent resolves, child just reads" shape `model` already
   *  follows one field down. */
  provider: ImagegenProvider | null;
  models: ImagegenModel[];
  loras: ImagegenLora[];
  model: ImagegenModel | null;
  samplers: string[];
  schedulers: string[];
  loraWeightMax: number;
  alwaysNegative: string;
  busy: boolean;
  /** The Agent's caps, already evaluated. Pressing anyway is a 429 the person cannot act on,
   *  so the button says why instead (lane A, deviation 3). */
  trialFull: boolean;
  queueFull: boolean;
  onTrial: () => void;
  onEnqueue: () => void;
  onPromptHelp: () => void;
}

export function GenerateForm({
  draft,
  patch,
  fleetProviders,
  provider,
  models,
  loras,
  model,
  samplers,
  schedulers,
  loraWeightMax,
  alwaysNegative,
  busy,
  trialFull,
  queueFull,
  onTrial,
  onEnqueue,
  onPromptHelp,
}: Props) {
  const tr = useT();
  const card = familyCard(model?.family);
  const family = model?.family || "";
  // Undeclared knobs = an Agent from before this ADR. Everything stays enabled; see the
  // header comment for why that is the safe direction.
  const declared = model?.knobs;
  const reads = (k: Knob): boolean => !declared || declared.includes(k);

  const sizes = useMemo(() => sizeOptions(model?.sizes, model?.family), [model]);
  // A LoRA only loads on a checkpoint of its own family; offering the rest would be a form
  // that produces a 400 the person cannot read.
  const usable = useMemo(
    () => loras.filter((l) => !family || !l.baseModel || l.baseModel === family),
    [loras, family],
  );
  const picked = useMemo(() => new Set(draft.loras.map((l) => l.name)), [draft.loras]);
  const byName = useMemo(() => new Map(usable.map((l) => [l.name, l] as const)), [usable]);
  // The chips follow the SELECTION, so removing a LoRA removes the words it brought and
  // nothing else — they are derived, never stored.
  const triggers = useMemo(
    () => [...new Set(usable.filter((l) => picked.has(l.name)).flatMap(loraTriggers).filter(Boolean))],
    [usable, picked],
  );

  const isEdit = draft.op !== "generate";
  const append = (text: string) => {
    const cur = draft.prompt.trimEnd();
    if (cur.split(/\s*,\s*/).includes(text)) return;
    patch({ prompt: cur ? `${cur}, ${text}` : text });
  };

  const toggleLora = (name: string) => {
    patch(
      picked.has(name)
        ? { loras: draft.loras.filter((l) => l.name !== name) }
        : // The starting weight is the catalogue row's when it declares one, else the Agent's
          // default of 1 — a row that publishes 0.6 means 0.6, and 1 is a different picture.
          { loras: [...draft.loras, { name, weight: loraWeight(byName.get(name)!) }] },
    );
  };

  const setWeight = (name: string, weight: number) => {
    patch({ loras: draft.loras.map((l) => (l.name === name ? { ...l, weight } : l)) });
  };

  // Ctrl+Enter and Ctrl+Shift+Enter are decision 11's two verbs. They are bound on the form
  // rather than the window: the pane may be one of six on screen, and a global binding
  // would fire the wrong studio's trial.
  const onKeyDown = (e: RKeyboardEvent) => {
    if (e.key !== "Enter" || !(e.ctrlKey || e.metaKey) || busy) return;
    e.preventDefault();
    if (e.shiftKey) {
      if (!queueFull) onEnqueue();
    } else if (!trialFull) onTrial();
  };

  const stepsPh = model?.params?.steps != null ? tr("imggen.default_ph", { v: model.params.steps }) : tr("imggen.default_ph_none");
  const cfgPh = model?.params?.cfg != null ? tr("imggen.default_ph", { v: model.params.cfg }) : tr("imggen.default_ph_none");

  // ADR 0082 unresolved question 2: shown only when there is a REAL choice — one fleet row is
  // not a choice (the same "more than one" rule the model/LoRA pickers already follow). `value`
  // reads the ALREADY-RESOLVED `provider` prop (ImagegenView's resolveFleetProvider) rather than
  // re-deriving it here, the same "parent resolves, child reads" split `model` already follows.
  const providerKindLabel = (kind?: string): string =>
    kind === "comfy" ? tr("agents.image_kind_comfy") : kind === "openai-compat" ? tr("agents.image_kind_openai_compat") : "";

  return (
    <div className="igen-form" onKeyDown={onKeyDown}>
      {fleetProviders.length > 1 && (
        <label className="igen-field">
          <span className="igen-label">{tr("imggen.provider")}</span>
          <select
            className="ds-select"
            value={provider?.id || ""}
            onChange={(e) => patch({ providerId: e.target.value })}
          >
            {fleetProviders.map((p) => {
              const kindLabel = providerKindLabel(p.kind);
              return (
                <option key={p.id} value={p.id}>
                  {kindLabel ? `${p.id} (${kindLabel})` : p.id}
                </option>
              );
            })}
          </select>
        </label>
      )}
      <label className="igen-field">
        <span className="igen-label">{tr("imggen.model")}</span>
        <select
          className="ds-select"
          value={draft.model}
          disabled={!models.length}
          onChange={(e) => patch({ model: e.target.value })}
        >
          <option value="">{models.length ? tr("imggen.model_none") : tr("imggen.no_models")}</option>
          {models.map((m) => (
            <option key={m.id} value={m.id}>
              {m.id}
              {m.warm ? " ●" : ""}
            </option>
          ))}
        </select>
      </label>

      {model && <FamilyCardBlock model={model} onQuality={append} />}

      <label className="igen-field">
        <span className="igen-label">{tr("imggen.prompt")}</span>
        <textarea
          className="igen-prompt"
          rows={5}
          value={draft.prompt}
          placeholder={tr("imggen.prompt_ph")}
          onChange={(e) => patch({ prompt: e.target.value })}
        />
      </label>
      {triggers.length > 0 && (
        <div className="igen-chips igen-triggers">
          <span className="igen-chips-cap">{tr("imggen.triggers")}</span>
          {triggers.map((w) => (
            <button
              key={w}
              type="button"
              className="igen-chip"
              title={tr("imggen.trigger_add", { word: w })}
              onClick={() => append(w)}
            >
              {w}
            </button>
          ))}
        </div>
      )}
      <div className="igen-row igen-help-row">
        <button type="button" className="ui-btn ui-btn-ghost" onClick={onPromptHelp}>
          <Icon name="sparkle" /> {tr("imggen.help_open")}
        </button>
      </div>

      <label className="igen-field">
        <span className="igen-label">{tr("imggen.negative")}</span>
        <textarea
          className="igen-negative"
          rows={2}
          value={draft.negative}
          disabled={!reads("negative")}
          placeholder={reads("negative") ? tr("imggen.negative_ph") : tr("imggen.knob_off", { family })}
          onChange={(e) => patch({ negative: e.target.value })}
        />
      </label>
      {(model?.negative || alwaysNegative) && reads("negative") && (
        <div className="igen-chips igen-fixed">
          {model?.negative && (
            <span className="igen-chip fixed" title={tr("imggen.negative_row")}>
              <Icon name="lock" /> {model.negative}
            </span>
          )}
          {alwaysNegative && (
            <span className="igen-chip fixed" title={tr("imggen.negative_always")}>
              <Icon name="lock" /> {alwaysNegative}
            </span>
          )}
        </div>
      )}

      <div className="igen-grid">
        <Knobbed label={tr("imggen.steps")} on={reads("steps")} family={family}>
          <input
            className="ds-input"
            type="number"
            min={1}
            max={150}
            value={draft.steps}
            disabled={!reads("steps")}
            placeholder={stepsPh}
            onChange={(e) => patch({ steps: e.target.value })}
          />
        </Knobbed>
        <Knobbed label={tr("imggen.cfg")} on={reads("cfg")} family={family}>
          <input
            className="ds-input"
            type="number"
            min={0}
            max={30}
            step={0.5}
            value={draft.cfg}
            disabled={!reads("cfg")}
            placeholder={cfgPh}
            onChange={(e) => patch({ cfg: e.target.value })}
          />
        </Knobbed>
        <Knobbed label={tr("imggen.sampler")} on={reads("sampler")} family={family}>
          {/* The options are the AGENT's allow-list: a name it does not know is refused with
              400, and the catalogue overlay's silent fallback does not apply to a member's
              typed value (decision 4). */}
          <select
            className="ds-select"
            value={draft.sampler}
            disabled={!reads("sampler")}
            onChange={(e) => patch({ sampler: e.target.value })}
          >
            <option value="">{tr("imggen.model_default")}</option>
            {samplers.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </Knobbed>
        <Knobbed label={tr("imggen.scheduler")} on={reads("scheduler")} family={family}>
          <select
            className="ds-select"
            value={draft.scheduler}
            disabled={!reads("scheduler")}
            onChange={(e) => patch({ scheduler: e.target.value })}
          >
            <option value="">{tr("imggen.model_default")}</option>
            {schedulers.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </Knobbed>
        <label className="igen-field">
          <span className="igen-label">{tr("imggen.size")}</span>
          <select
            className="ds-select"
            value={draft.size}
            // On an edit the input's dimensions win (decision 9). Shown disabled with the
            // reason, so the rule is read before the run rather than in a warning after it.
            disabled={isEdit}
            title={isEdit ? tr("imggen.size_from_input") : undefined}
            onChange={(e) => patch({ size: e.target.value })}
          >
            <option value="">{tr("imggen.model_default")}</option>
            {sizes.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          {isEdit && <span className="igen-hint">{tr("imggen.size_from_input")}</span>}
        </label>
        <label className="igen-field">
          <span className="igen-label">{tr("imggen.seed_policy")}</span>
          <select
            className="ds-select"
            value={draft.seedPolicy}
            onChange={(e) => patch({ seedPolicy: e.target.value as ImagegenDraft["seedPolicy"] })}
          >
            <option value="random">{tr("imggen.seed_random")}</option>
            <option value="fixed">{tr("imggen.seed_fixed")}</option>
            <option value="sequence">{tr("imggen.seed_sequence")}</option>
          </select>
        </label>
        {draft.seedPolicy !== "random" && (
          <label className="igen-field">
            <span className="igen-label">{tr("imggen.seed")}</span>
            <input
              className="ds-input"
              value={draft.seed}
              placeholder={tr("imggen.seed_ph")}
              onChange={(e) => patch({ seed: e.target.value })}
            />
          </label>
        )}
        <label className="igen-field">
          <span className="igen-label">{tr("imggen.jobs")}</span>
          <input
            className="ds-input"
            type="number"
            min={1}
            max={MAX_JOBS}
            value={draft.jobs}
            onChange={(e) => patch({ jobs: Math.max(1, Math.min(MAX_JOBS, Number(e.target.value) || 1)) })}
          />
          <span className="igen-hint">{tr("imggen.jobs_hint")}</span>
        </label>
      </div>

      <div className="igen-loras">
        <span className="igen-label">{tr("imggen.loras")}</span>
        {usable.length === 0 ? (
          <span className="igen-hint">{tr("imggen.lora_none")}</span>
        ) : (
          usable.map((l) => {
            const on = picked.has(l.name);
            const w = draft.loras.find((x) => x.name === l.name)?.weight ?? loraWeight(l);
            return (
              <div className="igen-lora" key={l.name}>
                <label className="igen-lora-pick">
                  <input type="checkbox" checked={on} onChange={() => toggleLora(l.name)} />
                  <span title={l.description}>{l.name}</span>
                </label>
                {on && (
                  <span className="igen-lora-weight" aria-label={tr("imggen.lora_weight", { name: l.name })}>
                    {/* The default is 1 and the ceiling is the Agent's `comfyMaxLoraWeight`,
                        reported once by the status: no column holds a per-LoRA default. */}
                    <Slider
                      value={w}
                      min={0}
                      max={loraWeightMax}
                      step={0.05}
                      onChange={(v) => setWeight(l.name, v)}
                      format={(v) => v.toFixed(2)}
                    />
                  </span>
                )}
              </div>
            );
          })
        )}
      </div>

      <details className="igen-advanced">
        <summary>{tr("imggen.advanced")}</summary>
        <div className="igen-grid">
          <label className="igen-field">
            <span className="igen-label">{tr("imggen.batch")}</span>
            <input
              className="ds-input"
              type="number"
              min={1}
              max={MAX_BATCH}
              value={draft.batchSize}
              onChange={(e) => patch({ batchSize: Math.max(1, Math.min(MAX_BATCH, Number(e.target.value) || 1)) })}
            />
            <span className="igen-hint">{tr("imggen.batch_hint")}</span>
          </label>
          <label className="igen-field">
            <span className="igen-label">{tr("imggen.op")}</span>
            <select className="ds-select" value={draft.op} onChange={(e) => patch({ op: e.target.value })}>
              {OPS.map((o) => (
                <option key={o} value={o}>
                  {tr(`imggen.op_${o}` as "imggen.op_generate")}
                </option>
              ))}
            </select>
          </label>
          <label className="igen-field">
            <span className="igen-label">{tr("imggen.out_dir")}</span>
            <input
              className="ds-input"
              value={draft.outDir}
              placeholder={tr("imggen.out_dir_ph")}
              onChange={(e) => patch({ outDir: e.target.value })}
            />
          </label>
          <label className="igen-field">
            <span className="igen-label">{tr("imggen.label")}</span>
            <input
              className="ds-input"
              value={draft.label}
              placeholder={tr("imggen.label_ph")}
              onChange={(e) => patch({ label: e.target.value })}
            />
          </label>
        </div>
        {isEdit && (
          <>
            <InputPicker paths={draft.inputs} onChange={(inputs) => patch({ inputs })} />
            <label className="igen-field">
              <span className="igen-label">{tr("imggen.strength")}</span>
              <Slider value={draft.strength} min={0} max={1} step={0.05} onChange={(v) => patch({ strength: v })} />
            </label>
          </>
        )}
      </details>

      <div className="igen-actions">
        <button
          type="button"
          className="ui-btn"
          disabled={busy || trialFull}
          title={
            trialFull
              ? tr("imggen.trial_full")
              : tr("imggen.trial_title") + (card ? ` (${tr("imggen.family_trial", { n: card.trialSteps })})` : "")
          }
          onClick={onTrial}
        >
          <Icon name="beaker" /> {tr("imggen.trial")}
        </button>
        <label className="igen-fullsteps" title={tr("imggen.trial_full_steps_hint")}>
          <input type="checkbox" checked={draft.fullSteps} onChange={(e) => patch({ fullSteps: e.target.checked })} />
          {tr("imggen.trial_full_steps")}
        </label>
        <button
          type="button"
          className="ui-btn ui-btn-primary"
          disabled={busy || queueFull}
          title={queueFull ? tr("imggen.queue_full") : tr("imggen.enqueue_title", { n: draft.jobs })}
          onClick={onEnqueue}
        >
          <Icon name="play" /> {tr("imggen.enqueue", { n: draft.jobs })}
        </button>
      </div>
    </div>
  );
}

/** A field that a family may not read: disabled, WITH the reason, never silently ignored. */
function Knobbed({
  label,
  on,
  family,
  children,
}: {
  label: string;
  on: boolean;
  family: string;
  children: React.ReactNode;
}) {
  const tr = useT();
  return (
    <label className={"igen-field" + (on ? "" : " off")}>
      <span className="igen-label">{label}</span>
      {children}
      {!on && <span className="igen-hint">{tr("imggen.knob_off", { family })}</span>}
    </label>
  );
}

/** Layer A's family card: dialect, quality chips, whether the negative reaches, the ranges
 *  (decision 7). Content only — the knobs it describes are still the Agent's word. */
function FamilyCardBlock({ model, onQuality }: { model: ImagegenModel; onQuality: (s: string) => void }) {
  const tr = useT();
  const card = familyCard(model.family);
  return (
    <details className="igen-family" open>
      <summary>
        {tr("imggen.family")}: {model.family || "—"}
      </summary>
      {model.description && <p className="igen-family-desc">{model.description}</p>}
      {!card ? (
        <p className="igen-hint">{tr("imggen.family_unknown")}</p>
      ) : (
        <>
          <p>{tr(card.dialect === "tags" ? "imggen.family_dialect_tags" : "imggen.family_dialect_sentences")}</p>
          <p className="igen-hint">
            {tr("imggen.family_steps", { lo: card.steps[0], hi: card.steps[1] })}
            {card.cfg && <> · {tr("imggen.family_cfg", { lo: card.cfg[0], hi: card.cfg[1] })}</>}
            {" · "}
            {tr("imggen.family_trial", { n: card.trialSteps })}
          </p>
          <p className="igen-hint">
            {tr(
              model.knobs && !model.knobs.includes("negative")
                ? "imggen.family_negative_no"
                : "imggen.family_negative_yes",
            )}
          </p>
          {card.quality.length > 0 && (
            <div className="igen-chips">
              <span className="igen-chips-cap">{tr("imggen.family_quality")}</span>
              {card.quality.map((q) => (
                <button key={q} type="button" className="igen-chip" onClick={() => onQuality(q)}>
                  {q}
                </button>
              ))}
            </div>
          )}
        </>
      )}
      {(model.license_name || model.source_url) && (
        <p className="igen-hint">
          {model.license_name && (
            <>
              {tr("imggen.license")}:{" "}
              {model.license_url ? (
                <a href={model.license_url} target="_blank" rel="noreferrer noopener">
                  {model.license_name}
                </a>
              ) : (
                model.license_name
              )}
            </>
          )}
          {model.source_url && (
            <>
              {model.license_name ? " · " : ""}
              <a href={model.source_url} target="_blank" rel="noreferrer noopener">
                {tr("imggen.source")}
              </a>
            </>
          )}
        </p>
      )}
    </details>
  );
}
