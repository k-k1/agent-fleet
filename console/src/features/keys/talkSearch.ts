// Past-session search (ADR 0110): the command palette's "conversations" mode. The Agent keeps a
// full-text index of what was said in every session of this workspace, any agent kind, running,
// stopped or archived; this module asks it and turns a hit into "open that session at that turn".
import { api } from "../../core/api/client.ts";
import { saveMark } from "../mirror/scrollMark.ts";
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

/** Runs one search. Rejects with the Agent's message when it answered an error. */
export async function searchSessions(q: string, signal?: AbortSignal): Promise<SessionSearchResult> {
  const d = await api(sessionSearchPath(q), { signal });
  if (!d || d.error) throw new Error(typeof d?.error === "string" ? d.error : "search failed");
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
 * The jump rides the mirror's own position memory: the mark is what the mirror restores when it
 * mounts the session, so no second scrolling path exists to disagree with it. Its limits are the
 * mark's — a turn older than the loaded tail window lands at the end, and a session already shown
 * in the target pane does not re-read its mark.
 *
 * An archived session cannot be opened until it is restored, so the archive shelf opens instead;
 * the mark is left in place for when it is.
 */
export function openSessionSearchHit(hit: SessionSearchHit, split: boolean, running: boolean): OpenHitOutcome {
  saveMark(hit.session, { atBottom: false, idx: hit.idx, offset: 0, near: true });
  const s = useSessionsStore.getState().sessions.find((x) => x.name === hit.session);
  if (s) return openSessionFromList(s, split, running) ? "opened" : "missing";
  if (hit.archived) {
    useSessionUI.getState().openArchived();
    return "archived";
  }
  return "missing";
}
