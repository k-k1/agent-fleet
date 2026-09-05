// The "reason" of a cleanup candidate (the cleanup modal's reason column) is text WE generate
// for the user to read, so it travels from the Agent as a catalog key (`clean.reason.*`) and is
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

// The reason rendered as a STATE BADGE plus a supporting hint sentence — the row's
// second line. Split per key in the catalogs (clean.reason_badge.* / clean.reason_hint.*)
// because a whole reason sentence squeezed into one pill wraps into a mess, while the
// state alone ("stopped", "merged", "uncommitted/unpushed") is what the eye scans for.
// A key without a badge entry (version skew: an Agent newer than this Console) degrades
// to the full sentence, same as cleanupReasonText.
export interface CleanupReasonParts {
  badge?: string;
  text: string;
}

export function cleanupReasonParts(c: { reason_key?: string; reason: string }): CleanupReasonParts {
  const key = c.reason_key;
  if (key?.startsWith("clean.reason.")) {
    const suffix = key.slice("clean.reason.".length);
    const badge = tMaybe("clean.reason_badge." + suffix);
    if (badge) return { badge, text: tMaybe("clean.reason_hint." + suffix) ?? "" };
  }
  return { text: cleanupReasonText(c) };
}
