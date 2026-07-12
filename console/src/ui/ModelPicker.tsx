// ModelPicker — the launch-time model choices for an agent kind, shared by
// LaunchModal and NewSessionModal (always inside a .ui-field, whose select styling
// applies). claude renders as segmented buttons (four fixed tiers); codex/opencode
// as a <select> — their catalogs are fetched live and unbounded in count and id
// length. Callers gate on caps.model and re-resolve the value when the kind
// changes (resolveModel), so this only renders and reports picks.
import { useModelOptions } from "../lib/agentModels.ts";
import type { ModelOption } from "../lib/agentModels.ts";

interface ModelPickerProps {
  kind: string;
  model: string;
  onChange: (model: string) => void;
}

export function ModelPicker({ kind, model, onChange }: ModelPickerProps) {
  const options = useModelOptions(kind);
  if (!options) return null;
  if (kind !== "claude") {
    // A stored last-used model can be missing from the fetched list (deprecated /
    // provider since disconnected, or the list hasn't loaded yet): keep it visible
    // as the selected option rather than showing 既定 while actually sending it.
    const opts: ModelOption[] = options.some(([v]) => v === model)
      ? options
      : [...options, [model, model]];
    return (
      <select value={model} onChange={(e) => onChange(e.target.value)}>
        {opts.map(([v, label]) => (
          <option key={v || "default"} value={v}>
            {label}
          </option>
        ))}
      </select>
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
