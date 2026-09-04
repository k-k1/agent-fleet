// Session-report card bodies (role==="report") are rendered from the catalog, not from
// the text the backend stored — the same treatment notices got in ADR 0033, extended to
// reports by docs/log/28 P6.
//
// A report used to be ONE string with two readers: the user reading the card, and the
// operator assistant receiving the same string as a prompt. That is why it was excluded
// from i18n — translating the card would have changed what the operator was told to do.
// The Agent now stores only the FACT (key + arguments) and re-renders the marching
// orders when it builds the prompt, so the card is free to follow the display language.
//
// `content` remains the source-language (ja) fallback for reports written before the
// keys existed — those carry no notice_key and must degrade to readable text.
import { t, tMaybe } from "../../lib/i18n/index.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import type { ChatMessage } from "../../types/chat.ts";

// Notes are optional trailing sentences; each is present exactly when its argument is
// (the Agent decides — workspace/agent/chat_report_text.go). Keep this list in step.
const NOTE_RATE_LIMIT = "chat.report.note.rate_limit_resume";
const NOTE_FOLD = "chat.report.note.fold";
const NOTE_REOPEN_TARGET = "chat.report.note.reopen_target";

// Times ride as epoch millis so the Console formats them in the user's locale; formatted
// server-side they would stay Japanese in an English Console.
function at(ms: string | undefined): string {
  const n = Number(ms);
  return Number.isFinite(n) && n > 0 ? fmtDateTime(n) : "";
}

// report_kind is not read here (the key already encodes the kind) but belongs to the
// shape a caller hands over, so a full report message type-checks as-is.
type ReportFields = Pick<
  ChatMessage,
  "content" | "notice_key" | "notice_args" | "report_kind" | "report_reason"
>;

export function reportText(m: ReportFields): string {
  const key = m.notice_key;
  if (!key) return m.content; // written before P6 — the stored sentence is all there is
  const args = m.notice_args ?? {};
  // An exit reason we have no label for is shown raw rather than blank: a newer Agent
  // may report a reason this Console has never heard of.
  const reason = m.report_reason ?? "";
  const label = tMaybe(`chat.report.exit_reason.${reason}`) ?? reason;
  const head = tMaybe(key, { ...args, label });
  if (head === undefined) return m.content; // unknown key (newer Agent) — keep the fallback

  let out = t("chat.report.headline", { display: args.display ?? "", name: args.name ?? "" }) + head;
  if (args.resume_at) out += t(NOTE_RATE_LIMIT, { at: at(args.resume_at) });
  if (Number(args.fold_n ?? 0) >= 2) {
    out += t(NOTE_FOLD, { count: args.fold_n ?? "", ats: args.fold_ats ?? "" });
  }
  if (args.reopen_at) out += t(NOTE_REOPEN_TARGET, { at: at(args.reopen_at) });
  return out;
}
