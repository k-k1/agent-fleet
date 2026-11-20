// "Return to queue" on a closed run (ADR 0137, addendum 2026-11-19, part B): the person's own press
// that puts the run's notes back to wait, with no time and no hand-backs counted. A run whose
// session holds a pull request that is open or merged may have done the work, so the confirmation
// says so before the press. Shared by the inline run list and the full run history.
import type { MsgKey } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import type { ConfirmOptions } from "../../ui/ConfirmProvider.tsx";
import type { useToast } from "../../ui/ToastProvider.tsx";
import type { Session } from "../../types/session.ts";
import { runOpenPRs } from "./queues.ts";
import type { MemoDelivery } from "../../types/memo.ts";
import { memoQueueDeliveryReturn } from "./api.ts";

type Tr = (k: MsgKey, p?: Record<string, string | number>) => string;
type Toast = ReturnType<typeof useToast>;

/** Asks, then returns the run's notes. Resolves true when they went back. */
export async function confirmReturnRun(opts: {
  categoryId: string;
  d: Pick<MemoDelivery, "id" | "session">;
  session: Pick<Session, "pr" | "links"> | undefined;
  askConfirm: (o: ConfirmOptions) => Promise<boolean>;
  toast: Toast;
  tr: Tr;
}): Promise<boolean> {
  const { categoryId, d, session, askConfirm, toast, tr } = opts;
  const prs = runOpenPRs(session);
  const ok = await askConfirm({
    title: tr("memo.d_return_title"),
    body: prs.length > 0 ? tr("memo.d_return_body_pr", { name: d.session, prs: prs.join(", ") }) : tr("memo.d_return_body"),
    confirmLabel: tr("memo.d_return_confirm"),
    danger: prs.length > 0,
  });
  if (!ok) return false;
  try {
    const res = await memoQueueDeliveryReturn(categoryId, d.id);
    if (res?.error) {
      toast(errText(res.error) || tr("memo.d_return_failed"), { kind: "warn" });
      return false;
    }
    toast(tr("memo.d_return_done"), { kind: "success" });
    return true;
  } catch {
    toast(tr("memo.d_return_failed"), { kind: "warn" });
    return false;
  }
}
