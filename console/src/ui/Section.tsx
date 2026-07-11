// Section — collapsible left-rail section with a title, optional icon + header
// actions, and a body. `id` keys the open/closed state in localStorage (same
// af-section-<id> keys as the old console, so collapse choices carry over).
// `count` shows a muted tally — the only signal of contents while collapsed.
import { useState } from "react";
import type { ReactNode } from "react";
import { Icon } from "./Icon.tsx";

interface SectionProps {
  id?: string;
  title?: ReactNode;
  icon?: string;
  count?: number;
  actions?: ReactNode;
  defaultOpen?: boolean;
  /** Controlled mode: pass `open` (+ `onToggle`) and the section renders that
   * state instead of owning it — for callers that must open programmatically
   * (e.g. the Files section on a reveal). Persistence is the caller's job then. */
  open?: boolean;
  onToggle?: () => void;
  children?: ReactNode;
}

const storeKey = (id: string) => `af-section-${id}`;

export function Section({ id, title, icon, count, actions, defaultOpen = true, open: openProp, onToggle, children }: SectionProps) {
  const [openState, setOpen] = useState(() => {
    if (!id) return defaultOpen;
    const v = localStorage.getItem(storeKey(id));
    return v === null ? defaultOpen : v === "1";
  });
  const controlled = openProp !== undefined;
  const open = controlled ? openProp : openState;
  const toggle = () => {
    if (controlled) {
      onToggle?.();
      return;
    }
    setOpen((o) => {
      const next = !o;
      if (id) localStorage.setItem(storeKey(id), next ? "1" : "0");
      return next;
    });
  };
  return (
    <section className={"ui-section" + (open ? "" : " collapsed")}>
      <div className="ui-section-head">
        <button type="button" className="ui-section-toggle" onClick={toggle} aria-expanded={open}>
          <span className="ui-section-caret">{open ? "▾" : "▸"}</span>
          {icon && <Icon name={icon} />}
          <span className="ui-section-title">{title}</span>
          {count != null && count > 0 && <span className="ui-section-count">{count}</span>}
        </button>
        <span className="ui-section-actions">{actions}</span>
      </div>
      {open && <div className="ui-section-body">{children}</div>}
    </section>
  );
}
