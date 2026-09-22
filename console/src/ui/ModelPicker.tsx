// ModelPicker — the launch-time model choices for an agent kind, shared by
// LaunchModal and NewSessionModal (always inside a .ui-field, whose select styling
// applies). claude renders as segmented buttons (four fixed tiers); codex/opencode
// through ModelCombo — their catalogs are fetched live and unbounded in count and id
// length. Callers gate on caps.model and re-resolve the value when the kind
// changes (resolveModel), so this only renders and reports picks.
import { useEffect, useMemo } from "react";
import { useT } from "../lib/i18n/index.ts";
import { Icon } from "./Icon.tsx";
import { useModelOptions, useHiddenModel, useModelCatalogSettled, modelCatalogReason } from "../lib/agentModels.ts";
import { useEffortOptions } from "../lib/agentModels.ts";
import type { ModelOption } from "../lib/agentModels.ts";
import { ModelCombo } from "./ModelCombo.tsx";
import { refreshUIPrefs } from "../lib/settings.ts";

interface ModelPickerProps {
  kind: string;
  model: string;
  onChange: (model: string) => void;
}

export function ModelPicker({ kind, model, onChange }: ModelPickerProps) {
  const tr = useT();
  const options = useModelOptions(kind);
  // A long-lived phone tab may have been foregrounded the whole time another device
  // edited this server-backed catalog. Refresh when a Claude picker actually opens as
  // well as on App foreground, so both Settings and launch modals see the latest ids.
  useEffect(() => {
    if (kind === "claude") void refreshUIPrefs();
  }, [kind]);

  const hidden = useHiddenModel(kind, model);
  const settled = useModelCatalogSettled(kind);
  const dynamicOptions = useMemo(() => {
    if (!options || kind === "claude") return options;
    // A stored last-used model can be missing from the fetched list (deprecated /
    // provider since disconnected, or the list hasn't loaded yet): keep it in the
    // full catalog rather than showing "default" while actually sending it. EXCEPT when the
    // user excluded it in settings — "gone" and "hidden" are different things, and adding it
    // back would resurrect a model the user hid (launching it is refused by the Agent's own
    // guard anyway, so offering the choice would be a lie).
    //
    // An empty model is never rescued: for a kind with a Default entry "" already IS present
    // in options, and for a kind with none (requiresConcreteModel — lcpp) there is nothing to
    // rescue, only a not-yet-resolved selection about to be auto-picked
    // (useAutoConcreteModel) — appending ["", ""] here would flash a blank row in the combo.
    if (hidden || !model) return options;
    return options.some(([v]) => v === model) ? options : [...options, [model, model] as ModelOption];
  }, [kind, model, options, hidden]);

  if (!options) return null;
  if (kind !== "claude") {
    // A live catalog takes a moment to arrive (the Agent asks the CLI or its daemon), and
    // until it does the picker can only offer "default" — which is exactly what it shows when
    // the account really has nothing. Saying "loading" for that moment is the difference
    // between a picker worth waiting for and one that looks broken; it is also what keeps the
    // note below from flashing on every open (useModelCatalogSettled).
    const onlyDefault = settled && (dynamicOptions?.length ?? 0) <= 1;
    // What emptied it. "hidden" / "route" are what the Agent reported (agent_models.go's
    // emptyReason) and are facts about this workspace's own settings; "unreachable" is this
    // side's own — the request never landed, which is the ordinary state for the first
    // seconds after a workspace starts. Only "catalog_empty" keeps the old wording, because
    // only there can the Console not tell not-signed-in from provider-unreachable from a plan
    // with the default alone (Copilot Free). Pointing at the connection and the plan for any
    // of the other three sent people to check something that was never wrong.
    const reason = onlyDefault ? modelCatalogReason(kind) : "";
    const note =
      reason === "hidden"
        ? "ui.model_none_hidden"
        : reason === "route"
          ? "ui.model_none_route"
          : reason === "unreachable"
            ? "ui.model_unreachable"
            : "ui.model_default_only";
    return (
      <div className="model-picker-dynamic">
        <ModelCombo kind={kind} options={dynamicOptions ?? []} value={model} onChange={onChange} />
        {!settled && (
          <span className="ui-field-hint model-picker-loading">
            <Icon name="loading" spin /> {tr("ui.model_loading")}
          </span>
        )}
        {onlyDefault && <span className="ui-field-hint">{tr(note)}</span>}
      </div>
    );
  }
  // Claude Code OAuth has no account-aware model catalog to query. Keep the stable
  // tier aliases as the fast path and offer only full ids that the user deliberately
  // registered in Agent settings.
  const aliases = options.filter(([v]) => ["fable", "opus", "sonnet", "haiku"].includes(v));
  const registered = options.filter(([v]) => !["fable", "opus", "sonnet", "haiku"].includes(v));
  const registeredSelected = registered.some(([v]) => v === model);
  return (
    <div className="model-picker-claude">
      <div className="ui-seg">
        {aliases.map(([v, label]) => (
          <button
            key={v || "default"}
            type="button"
            className={"seg-btn" + (model === v ? " active" : "")}
            onClick={() => onChange(v)}
          >
            {label}
          </button>
        ))}
      </div>
      {registered.length > 0 && (
        <select
          value={registeredSelected ? model : ""}
          onChange={(e) => e.target.value && onChange(e.target.value)}
          aria-label={tr("ui.claude_registered_model")}
        >
          <option value="">{tr("ui.claude_registered_model")}</option>
          {registered.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
        </select>
      )}
    </div>
  );
}

interface EffortPickerProps {
  kind: string;
  model: string;
  effort: string;
  onChange: (effort: string) => void;
}

export function EffortPicker({ kind, model, effort, onChange }: EffortPickerProps) {
  const options = useEffortOptions(kind, model);
  const opts = options.some(([v]) => v === effort) ? options : [...options, [effort, effort] as [string, string]];
  return (
    <select value={effort} onChange={(e) => onChange(e.target.value)}>
      {opts.map(([v, label]) => (
        <option key={v || "default"} value={v}>
          {label}
        </option>
      ))}
    </select>
  );
}
