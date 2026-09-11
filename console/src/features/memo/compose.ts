// composeMemoMessage — the text the send modal (SendMemoModal.tsx) opens with. It lives
// apart from the modal so it can be tested without the API client and the component tree.
import { t } from "../../lib/i18n/index.ts";
import type { Memo } from "../../types/memo.ts";

// Concatenate the selected memos directly, grouped by category, mirroring the server's
// buildFlushMessage (memo.go) so an unedited send is byte-for-byte the server flush.
// A lone memo skips the scaffolding (see below) — keep both sides in step.
export function composeMemoMessage(memos: Memo[]): string {
  // One memo has nothing to group or count: the "## category" heading and the "1. "
  // prefix would only make the agent read past a list of one, and a multi-line note
  // reads as a list item that lost its indent. Send it as the bare text instead.
  const solo = memos.length === 1;
  const num = (n: number) => (solo ? "" : `${n}. `);
  const cont = solo ? "" : "   "; // continuation lines align under the number
  const sorted = memos.slice().sort((a, b) => (a.category < b.category ? -1 : a.category > b.category ? 1 : 0));
  const lines: string[] = [];
  let lastCat = "\x00";
  let n = 0;
  for (const m of sorted) {
    if (!solo && m.category !== lastCat) {
      lastCat = m.category;
      n = 0;
      if (lines.length) lines.push("");
      lines.push("## " + (m.category || t("memo.uncategorized")));
    }
    n++;
    if (m.kind === "file") {
      lines.push(num(n) + t("memo.flush_file", { path: m.refPath }));
      if (m.body) lines.push(cont + m.body);
    } else if (m.body) {
      lines.push(num(n) + m.body);
    } else {
      lines.push(num(n) + t("memo.flush_image_only"));
    }
    if (m.attachments?.length) {
      lines.push(cont + t("memo.flush_images", { names: m.attachments.map((a) => a.name).join(", ") }));
    }
  }
  return lines.join("\n");
}
