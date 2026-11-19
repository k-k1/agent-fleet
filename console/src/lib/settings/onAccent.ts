// Text colour for a user-chosen accent fill. The accent palette is theme-independent, so the
// foreground the theme tokens supply (--on-accent: white in light, dark in dark) cannot be right
// for every pick: white on the green/warm/teal accents is 2.4–2.6:1 in the light theme.
// The pick is therefore made from the fill itself, by WCAG contrast ratio, never by theme.

export const ON_ACCENT_LIGHT = "#ffffff";
// Darker than the dark theme's --on-accent (#06222e): with that one, the blue and rose accents
// sit at 4.47/4.52:1 and neither candidate would clear 4.5:1 for blue.
export const ON_ACCENT_DARK = "#05070a";

function channel(v: number): number {
  const c = v / 255;
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

// Parse #rgb / #rrggbb only. Anything else (hsl(), rgb(), named colours, "transparent", empty,
// wrong length) is "unknown" rather than guessed at: the caller then leaves the variable unset
// and the CSS falls back to the theme's own --on-accent.
function parseHex(input: string): [number, number, number] | null {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(input.trim());
  if (!m) return null;
  let h = m[1];
  if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as [number, number, number];
}

export function relativeLuminance(hex: string): number | null {
  const rgb = parseHex(hex);
  if (!rgb) return null;
  return 0.2126 * channel(rgb[0]) + 0.7152 * channel(rgb[1]) + 0.0722 * channel(rgb[2]);
}

export function contrastRatio(a: string, b: string): number | null {
  const x = relativeLuminance(a);
  const y = relativeLuminance(b);
  if (x === null || y === null) return null;
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

// Hover fill: nudge the fill away from the text colour (toward white under dark text, toward
// black under white text), so hovering never lowers the contrast the on-colour was picked for.
// Mixing toward the theme's --fg did the opposite for dark text in the light theme.
export function hoverFillColor(fill: string | null | undefined): string | null {
  const on = onAccentColor(fill);
  if (!fill || !on) return null;
  return `color-mix(in srgb, ${fill.trim()} 85%, ${on === ON_ACCENT_DARK ? "#ffffff" : "#000000"})`;
}

// The foreground with the higher contrast against `fill`; null when `fill` is not a hex colour
// (or is null — no colour chosen). A tie goes to the dark text.
export function onAccentColor(fill: string | null | undefined): string | null {
  if (!fill) return null;
  const light = contrastRatio(fill, ON_ACCENT_LIGHT);
  const dark = contrastRatio(fill, ON_ACCENT_DARK);
  if (light === null || dark === null) return null;
  return light > dark ? ON_ACCENT_LIGHT : ON_ACCENT_DARK;
}
