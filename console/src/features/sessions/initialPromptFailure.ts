// The one place a create-time initial prompt that failed because the Claude login expired turns
// into a toast. The Agent settles the prompt in the background (pending -> failed), long after
// the create call returned, so the only observable moment is the list update that carries the
// change. A session first SEEN as failed (page reload, another member's earlier create) is not
// announced: that failure is old news, and the toast would re-fire on every reload.
import { toastInitialPromptAuthExpired } from "../../lib/authExpiredToast.tsx";
import { t as tr } from "../../lib/i18n/index.ts";
import { toast } from "../../ui/toast.ts";
import type { Session } from "../../types/session.ts";

const REASON_AUTH_EXPIRED = "auth_expired";

let pending = new Set<string>();

/** noteInitialPromptFailures compares each arriving list against the sessions it last saw
 *  pending. No poll of its own: applyList already sees every list. */
export function noteInitialPromptFailures(list: Session[]): void {
  // An empty list is also the startup value and what a failed fetch leaves behind; keep what
  // was observed rather than forgetting the pending sessions.
  if (!list.length) return;
  const next = new Set<string>();
  for (const s of list) {
    if (s.initialPromptState === "pending") next.add(s.name);
    else if (s.initialPromptState === "failed" && s.initialPromptReason === REASON_AUTH_EXPIRED && pending.has(s.name)) {
      toastInitialPromptAuthExpired(toast, s.name, tr("start.initial_prompt_auth_expired", { name: s.name }));
    }
  }
  pending = next;
}

/** Test seam: forget the observed pending sessions. */
export function resetInitialPromptFailuresForTest(): void {
  pending = new Set();
}
