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

// Slider: a labeled range input for a 0..1（既定）スカラー設定（音量など）。format で
// 値の見せ方（例: パーセント）を差し替えられる。onChange は連続的に呼ばれる（ドラッグ中も）。
export function Slider({
  value,
  min = 0,
  max = 1,
  step = 0.05,
  onChange,
  format = (v) => `${Math.round(v * 100)}%`,
}: {
  value: number;
  min?: number;
  max?: number;
  step?: number;
  onChange: (v: number) => void;
  format?: (v: number) => string;
}) {
  return (
    <div className="ds-slider">
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      />
      <span className="ds-slider-val">{format(value)}</span>
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
