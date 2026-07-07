// Pill — small state chip (session/workspace state, counts, badges).
// Tones map to the token palette; "muted" is the quiet default for inactive states.
import type { HTMLAttributes } from "react";
import { cx } from "./cx.ts";

export type PillTone = "ok" | "warn" | "danger" | "accent" | "muted";

export interface PillProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: PillTone;
  /** codicon name shown before the text (e.g. a ● state dot alternative). */
  icon?: string;
}

export function Pill({ tone = "muted", icon, className, children, ...rest }: PillProps) {
  return (
    <span className={cx("ui-pill", `ui-pill-${tone}`, className)} {...rest}>
      {icon && <span className={`codicon codicon-${icon}`} aria-hidden="true" />}
      {children}
    </span>
  );
}
