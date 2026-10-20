// Agent memory search for the command palette's memory mode (ADR 0108). Two reads, both the ones
// Settings > Agent memory > Memories makes: the memory list (name, description, type, author; what
// a query is matched against at once) and the body search, which the Agent answers without
// counting a use for the memories it finds, so searching does not move the usage ranking.
import { api, errDetail, type ApiError } from "../../core/api/client.ts";
import type { MemoryListed, MemorySearchHit } from "../settings/memory/memoryTypes.ts";

export interface MemoryList {
  entries: MemoryListed[];
  /** Memories left out because their text failed the secret scan: counted, never listed. */
  withheld: number;
}

// Enough to choose from in a palette.
const LIMIT = 30;

/** Rejects with the reason as the message when the answer is an error or no answer at all, so the
 * caller can tell "nothing matched" from "could not search". */
function answer(d: unknown): Record<string, unknown> {
  if (!d || typeof d !== "object") throw new Error(errDetail(null));
  const r = d as Record<string, unknown>;
  if (r.error) throw new Error(errDetail(r.error as ApiError | string));
  return r;
}

export async function loadMemoryList(signal?: AbortSignal): Promise<MemoryList> {
  const d = answer(await api("api/agents/memory/entries/list", { signal }));
  return { entries: Array.isArray(d.entries) ? (d.entries as MemoryListed[]) : [], withheld: Number(d.withheld) || 0 };
}

export function memorySearchPath(q: string): string {
  return `api/agents/memory/entries/member-search?limit=${LIMIT}&q=${encodeURIComponent(q)}`;
}

/** Body search over the user scope and every project (no scope parameter). */
export async function searchMemoryBodies(q: string, signal?: AbortSignal): Promise<MemorySearchHit[]> {
  const d = answer(await api(memorySearchPath(q), { signal }));
  return Array.isArray(d.hits) ? (d.hits as MemorySearchHit[]) : [];
}
