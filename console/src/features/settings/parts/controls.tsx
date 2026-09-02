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

// Select: a native dropdown sharing Choice's [value, label] options, for lists too long
// for a segmented control (e.g. opencode の ~50 モデル). Values round-trip through the
// original option (String() のみ表示用に使い、onChange は元の型の値を返す)。
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

// OrderList: 優先順位の並べ替え（上下ボタン式）。value は表示順そのままの id 配列で、
// 呼び出し側が正規化済みの完全な順序を渡す。行の並びが 1 位から順位を表す。
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

// OnOff: an オン / オフ toggle built on Choice (value may be undefined = オフ).
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
