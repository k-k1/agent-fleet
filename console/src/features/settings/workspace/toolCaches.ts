// Request layer for the tool-cache rows (workspace/agent/tool_caches.go), shared by the
// Settings › Machine section and the low-disk notice's cleanup so both empty a cache through
// the one DELETE.
import { api } from "../../../core/api/client.ts";
import type { Place } from "./machinePlace.tsx";

export interface ToolCacheRow extends Place {
  name: string;
  bytes: number;
  files: number;
  /** pids that could be using the cache; the Agent refuses to empty it while non-empty. */
  busy?: number[];
  /** why a Gradle row offers nothing: the versions the projects pin could not be read. */
  skipped?: string;
}

/** GET: measures every row (slow; only on a press or an opened confirm). Throws on an error
 *  answer, which includes an Agent older than the endpoint. */
export async function loadToolCaches(): Promise<{ caches: ToolCacheRow[]; truncated: boolean }> {
  const res = await api("api/cleanup/tool-caches");
  if (!res || res.error || !Array.isArray(res.caches)) throw new Error("tool caches unavailable");
  return { caches: res.caches, truncated: !!res.truncated };
}

/** DELETE: empties one cache. Returns the raw answer ({bytes} or {error}); the Agent itself
 *  refuses while a build or install uses the cache (error.code "cache_in_use"). */
export function emptyToolCache(name: string): Promise<any> {
  return api("api/cleanup/tool-caches/" + encodeURIComponent(name), { method: "DELETE" });
}
