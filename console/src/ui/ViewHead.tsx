// ViewHead — the header band a pane view renders above its content.
//
// Every pane view (terminal, mirror, scm, file, diff, doc, reader, chat) draws
// the same band: a title on the left, freeform chips/controls in the middle, an
// action cluster pinned right. It used to be re-declared per feature, which let
// the two `.view-head` rules in scm.css and mirror.css silently merge by import
// order. The band now has one owner (ui.css) and one component.
//
// Two things the band must clear are overlaid by the Pane, not laid out here:
// the drag grip (top-left) and the pop-out/wrap/close cluster (top-right). The
// padding in ui.css reserves that room, sized from --pane-ctl-w.
import type { ReactNode } from "react";
import { cx } from "./cx.ts";

interface ViewHeadProps {
  /** Right-aligned cluster (toggles/buttons). Omit when the view has none. */
  actions?: ReactNode;
  /** Per-view tuning: `fileinfo` (wrapping variant), `reader-head`, … */
  className?: string;
  /** Title first, then any chips/tags/controls the view puts inline. */
  children?: ReactNode;
}

export function ViewHead({ actions, className, children }: ViewHeadProps) {
  return (
    <header className={cx("view-head", className)}>
      {children}
      {actions != null && <span className="view-head-actions">{actions}</span>}
    </header>
  );
}
