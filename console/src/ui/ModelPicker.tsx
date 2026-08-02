// ModelPicker — the launch-time model choices for an agent kind, shared by
// LaunchModal and NewSessionModal (always inside a .ui-field, whose select styling
// applies). claude renders as segmented buttons (four fixed tiers); codex/opencode
// as a <select> — their catalogs are fetched live and unbounded in count and id
// length. Callers gate on caps.model and re-resolve the value when the kind
// changes (resolveModel), so this only renders and reports picks.
import { useEffect, useMemo, useState } from "react";
import { useT } from "../lib/i18n/index.ts";
import { useModelOptions, useHiddenModel } from "../lib/agentModels.ts";
import { useEffortOptions } from "../lib/agentModels.ts";
import type { ModelOption } from "../lib/agentModels.ts";
import { filterModelOptions } from "../lib/modelFilter.ts";

interface ModelPickerProps {
  kind: string;
  model: string;
  onChange: (model: string) => void;
}

export function ModelPicker({ kind, model, onChange }: ModelPickerProps) {
  const tr = useT();
  const options = useModelOptions(kind);
  const [query, setQuery] = useState("");
  useEffect(() => setQuery(""), [kind]);

  const hidden = useHiddenModel(kind, model);
  const dynamicOptions = useMemo(() => {
    if (!options || kind === "claude") return options;
    // A stored last-used model can be missing from the fetched list (deprecated /
    // provider since disconnected, or the list hasn't loaded yet): keep it in the
    // full catalog rather than showing 既定 while actually sending it. EXCEPT when the
    // user excluded it in settings — 消えた と 隠した は別物で、足し戻すと隠したはずの
    // モデルが復活する（起動は Agent 側ガードで断られるので、選べても嘘になる）。
    if (hidden) return options;
    return options.some(([v]) => v === model) ? options : [...options, [model, model] as ModelOption];
  }, [kind, model, options, hidden]);
  const filtered = useMemo(
    () => (dynamicOptions ? filterModelOptions(dynamicOptions, query) : []),
    [dynamicOptions, query],
  );

  if (!options) return null;
  if (kind !== "claude") {
    const selectedVisible = filtered.some(([v]) => v === model);
    const selectValue = selectedVisible ? model : "__filtered_selection__";
    return (
      <div className="model-picker-dynamic">
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={tr("ui.filter_models")}
          aria-label={tr("ui.filter_kind_models", { kind })}
        />
        <select
          value={selectValue}
          disabled={filtered.length === 0}
          onChange={(e) => onChange(e.target.value)}
          aria-label={tr("ui.kind_model", { kind })}
        >
          {!selectedVisible && (
            <option value="__filtered_selection__" disabled>
              {filtered.length ? tr("ui.select_from_count", { count: filtered.length }) : tr("ui.no_matching_models")}
            </option>
          )}
          {filtered.map(([v, label]) => (
            <option key={v || "default"} value={v}>
              {label}
            </option>
          ))}
        </select>
        {query.trim() && <span className="ui-field-hint">{tr("ui.count_items", { count: filtered.length })}</span>}
      </div>
    );
  }
  // Claude Code has no account-aware model catalog to query. Keep the stable tier
  // aliases as the fast path, but also accept its documented full model names so a
  // user can pin an older release (for example claude-opus-4-8). The provider remains
  // the authority on whether that release is still available to this account.
  const aliasSelected = options.some(([v]) => v === model);
  return (
    <div className="model-picker-claude">
      <div className="ui-seg">
        {options.map(([v, label]) => (
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
      <input
        type="text"
        value={aliasSelected ? "" : model}
        onChange={(e) => onChange(e.target.value.trimStart())}
        onBlur={(e) => onChange(e.target.value.trim())}
        placeholder={tr("ui.claude_full_model_placeholder")}
        aria-label={tr("ui.claude_full_model")}
        spellCheck={false}
        autoCapitalize="none"
        autoCorrect="off"
      />
      <span className="ui-field-hint">{tr("ui.claude_full_model_hint")}</span>
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
