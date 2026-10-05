import type { ReactNode } from "react";
import { useState } from "react";

// The two pieces every connection card's settings area is built from — the agent cards'
// behaviour settings and the chat cards' notification settings render exactly this markup, so
// it lives once here rather than as two copies that drift.

// SettingRow: a labeled row inside a card's settings area (.ps-row).
export function SettingRow({ label, sub, children }: { label: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="ps-row">
      <span className="ps-label">
        {label}
        {sub && <span className="sub">{sub}</span>}
      </span>
      {children}
    </div>
  );
}

// SettingsDisclosure: the collapsed-by-default settings area under a card, so the card reads
// as "connect" first with the detail a deliberate second level.
export function SettingsDisclosure({ label, children }: { label: ReactNode; children?: ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <div className={"p-settings" + (open ? " open" : "")}>
      <button type="button" className="ps-disclosure" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="ps-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        {label}
      </button>
      {open && <div className="ps-body">{children}</div>}
    </div>
  );
}
