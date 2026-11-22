// "Run now" of an auto-run category: the press and the words for why it is unavailable. It is
// offered on the Memo queue view's category head and in the Auto-run view's category menu, so the
// two share one implementation. The enabled state is advisory (the list's fireBlock); the Control
// Plane's claim stays the authority and its refusals still arrive as toasts.
import { useState, useSyncExternalStore } from "react";
import { errText } from "../../core/api/client.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { kindLabel } from "../../lib/sessionkind.ts";
import { toast } from "../../ui/toast.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { memoQueueFire } from "./api.ts";
import { fireBlockWords } from "./queues.ts";

/** How a press ended: started a run, was refused for want of a slot (the caller may show who holds
 *  them), or failed otherwise. The toast has been shown already. */
export type RunNowResult = "started" | "no_slot" | "failed";

export async function runNow(categoryId: string): Promise<RunNowResult> {
  try {
    const res = await memoQueueFire(categoryId);
    if (res?.error) {
      toast(errText(res.error) || t("memo.d_fire_failed"), { kind: "warn" });
      return res.error.code === "memo_queue_no_slot" ? "no_slot" : "failed";
    }
    toast(t("memo.d_fire_started"), { kind: "success" });
    return "started";
  } catch {
    toast(t("memo.d_fire_failed"), { kind: "warn" });
    return "failed";
  }
}

/** Categories with a press in flight. Module-level so a menu that closed and reopened still knows. */
const pressing = new Set<string>();
const pressListeners = new Set<() => void>();
const setPressing = (id: string, on: boolean) => {
  if (on) pressing.add(id);
  else pressing.delete(id);
  for (const l of pressListeners) l();
};
const subscribePressing = (l: () => void) => {
  pressListeners.add(l);
  return () => void pressListeners.delete(l);
};

/** The state of a Run now control. Whether it is enabled comes from the queue list's fireBlock
 *  alone ("nothing_waiting" included): the memo list is a separate read and may lag it. */
export function useRunNow(categoryId: string, queue: MemoQueue | undefined) {
  const tr = useT();
  const [, setBusy] = useState(false);
  // Shared per category: a menu reopened mid-press must see the press end (and re-enable) too.
  const busy = useSyncExternalStore(subscribePressing, () => pressing.has(categoryId));
  const why = fireBlockWords(queue, (iso) => fmtDateTime(iso), (kind) => kindLabel(kind));
  const reason = why ? tr(why.key, why.params) : "";
  const press = async (): Promise<RunNowResult | null> => {
    if (busy || reason) return null;
    setBusy(true);
    setPressing(categoryId, true);
    try {
      return await runNow(categoryId);
    } finally {
      setPressing(categoryId, false);
      setBusy(false);
    }
  };
  return {
    busy,
    /** Why a press would be refused, "" when it may start a run. */
    reason,
    /** The control's tooltip and accessible name. */
    title: reason ? tr("memo.d_fire_blocked", { why: reason }) : tr("memo.d_fire_title"),
    press,
  };
}
