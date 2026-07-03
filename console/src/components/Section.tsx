import { useState } from "react";
import type { ReactNode } from "react";
import Icon from "./Icon.jsx";

// Collapsible left-pane section with a title, optional icon + header actions, and a
// body. Sections are stacked in the left pane (Sessions / Repos / Files) and stay
// independently open/closed.
//
// `id` keys the open/closed state in localStorage so a section the user collapsed
// stays collapsed across reloads (the left pane is long; folding away what you
// don't use shouldn't reset every time). `count` shows a muted tally next to the
// title — useful at a glance, and the only signal of contents while collapsed.
interface SectionProps {
  id?: string;
  title?: ReactNode;
  icon?: string;
  count?: number;
  actions?: ReactNode;
  defaultOpen?: boolean;
  children?: ReactNode;
}

const storeKey = (id: string) => `af-section-${id}`;

export default function Section({ id, title, icon, count, actions, defaultOpen = true, children }: SectionProps) {
  const [open, setOpen] = useState(() => {
    if (!id) return defaultOpen;
    const v = localStorage.getItem(storeKey(id));
    return v === null ? defaultOpen : v === "1";
  });
  const toggle = () =>
    setOpen((o) => {
      const next = !o;
      if (id) localStorage.setItem(storeKey(id), next ? "1" : "0");
      return next;
    });
  return (
    <section className={"pane-section" + (open ? "" : " collapsed")}>
      <div className="pane-head">
        <button className="pane-toggle" onClick={toggle} aria-expanded={open}>
          <span className="caret">{open ? "▾" : "▸"}</span>
          {icon && <Icon name={icon} className="pane-ic" />}
          {title}
          {count != null && count > 0 && <span className="pane-count">{count}</span>}
        </button>
        <span className="pane-actions">{actions}</span>
      </div>
      {open && <div className="pane-body">{children}</div>}
    </section>
  );
}
