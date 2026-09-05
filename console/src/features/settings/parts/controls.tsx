import type { ReactNode } from "react";
import { IconButton } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// Row: the standard label + control row shared by every settings tab (was copy-pasted
// as a local `Row` in each — Display/Agents/Assistant/Tts/Tokens/Env/Notifications —
// which drifts). Left label (.ds-label) + right control.
export function Row({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}

// Shared settings-tab controls, so the segmented Choice and the on/off toggle are defined
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

// Slider: a labeled range input for a scalar setting, 0..1 by default (volume and the like).
// format replaces how the value is shown (a percentage, say). onChange fires continuously,
// including while dragging.
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

// Select: a native dropdown sharing Choice's [value, label] options, for lists too long
// for a segmented control (opencode's ~50 models, say). Values round-trip through the
// original option: String() is used for display only and onChange returns the original type.
export function Select({ value, options, onChange }: ChoiceProps) {
  return (
    <select
      className="ds-select"
      value={String(value)}
      onChange={(e) => {
        const hit = options.find(([v]) => String(v) === e.target.value);
        onChange(hit ? hit[0] : e.target.value);
      }}
    >
      {options.map(([v, label]) => (
        <option key={String(v)} value={String(v)}>
          {label as string}
        </option>
      ))}
    </select>
  );
}

// OrderList: reorders a priority list with up/down buttons. value is an id array in display
// order, and the caller passes a complete, already-normalised order. The row order is the
// ranking, starting at 1.
export function OrderList({
  value,
  labels,
  onChange,
}: {
  value: string[];
  labels: Record<string, string>;
  onChange: (v: string[]) => void;
}) {
  const tr = useT();
  const move = (i: number, d: number) => {
    const j = i + d;
    if (j < 0 || j >= value.length) return;
    const next = value.slice();
    [next[i], next[j]] = [next[j], next[i]];
    onChange(next);
  };
  return (
    <div className="ds-orderlist">
      {value.map((id, i) => (
        <div key={id} className="ds-orderlist-row">
          <span className="ds-orderlist-rank">{i + 1}</span>
          <span className="ds-orderlist-label">{labels[id] ?? id}</span>
          <IconButton icon="chevron-up" label={tr("common.move_up")} disabled={i === 0} onClick={() => move(i, -1)} />
          <IconButton
            icon="chevron-down"
            label={tr("common.move_down")}
            disabled={i === value.length - 1}
            onClick={() => move(i, 1)}
          />
        </div>
      ))}
    </div>
  );
}

// OnOff: an on/off toggle built on Choice (value may be undefined, which is off).
export function OnOff({ value, onChange }: { value?: boolean; onChange: (v: boolean) => void }) {
  const tr = useT();
  return (
    <Choice
      value={!!value}
      options={[
        [true, tr("common.on")],
        [false, tr("common.off")],
      ]}
      onChange={onChange}
    />
  );
}
