// ModelPicker — the launch-time model choices for an agent kind, shared by
// LaunchModal and NewSessionModal (always inside a .ui-field, whose select styling
// applies). claude renders as segmented buttons (four fixed tiers); codex/opencode
// as a <select> — their catalogs are fetched live and unbounded in count and id
// length. Callers gate on caps.model and re-resolve the value when the kind
// changes (resolveModel), so this only renders and reports picks.
import { useEffect, useMemo, useState } from "react";
import { useModelOptions } from "../lib/agentModels.ts";
import { useEffortOptions } from "../lib/agentModels.ts";
import type { ModelOption } from "../lib/agentModels.ts";
import { filterModelOptions } from "../lib/modelFilter.ts";

interface ModelPickerProps {
  kind: string;
  model: string;
  onChange: (model: string) => void;
}

export function ModelPicker({ kind, model, onChange }: ModelPickerProps) {
  const options = useModelOptions(kind);
  const [query, setQuery] = useState("");
  useEffect(() => setQuery(""), [kind]);

  const dynamicOptions = useMemo(() => {
    if (!options || kind === "claude") return options;
    // A stored last-used model can be missing from the fetched list (deprecated /
    // provider since disconnected, or the list hasn't loaded yet): keep it in the
    // full catalog rather than showing 既定 while actually sending it.
    return options.some(([v]) => v === model) ? options : [...options, [model, model] as ModelOption];
  }, [kind, model, options]);
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
          placeholder="モデルを絞り込み…"
          aria-label={`${kind} のモデルを絞り込み`}
        />
        <select
          value={selectValue}
          disabled={filtered.length === 0}
          onChange={(e) => onChange(e.target.value)}
          aria-label={`${kind} のモデル`}
        >
          {!selectedVisible && (
            <option value="__filtered_selection__" disabled>
              {filtered.length ? `${filtered.length} 件から選択` : "一致するモデルなし"}
            </option>
          )}
          {filtered.map(([v, label]) => (
            <option key={v || "default"} value={v}>
              {label}
            </option>
          ))}
        </select>
        {query.trim() && <span className="ui-field-hint">{filtered.length} 件</span>}
      </div>
    );
  }
  return (
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
