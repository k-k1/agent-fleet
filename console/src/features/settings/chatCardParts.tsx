import type { ReactNode } from "react";
import { useState } from "react";
import { useT } from "../../lib/i18n/index.ts";

// The toggleable notification event groups (mirror the backend's bridge.EventKeys).
export const CHAT_EVENTS: [string, string][] = [
  ["answer-ready", "ops.ev_answer_ready"],
  ["question", "ops.ev_question"],
  ["permission-request", "ops.ev_permission"],
  ["exit", "ops.ev_exit"],
  ["session-report", "ops.ev_report"],
];
export const ALL_EVENTS = CHAT_EVENTS.map(([k]) => k);

// SettingsPanel — the collapsible 通知設定 disclosure shown on a connected card, mirroring
// the agent 動作設定 (P2 CardSettings): collapsed by default, so the card reads "connect"
// first with the detail settings a deliberate second level.
export function SettingsPanel({ children }: { children?: ReactNode }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className={"p-settings" + (open ? " open" : "")}>
      <button type="button" className="ps-disclosure" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="ps-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        {tr("chat.settings")}
      </button>
      {open && <div className="ps-body">{children}</div>}
    </div>
  );
}

// A labeled settings row (connection-card style: .ps-row).
export function PsRow({ label, sub, children }: { label: ReactNode; sub?: ReactNode; children?: ReactNode }) {
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
