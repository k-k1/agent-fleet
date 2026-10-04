// Past-session search (ADR 0110): the command palette's "conversations" mode. The Agent keeps a
// full-text index of what was said in every session of this workspace, any agent kind, running,
// stopped or archived; this module asks it and turns a hit into "open that session at that turn".
import { api, errDetail, type ApiError } from "../../core/api/client.ts";
import { requestJump } from "../mirror/scrollMark.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionUI } from "../sessions/ui.ts";
import { openSessionFromList } from "../sessions/open.ts";

/** One matching turn — GET /api/session-search's hits[] (sessionsearch.Hit in the Agent). */
export interface SessionSearchHit {
  session: string;
  display: string;
  kind: string;
  repo?: string;
  archived?: boolean;
  idx: number;
  role: string;
  ts?: string;
  snippet: string;
  score: number;
}

/** GET /api/session-search's answer (sessionsearch.Result in the Agent). */
export interface SessionSearchResult {
  hits: SessionSearchHit[];
  /** A pass is running, so sessions changed since the last one may be missing. */
  indexing: boolean;
  indexed: number;
  total: number;
}

// Enough to choose from in a palette; the Agent caps at 50.
const LIMIT = 30;

export function sessionSearchPath(q: string): string {
  return `api/session-search?q=${encodeURIComponent(q)}&limit=${LIMIT}`;
}

/** Runs one search. Rejects when the search did not run — a refused or failed answer from the CP
 * or the Agent, or no answer at all — with the reason as the message, so the caller can tell
 * "nothing matched" from "could not search". */
export async function searchSessions(q: string, signal?: AbortSignal): Promise<SessionSearchResult> {
  const d = await api(sessionSearchPath(q), { signal });
  if (!d || typeof d !== "object") throw new Error(errDetail(null));
  if (d.error) throw new Error(errDetail(d.error as ApiError | string));
  return {
    hits: Array.isArray(d.hits) ? d.hits : [],
    indexing: !!d.indexing,
    indexed: Number(d.indexed) || 0,
    total: Number(d.total) || 0,
  };
}

export type OpenHitOutcome = "opened" | "archived" | "missing";

/**
 * Opens the session a hit belongs to, scrolled to the hit's turn.
 *
 * The jump rides the mirror's own position memory (scrollMark.requestJump): a mirror that mounts
 * the session restores the mark, and one already showing it is told to apply it. Its limit is the
 * mark's — a turn older than the loaded tail window is not reached.
 *
 * An archived session cannot be opened until it is restored, so the archive shelf opens instead;
 * the mark is left in place for when it is.
 */
export function openSessionSearchHit(hit: SessionSearchHit, split: boolean, running: boolean): OpenHitOutcome {
  requestJump(hit.session, { atBottom: false, idx: hit.idx, offset: 0, near: true });
  const s = useSessionsStore.getState().sessions.find((x) => x.name === hit.session);
  if (s) return openSessionFromList(s, split, running) ? "opened" : "missing";
  if (hit.archived) {
    useSessionUI.getState().openArchived();
    return "archived";
  }
  return "missing";
}
