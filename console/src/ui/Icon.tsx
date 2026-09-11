import { BRAND_PREFIX, agentBrandClass } from "../lib/brandicons.ts";

// Icon — codicon wrapper (name → span.codicon). `spin` for loading spinners.
//
// A "brand:<key>" name renders a vendored brand SVG instead of a codicon glyph
// (lib/brandicons) — that is how the agent kinds show their own CLI's logo. The span keeps
// the .codicon class either way, so every rule that sizes, colors or spaces icons
// (".notification-mute .codicon { font-size: 13px }" and its ~40 siblings) applies to both
// without being restated, and callers keep passing a single `name`.
interface IconProps {
  name: string;
  spin?: boolean;
  className?: string;
  title?: string;
}

export function Icon({ name, spin, className, title }: IconProps) {
  const isBrand = name.startsWith(BRAND_PREFIX);
  // An unvendored brand key falls back to a neutral glyph rather than "codicon-brand:x",
  // which would render as an empty gap.
  const base = isBrand
    ? agentBrandClass(name.slice(BRAND_PREFIX.length)) ?? "codicon-circle-large-outline"
    : `codicon-${name}`;
  return (
    <span
      className={`codicon ${base}` + (spin ? " codicon-spin" : "") + (className ? " " + className : "")}
      title={title}
      aria-hidden={title ? undefined : "true"}
    />
  );
}
