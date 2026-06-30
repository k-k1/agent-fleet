import { useState } from "react";
import Icon from "./Icon.jsx";

// Collapsible left-pane section with a title, optional icon + header actions, and a
// body. Sections are stacked in the left pane (Sessions / Repos / Files) and stay
// independently open/closed.
export default function Section({ title, icon, actions, defaultOpen = true, children }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className="pane-section">
      <div className="pane-head">
        <button className="pane-toggle" onClick={() => setOpen((o) => !o)}>
          <span className="caret">{open ? "▾" : "▸"}</span>
          {icon && <Icon name={icon} className="pane-ic" />}
          {title}
        </button>
        <span className="pane-actions">{actions}</span>
      </div>
      {open && <div className="pane-body">{children}</div>}
    </section>
  );
}
