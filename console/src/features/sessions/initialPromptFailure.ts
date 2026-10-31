// The one place a create-time initial prompt that failed because the agent login (the session's kind) expired turns
// into a toast. The Agent settles the prompt in the background, long after the create call
// returned, and with the login already dead it settles within milliseconds of the create — so the
// first list that carries the session may already say failed, with no pending in between. The
// rule is therefore not "pending -> failed" but "failed that was not in the list we last saw":
// a session first SEEN failed in the opening snapshot of a scope (page reload, workspace switch)
// is old news and stays quiet, while one that appears or changes to failed afterwards is new.
//
// Everything observed belongs to one (user, tenant) scope. Another workspace can hold a session
// of the same name, so a scope change drops both the observations and the sticky toasts shown
// for the old scope.
import { getTenant, getUser } from "../../core/api/client.ts";
import { initialPromptAuthExpiredText, initialPromptToastKey, toastInitialPromptAuthExpired } from "../../lib/authExpiredToast.tsx";
import { t as tr } from "../../lib/i18n/index.ts";
import { dismissToast, toast } from "../../ui/toast.ts";
import type { Session } from "../../types/session.ts";

const REASON_AUTH_EXPIRED = "auth_expired";

const scopeNow = () => `${getUser()}\u0000${getTenant()}`;

let scope = "";
let baselined = false;
let seen = new Map<string, string>(); // session name -> initialPromptState in the last list
let shown = new Set<string>(); // sticky toast keys raised for the current scope

function reset(next: string): void {
  for (const k of shown) dismissToast(k);
  shown = new Set();
  seen = new Map();
  baselined = false;
  scope = next;
}

/** noteInitialPromptFailures compares each arriving list against the previous one. No poll of its
 *  own: applyList already sees every list. */
export function noteInitialPromptFailures(list: Session[]): void {
  const now = scopeNow();
  if (now !== scope) reset(now);
  // A fetch that fails never reaches applyList (store.refresh keeps the last list), so an empty
  // list here is a real answer. As the opening snapshot it baselines an empty workspace: the
  // first session ever created there must be announced when it arrives failed. After a baseline
  // it is ignored rather than remembered, or the next full list would read every session in it
  // as new.
  if (!list.length) {
    baselined = true;
    return;
  }
  const next = new Map<string, string>();
  for (const s of list) {
    const st = s.initialPromptState ?? "";
    next.set(s.name, st);
    if (baselined && st === "failed" && s.initialPromptReason === REASON_AUTH_EXPIRED && seen.get(s.name) !== "failed") {
      shown.add(initialPromptToastKey(now, s.name));
      toastInitialPromptAuthExpired(toast, now, s.name, initialPromptAuthExpiredText(tr, s.name, s.kind), s.kind);
    }
  }
  seen = next;
  baselined = true;
}

/** Test seam: forget everything observed. */
export function resetInitialPromptFailuresForTest(): void {
  reset("");
}
