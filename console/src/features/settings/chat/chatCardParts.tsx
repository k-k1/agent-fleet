import type { ReactNode } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { SettingRow, SettingsDisclosure } from "../parts/cardSettings.tsx";

// The toggleable notification event groups (mirror the backend's bridge.EventKeys).
export const CHAT_EVENTS: [string, string][] = [
  ["answer-ready", "ops.ev_answer_ready"],
  ["question", "ops.ev_question"],
  ["permission-request", "ops.ev_permission"],
  ["exit", "ops.ev_exit"],
  ["session-report", "ops.ev_report"],
];
export const ALL_EVENTS = CHAT_EVENTS.map(([k]) => k);

// SettingsPanel — the notification-settings disclosure shown on a connected card.
export function SettingsPanel({ children }: { children?: ReactNode }) {
  const tr = useT();
  return <SettingsDisclosure label={tr("chat.settings")}>{children}</SettingsDisclosure>;
}

export { SettingRow as PsRow };
