import type { ReactNode } from "react";

// Shared settings-tab controls, so the segmented Choice / オン・オフ toggle is defined
// once instead of copied between DisplayTab and AgentsTab (they drift otherwise).
export interface ChoiceProps {
  value: unknown;
  options: unknown[][];
  onChange: (v: any) => void;
}

// Choice: a small horizontal segmented control.
export function Choice({ value, options, onChange }: ChoiceProps) {
  return (
    <div className="seg choice-seg">
      {options.map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          className={"seg-btn" + (v === value ? " active" : "")}
          onClick={() => onChange(v)}
        >
          {label as ReactNode}
        </button>
      ))}
    </div>
  );
}

// OnOff: an オン / オフ toggle built on Choice (value may be undefined = オフ).
export function OnOff({ value, onChange }: { value?: boolean; onChange: (v: boolean) => void }) {
  return (
    <Choice
      value={!!value}
      options={[
        [true, "オン"],
        [false, "オフ"],
      ]}
      onChange={onChange}
    />
  );
}
