// Request layer for the tool-cache rows (workspace/agent/tool_caches.go), shared by the
// Settings › Machine section and the low-disk notice's cleanup so both empty a cache through
// the one DELETE.
import { deleteToolCache, getToolCaches } from "./workspaceApi.ts";
import { markCleaned } from "../../../core/store/homeReclaim.ts";
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

/** The last automatic go-build trim since the Agent started (go_trim, #4560). */
export interface GoTrim {
  at: string;
  bytes: number;
  files: number;
}

/** GET: measures every row (slow; only on a press or an opened confirm). Throws on an error
 *  answer, which includes an Agent older than the endpoint. */
export async function loadToolCaches(): Promise<{ caches: ToolCacheRow[]; truncated: boolean; goTrim?: GoTrim }> {
  const res = await getToolCaches();
  if (!res || res.error || !Array.isArray(res.caches)) throw new Error("tool caches unavailable");
  return { caches: res.caches, truncated: !!res.truncated, goTrim: res.go_trim };
}

/** DELETE: empties one cache. Returns the raw answer ({bytes} or {error}); the Agent itself
 *  refuses while a build or install uses the cache (error.code "cache_in_use"). */
export async function emptyToolCache(name: string): Promise<Awaited<ReturnType<typeof deleteToolCache>>> {
  try {
    return await deleteToolCache(name);
  } finally {
    // Whatever the answer (a lost one included), the walk behind the low-disk notice's figure may
    // no longer describe the disk, so every caller of this DELETE retires it.
    markCleaned();
  }
}
