// Fast-forward with a way out of a stale index.lock. A git killed while holding the lock
// (a container stop) leaves it behind, and every later write in that working copy fails
// until it is removed; the agent answers git_index_lock_stale only when no git runs there.
// The user still confirms first, and the agent checks again before it deletes the file.
import { apiJSON } from "../../core/api/client.ts";
import { askConfirm } from "../../ui/confirmBridge.ts";
import { t } from "../../lib/i18n/index.ts";

export const STALE_INDEX_LOCK = "git_index_lock_stale";

/** POSTs a fast-forward endpoint (`…/ff`, `…/parent-ff`). On a stale lock it asks, and on
 *  yes retries once with removeStaleLock; on no it returns the error for the caller's toast. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function postFastForward(path: string): Promise<any> {
  const res = await apiJSON(path, "POST", {});
  if (res?.error?.code !== STALE_INDEX_LOCK) return res;
  const ok = await askConfirm({
    title: t("repo.stale_lock.title"),
    body: t("repo.stale_lock.body", { path: res.error.lock || "index.lock" }),
    confirmLabel: t("repo.stale_lock.remove"),
    danger: true,
  });
  if (!ok) return res;
  return apiJSON(path, "POST", { removeStaleLock: true });
}
