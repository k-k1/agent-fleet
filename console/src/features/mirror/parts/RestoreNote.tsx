import type { ReactNode } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

/** restoreNoteStamps reads the poll's `transcriptRestoredAt`: one RFC3339 stamp per time the
 *  Agent put a deleted conversation log back from its backup. Anything else is dropped, so a
 *  newer or older Agent cannot break the render. */
export function restoreNoteStamps(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((s): s is string => typeof s === "string" && s !== "") : [];
}

/** restoreNoteCards turns the stamps into inline cards for TranscriptView, each placed at the
 *  moment of its restore. */
export function restoreNoteCards(stamps: string[]): Array<{ at: number; node: ReactNode }> {
  return stamps.map((s) => ({ at: Date.parse(s), node: <RestoreNote key={"restore-" + s} /> }));
}

/** RestoreNote tells the member the conversation was recovered from the Agent's backup. */
export function RestoreNote() {
  return (
    <div className="mt-restore-note muted" role="note">
      <Icon name="info" /> {tr("mirror.transcript_restored_note")}
    </div>
  );
}
