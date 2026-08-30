// Work item inbox — Console endpoints (docs/log/80). The tenant header rides every request
// via the global fetch wrapper (client.ts), so callers pass nothing extra.
import { api, apiJSON, raw } from "../../core/api/client.ts";
import type { WorkItemQuery } from "./read.ts";

const q = (id: string) => encodeURIComponent(id);

/** The rail's payload. Works while the workspace is stopped — it reads the CP cache. */
export function workItemList(): Promise<unknown> {
  return api("api/work-items");
}

/** 更新 button: forced and synchronous on the CP side, so the response already carries
 * the new rows (a button that answers with the same stale list reads as broken). */
export function workItemRefresh(): Promise<unknown> {
  return apiJSON("api/work-items/refresh", "POST");
}

/** Bitbucket の「どこを見るか」の候補（docs/log/80 §80.22）。クローン用のリポジトリ一覧と
 *  同じ経路で、トークンを持つ Agent が答える。
 *
 *  ⚠️ **Workspace が停止中だと取れない**（レール本体は CP のキャッシュなので停止中でも
 *  開ける）。呼び手は取れなかったら手書きのクエリ欄に落ちること —— ここで詰まらせると、
 *  「停止中の Workspace をチケットから起こす」という機能の芯を、設定側で塞ぐことになる。 */
export function bitbucketRepoList(): Promise<unknown> {
  return api("api/connections/git/bitbucket.org/repos");
}

export function workItemQueryCreate(
  patch: Partial<WorkItemQuery>,
): Promise<WorkItemQuery | { error?: unknown }> {
  return apiJSON("api/work-item-queries", "POST", patch);
}

export function workItemQueryUpdate(
  id: string,
  patch: Partial<WorkItemQuery>,
): Promise<WorkItemQuery | { error?: unknown }> {
  return apiJSON(`api/work-item-queries/${q(id)}`, "PATCH", patch);
}

export function workItemQueryDelete(id: string): Promise<Response> {
  return raw(`api/work-item-queries/${q(id)}`, { method: "DELETE" });
}

/** Post a human-approved draft back to the ticket (docs/log/80 §80.10). The CP relays it to
 * the Agent, which holds the tokens; a stopped workspace answers 409 rather than being
 * started for it. */
export function workItemComment(rec: { provider: string; key: string; body: string }): Promise<unknown> {
  return apiJSON("api/work-items/comment", "POST", rec);
}

/** Ledger: record that a session was started for an item. Idempotent per
 * (itemKey, sessionName) on the CP, so a retried launch does not double the row. */
export function workItemSessionCreate(rec: {
  provider: string;
  itemKey: string;
  sessionName: string;
  repo?: string;
  branch?: string;
}): Promise<unknown> {
  return apiJSON("api/work-item-sessions", "POST", rec);
}

export function workItemSessionDelete(id: string): Promise<Response> {
  return raw(`api/work-item-sessions/${q(id)}`, { method: "DELETE" });
}
