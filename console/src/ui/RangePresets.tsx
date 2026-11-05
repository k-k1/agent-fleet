// The preset chooser that leads every from/to range bar: This month / Last month / Last 30 days /
// Custom. One component so the panels keep the same control order. Choosing a preset hands the
// range to the caller, which fills its fields and applies; "Custom" is just the state of typed
// dates and picks nothing.
import { useT } from "../lib/i18n/index.ts";
import { matchPreset, presetRange, RANGE_PRESETS } from "../lib/rangePresets.ts";
import type { RangePresetId, RangeZone } from "../lib/rangePresets.ts";

export function RangePresets({
  from,
  to,
  zone,
  onPick,
  disabled,
  now = new Date(),
}: {
  from: string;
  to: string;
  zone: RangeZone;
  onPick: (range: { from: string; to: string }) => void;
  disabled?: boolean;
  /** Injected by tests; the clock is never read anywhere else. */
  now?: Date;
}) {
  const tr = useT();
  const current = matchPreset(from, to, now, zone);
  return (
    <label className="range-presets">
      {tr("admin.preset_label")}
      <select
        value={current}
        disabled={disabled}
        onChange={(e) => {
          const id = e.target.value as RangePresetId | "custom";
          if (id !== "custom") onPick(presetRange(id, now, zone));
        }}
      >
        {RANGE_PRESETS.map((id) => (
          <option key={id} value={id}>
            {tr(`admin.preset_${id}` as const)}
          </option>
        ))}
        <option value="custom">{tr("admin.preset_custom")}</option>
      </select>
      <span className="muted range-presets-note">
        {zone === "utc" ? tr("admin.preset_note_utc") : tr("admin.preset_note_local")}
      </span>
    </label>
  );
}
