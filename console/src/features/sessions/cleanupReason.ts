// The "reason" of a cleanup candidate (掃除モーダルの理由列) is text WE generate for the
// user to read, so it travels from the Agent as a catalog key (`clean.reason.*`) and is
// rendered here — it follows settings.locale instead of being frozen to the Agent's
// source language (ADR 0033). Before that the Agent sent Japanese prose, which an English
// Console showed verbatim next to its own English labels.
//
// `reason` remains the source-language (ja, ADR 0016 §4) fallback: an Agent older than the
// keys sends only prose, and a key this Console does not know (version skew) must degrade
// to readable text rather than to an empty cell. The Agent keeps sending the prose too —
// list_cleanup_candidates relays the same JSON to the assistant, which has no catalog.
import { tMaybe } from "../../lib/i18n/index.ts";

export function cleanupReasonText(c: { reason_key?: string; reason: string }): string {
  return (c.reason_key ? tMaybe(c.reason_key) : undefined) ?? c.reason;
}
