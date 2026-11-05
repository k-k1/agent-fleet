// The preset chooser that leads every from/to range bar: This month / Last month / Last 30 days /
// Custom. One component so the panels keep the same control order. Choosing a preset hands the
// range to the caller, which fills its fields and applies; "Custom" is just the state of typed
// dates and picks nothing.
import { useEffect, useState } from "react";
import { useT } from "../lib/i18n/index.ts";
import { matchPreset, presetRange, RANGE_PRESETS, todayKey } from "../lib/rangePresets.ts";
import type { RangePresetId, RangeZone } from "../lib/rangePresets.ts";

export function RangePresets({
  from,
  to,
  zone,
  onPick,
  disabled,
  now,
}: {
  from: string;
  to: string;
  zone: RangeZone;
  onPick: (range: { from: string; to: string }) => void;
  disabled?: boolean;
  /** Injected by tests. Without it the clock is read at the moment of use, never captured at
   *  render: a screen left open across midnight or a month end must pick the new day. */
  now?: Date;
}) {
  const tr = useT();
  // A screen left open across midnight keeps showing "This month" for last month's range, and
  // choosing the shown item again fires no change. Watching the date key re-renders at the
  // boundary, where the stale range stops matching and the chooser reads "Custom" so the preset
  // can be chosen again. Skipped when a test injects the clock.
  const [, setDay] = useState(() => todayKey(new Date(), zone));
  useEffect(() => {
    if (now) return;
    const check = () => setDay(todayKey(new Date(), zone));
    const id = setInterval(check, 30_000);
    window.addEventListener("focus", check);
    document.addEventListener("visibilitychange", check);
    return () => {
      clearInterval(id);
      window.removeEventListener("focus", check);
      document.removeEventListener("visibilitychange", check);
    };
  }, [now, zone]);
  const current = matchPreset(from, to, now ?? new Date(), zone);
  return (
    <label className="range-presets">
      {tr("admin.preset_label")}
      <select
        value={current}
        disabled={disabled}
        onChange={(e) => {
          const id = e.target.value as RangePresetId | "custom";
          if (id !== "custom") onPick(presetRange(id, now ?? new Date(), zone));
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
