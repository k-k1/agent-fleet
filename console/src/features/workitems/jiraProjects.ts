// The project keys of the member's Jira connection, so the mirror can link PROJ-12 for a project
// no saved query reaches (docs/log/80 §80.25, ADR 0061 decision 28). Metadata only; the CP bounds
// and caches the list (workitems_jira_projects.go) and an empty answer is today's behaviour: a key
// whose project is not cached stays plain text.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";

interface JiraProjectsState {
  keys: string[];
  /** Set when the keys last landed; 0 = never. */
  at: number;
}

export const useJiraProjects = create<JiraProjectsState>(() => ({ keys: [], at: 0 }));

/** The list belongs to one member of one tenant. The Console switches both without a reload, and
 * core/store/tenant.ts calls this on every such switch (this module must not import that store: it
 * reads localStorage at load, which every test that mocks the api client would trip over). It drops
 * everything at once, and bumps the generation so an answer asked for under the previous owner is
 * discarded when it lands. */
let generation = 0;

export function invalidateJiraProjects(): void {
  generation++;
  pending = null;
  lastTry = 0;
  useJiraProjects.setState({ keys: [], at: 0 });
}

/** The CP holds a list for an hour; asking more often than this only reaches its cache. */
export const JIRA_PROJECTS_TTL_MS = 10 * 60 * 1000;
/** A failed read is not retried sooner: every rendered message asks. */
const RETRY_MS = 60 * 1000;
const KEY_RE = /^[A-Z][A-Z0-9_]{1,9}$/;

let pending: Promise<void> | null = null;
let lastTry = 0;

/** Load the list when it has never been read or is older than the TTL. Never throws; a failure
 * leaves the previous keys in place (a stale key costs a "no details" note, never a wrong link). */
export function ensureJiraProjects(): Promise<void> {
  const now = Date.now();
  if (useJiraProjects.getState().at && now - useJiraProjects.getState().at < JIRA_PROJECTS_TTL_MS) return Promise.resolve();
  if (pending) return pending;
  if (now - lastTry < RETRY_MS) return Promise.resolve();
  lastTry = now;
  const gen = generation;
  const mine: Promise<void> = api("api/work-items/jira-projects")
    .then((d: { keys?: unknown; error?: unknown } | null) => {
      if (gen !== generation) return;
      if (!d || d.error || !Array.isArray(d.keys)) return;
      const keys = d.keys.filter((k): k is string => typeof k === "string" && KEY_RE.test(k));
      useJiraProjects.setState({ keys, at: Date.now() });
    })
    .catch(() => {})
    .finally(() => {
      if (pending === mine) pending = null;
    });
  pending = mine;
  return mine;
}

/** For tests: the store and the retry clock are module state and would leak between cases. */
export function resetJiraProjects(): void {
  invalidateJiraProjects();
}
