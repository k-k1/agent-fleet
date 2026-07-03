import { SURFACE_COLORS, surfaceValue } from "../lib/settings.js";

// SwatchGrid: the surface-color picker grid. Each swatch previews the color as it'll
// look in the active theme; "デフォルト" (no color) shows a slashed neutral chip; the
// selected one shows a check. Shared by the 外観 popover (TopBar) and the 表示 settings
// tab (DisplayTab) so the two stay in lockstep.
export default function SwatchGrid({
  theme,
  value,
  onChange,
}: {
  theme: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="swatch-row">
      {SURFACE_COLORS.map((c) => {
        const col = surfaceValue(c.id, theme);
        return (
          <button
            key={c.id}
            type="button"
            title={c.label}
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
