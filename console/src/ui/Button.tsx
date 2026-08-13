// Button / IconButton — the next console's only button primitives.
//
// Every actionable control goes through these (docs/22 §ui/): consistent
// icon+label composition, variants for tone, no ad-hoc <button> styling in
// features. Icons are codicon names (the icon font ships with the app).
import type { ButtonHTMLAttributes } from "react";
import { cx } from "./cx.ts";

export type ButtonVariant = "default" | "primary" | "ghost" | "danger";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  /** codicon name (without the codicon- prefix), rendered before the label. */
  icon?: string;
  /** Compact paddings for dense rows (section headers, list actions). */
  small?: boolean;
}

export function Button({ variant = "default", icon, small, className, children, ...rest }: ButtonProps) {
  return (
    <button type="button" className={cx("ui-btn", `ui-btn-${variant}`, small && "ui-btn-sm", className)} {...rest}>
      {icon && <span className={`codicon codicon-${icon}`} aria-hidden="true" />}
      {children}
    </button>
  );
}

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** codicon name (without the codicon- prefix). */
  icon: string;
  /** Accessible name; also the tooltip. Icon-only buttons MUST be labeled. */
  label: string;
  /** Spin the icon (in-flight action), same modifier Icon uses. */
  spin?: boolean;
  variant?: ButtonVariant;
}

export function IconButton({ icon, label, variant = "ghost", spin, className, ...rest }: IconButtonProps) {
  return (
    <button
      type="button"
      className={cx("ui-btn", `ui-btn-${variant}`, "ui-iconbtn", className)}
      aria-label={label}
      title={label}
      {...rest}
    >
      <span className={`codicon codicon-${icon}` + (spin ? " codicon-spin" : "")} aria-hidden="true" />
    </button>
  );
}
