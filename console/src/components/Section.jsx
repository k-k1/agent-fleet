import { useState } from "react";

// Collapsible left-pane section with a title, optional header actions, and a body.
// Sections are stacked in the left pane (Sessions / Repos / Files) and stay
// independently open/closed.
export default function Section({ title, actions, defaultOpen = true, children }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className="pane-section">
      <div className="pane-head">
        <button className="pane-toggle" onClick={() => setOpen((o) => !o)}>
          <span className="caret">{open ? "▾" : "▸"}</span>
          {title}
        </button>
        <span className="pane-actions">{actions}</span>
      </div>
      {open && <div className="pane-body">{children}</div>}
    </section>
  );
}
