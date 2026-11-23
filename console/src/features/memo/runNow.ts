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
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { Memo, MemoQueue } from "../../types/memo.ts";
import { memoQueueFire } from "./api.ts";
import { fireBlockWords } from "./queues.ts";

/** How a press ended: started a run, was refused for want of a slot (the caller may show who holds
 *  them), or failed otherwise. The toast has been shown already. */
export type RunNowResult = "started" | "no_slot" | "failed";

export async function runNow(categoryId: string, memoId?: string): Promise<RunNowResult> {
  try {
    const res = await memoQueueFire(categoryId, memoId);
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

/** Press "Run now" for one memo of a category (its context menu). It shares the category's press
 *  state, so it cannot overlap the category's own Run now; null = a press was already in flight. */
export async function runMemoNow(categoryId: string, memoId: string): Promise<RunNowResult | null> {
  if (pressing.has(categoryId)) return null;
  setPressing(categoryId, true);
  try {
    return await runNow(categoryId, memoId);
  } finally {
    setPressing(categoryId, false);
  }
}

/** Why one memo's "Run now" is unavailable, as a catalogue key and its parameters; null when a press
 *  may be sent. Advisory, like fireBlockWords: the Control Plane's claim stays the authority and
 *  answers a memo that cannot run with memo_queue_memo_not_ready. Only the slot reason is read from
 *  the queue's fireBlock; the rest is the memo's own state. */
export function memoRunNowWhy(
  m: Pick<Memo, "state" | "afterStatus" | "startAfter" | "notBefore">,
  queue: Pick<MemoQueue, "fireBlock"> | undefined,
  now: number,
  at: (iso: string) => string,
): { key: MsgKey; params?: Record<string, string | number> } | null {
  if (m.state === "held") return { key: "memo.run_one_held" };
  if (m.afterStatus?.some((a) => a.state !== "satisfied")) return { key: "memo.run_one_prerequisites" };
  const later = (iso: string | undefined) => !!iso && Date.parse(iso) > now;
  if (later(m.notBefore)) return { key: "memo.run_one_deferred", params: { at: at(m.notBefore ?? "") } };
  if (later(m.startAfter)) return { key: "memo.run_one_scheduled", params: { at: at(m.startAfter ?? "") } };
  if (queue?.fireBlock?.reason === "no_place") {
    return { key: queue.fireBlock.detail?.by === "member" ? "memo.waiting_no_place_member" : "memo.waiting_no_place" };
  }
  return null;
}

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
