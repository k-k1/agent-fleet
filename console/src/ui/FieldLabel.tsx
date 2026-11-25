// FieldLabel — a form label with an optional ⓘ button that reveals the field's static
// explanation. The rule (docs/CONVENTIONS.md §13): background that is true whatever the
// field's value or the surrounding state goes behind ⓘ; text that reports the current state
// or why something is blocked stays inline next to the field.
//
// The panel is always in the DOM (hidden while closed) so the button's aria-describedby
// resolves and a screen reader announces the explanation with the button, even closed.
// The button is a real <button>, so Tab reaches it and Enter / Space toggles it; Escape
// closes an open panel without closing the surrounding modal.
import { useId, useState } from "react";
import { useEscLayer } from "../lib/escLayer.ts";
import type { ReactNode } from "react";
import { useT } from "../lib/i18n/index.ts";

interface FieldLabelProps {
  /** The label text. */
  children: ReactNode;
  /** The control's id, when the label labels an input. Omit for a group label. */
  htmlFor?: string;
  /** The static explanation. Omit and no ⓘ is drawn. */
  info?: ReactNode;
  className?: string;
  /** Drop the muted small `.ui-field-label` look: for a label that wraps a checkbox and its text. */
  plain?: boolean;
  /** "span" for a group heading (a fieldset legend) that labels no single control. */
  as?: "label" | "span";
  /** Controls shown at the end of the label row (a header's action buttons), right-aligned. */
  trailing?: ReactNode;
  /** Extra class on the label row (the flex row holding label, ⓘ and trailing controls). */
  rowClassName?: string;
}

export function FieldLabel({ children, htmlFor, info, className, plain, as, trailing, rowClassName }: FieldLabelProps) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const panelId = useId();
  // An open panel is the topmost Escape layer wherever focus has moved, so the first Escape
  // closes it and only the next one reaches the surrounding modal.
  useEscLayer(() => setOpen(false), open);
  const Tag = as ?? "label";
  const cls = [plain ? "" : "ui-field-label", className ?? ""].filter(Boolean).join(" ") || undefined;
  if (info == null || info === false) {
    return (
      <Tag className={cls} htmlFor={htmlFor}>
        {children}
      </Tag>
    );
  }
  return (
    <>
      <span className={"ui-field-labelrow" + (rowClassName ? " " + rowClassName : "")}>
        <Tag className={cls} htmlFor={htmlFor}>
          {children}
        </Tag>
        <button
          type="button"
          className="ui-field-info"
          aria-expanded={open}
          aria-controls={panelId}
          aria-describedby={panelId}
          aria-label={tr("common.field_info")}
          onClick={() => setOpen((v) => !v)}
        >
          <span aria-hidden="true">ⓘ</span>
        </button>
        {trailing != null && <span className="ui-field-label-trailing">{trailing}</span>}
      </span>
      <div id={panelId} className="ui-field-hint ui-field-info-panel" hidden={!open}>
        {info}
      </div>
    </>
  );
}
