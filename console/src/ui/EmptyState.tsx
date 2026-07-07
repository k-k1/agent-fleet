// EmptyState — shared empty/loading/placeholder block (port of the old
// components/EmptyState seed): centered icon + title + optional hint/actions.
import type { ReactNode } from "react";
import { cx } from "./cx.ts";

export interface EmptyStateProps {
  /** codicon name (without the codicon- prefix). */
  icon?: string;
  title: ReactNode;
  hint?: ReactNode;
  /** Optional action row (Buttons) under the hint. */
  children?: ReactNode;
  className?: string;
}

export function EmptyState({ icon, title, hint, children, className }: EmptyStateProps) {
  return (
    <div className={cx("ui-empty", className)}>
      {icon && <span className={`codicon codicon-${icon} ui-empty-icon`} aria-hidden="true" />}
      <div className="ui-empty-title">{title}</div>
      {hint && <div className="ui-empty-hint">{hint}</div>}
      {children && <div className="ui-empty-actions">{children}</div>}
    </div>
  );
}
