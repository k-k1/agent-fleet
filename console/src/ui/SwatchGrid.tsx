import { SURFACE_COLORS, surfaceValue } from "../lib/settings.ts";
import { useT } from "../lib/i18n/index.ts";

// SwatchGrid: the surface-color picker grid. Each swatch previews the color as it'll
// look in the active theme; the default entry (no color) shows a slashed neutral chip; the
// selected one shows a check. Shared by the appearance popover (TopBar) and the Display
// settings tab (DisplayTab, P7) so the two stay in lockstep.
export function SwatchGrid({
  theme,
  value,
  onChange,
}: {
  theme: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const tr = useT();
  return (
    <div className="swatch-row">
      {SURFACE_COLORS.map((c) => {
        const col = surfaceValue(c.id, theme);
        return (
          <button
            key={c.id}
            type="button"
            title={tr(c.labelKey)}
            className={"swatch" + (c.id === value ? " active" : "") + (col ? "" : " swatch-default")}
            style={col ? { background: col } : undefined}
            onClick={() => onChange(c.id)}
          >
            {c.id === value ? "✓" : ""}
          </button>
        );
      })}
    </div>
  );
}
