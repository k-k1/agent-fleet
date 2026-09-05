// Work item inbox — Console endpoints (docs/log/80). The tenant header rides every request
// via the global fetch wrapper (client.ts), so callers pass nothing extra.
import { api, apiJSON, raw } from "../../core/api/client.ts";
import type { WorkItemQuery } from "./read.ts";

const q = (id: string) => encodeURIComponent(id);

/** The rail's payload. Works while the workspace is stopped — it reads the CP cache. */
export function workItemList(): Promise<unknown> {
  return api("api/work-items");
}

/** The refresh button: forced and synchronous on the CP side, so the response already carries
 * the new rows (a button that answers with the same stale list reads as broken). */
export function workItemRefresh(): Promise<unknown> {
  return apiJSON("api/work-items/refresh", "POST");
}

/** Candidate targets for a Bitbucket query, i.e. where to look (docs/log/80 §80.22). Same route
 *  as the repository list used for cloning: the Agent, which holds the token, answers it.
 *
 *  Unavailable while the Workspace is stopped (the rail itself still opens, because it reads the
 *  CP cache). A caller that gets nothing must fall back to the free-text query field: blocking
 *  here would let the settings side shut off the point of the feature, which is waking a stopped
 *  Workspace from a ticket. */
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
