// "Review again in <reviewer>": remember which session reviewed a plan, and send the revised
// plan back to it as a peer message instead of launching a cold reviewer.
//
// Where the memory lives: on the planner session's record at the Agent (Session.planReviewers,
// written by POST /api/sessions/{name}/plan-reviewer and read back with the session list), keyed
// by plan FILE path, so every device of the user offers the shortcut. This browser's
// localStorage stays as a cache and fallback: an Agent without the route (older build) never
// returns the field, and a write that failed here is still remembered for this browser. The
// precedence is: a write this tab has not yet seen come back in the list, then the Agent's
// record, then localStorage. The path is half the key because one planner session can hold two
// plans (claude names the file per plan), and a revision rewrites the SAME file, which is what
// makes the second round find the first round's reviewer.
//
// Only the NAME is stored, on both sides, and bounded (MAX_ENTRIES here, MaxPlanReviewers at the
// Agent). The label shown on the button is read from the live session list each render, so a
// renamed reviewer shows its new title and a deleted one is not offered (names are immutable
// slugs, never reused). Any miss, from either store, falls back to the launch dialog: a missed
// shortcut, never a wrong send.
import { useSyncExternalStore } from "react";
import { sessionAlive, sessionPeerSend, sessionPlanReviewerSet, sessionReady } from "../../core/api/client.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { Session } from "../../types/session.ts";
import { planFingerprint } from "./planComments.ts";
import { rereviewPrompt } from "./planReview.ts";

const LS_KEY = "af.plan-reviewers";
/** Oldest entry evicted first, so localStorage cannot grow without bound. */
const MAX_ENTRIES = 50;

type Store = Record<string, string>;

export const reviewerKey = (planner: string, planPath: string) => `${planner}\u0000${planPath}`;

function load(): Store {
  try {
    const parsed = JSON.parse(localStorage.getItem(LS_KEY) || "null") as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Store = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) if (typeof v === "string" && v) out[k] = v;
    return out;
  } catch {
    return {}; // private mode / corrupt JSON: the shortcut is auxiliary, start empty
  }
}

/** Writes of this tab the session list has not confirmed yet ("" = unlinked). Without it the
 *  Agent's older record would win for the seconds until the next list poll, or for good when the
 *  write failed. */
const pending = new Map<string, string>();

const serverReviewer = (sessions: Session[], planner: string, planPath: string): string =>
  sessions.find((s) => s.name === planner)?.planReviewers?.find((e) => e.path === planPath)?.reviewer || "";

let store: Store = load();
const listeners = new Set<() => void>();

// Another tab can launch a new reviewer for the same plan. Every write merges onto what storage
// holds NOW (not this tab's copy from import time), or a write for another plan would erase it.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== null && e.key !== LS_KEY) return;
    const before = store;
    store = load();
    // Another tab chose a different reviewer for a plan: that is newer than this tab's
    // unconfirmed write.
    for (const k of [...pending.keys()]) if (before[k] !== store[k]) pending.delete(k);
    listeners.forEach((l) => l());
  });
}

function commit(next: Store) {
  store = next;
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(next));
  } catch {
    /* quota / private mode: keep it in memory for this tab */
  }
  listeners.forEach((l) => l());
}

/** Tell the Agent, then drop the pending marker once a fresh list carries the answer. A failed
 *  write keeps the marker, so this tab keeps its own newer choice over the Agent's older one. */
function syncToAgent(planner: string, planPath: string, reviewer: string) {
  const k = reviewerKey(planner, planPath);
  pending.set(k, reviewer);
  void sessionPlanReviewerSet(planner, planPath, reviewer)
    .then(async (ok) => {
      if (!ok || pending.get(k) !== reviewer) return;
      await useSessionsStore.getState().refresh();
      if (pending.get(k) === reviewer) pending.delete(k);
    })
    .catch(() => {})
    .finally(() => listeners.forEach((l) => l()));
}

/** Record that `reviewer` reviewed the plan at `planPath` of `planner`. A later launch for the
 *  same plan replaces it: the newest reviewer is the one holding the latest round's context. */
export function rememberReviewer(planner: string, planPath: string, reviewer: string) {
  if (!planner || !planPath || !reviewer) return;
  const k = reviewerKey(planner, planPath);
  const next = load();
  delete next[k]; // re-insert last so insertion order is recency
  next[k] = reviewer;
  const keys = Object.keys(next);
  for (const old of keys.slice(0, Math.max(0, keys.length - MAX_ENTRIES))) delete next[old];
  commit(next);
  syncToAgent(planner, planPath, reviewer);
}

export function forgetReviewer(planner: string, planPath: string) {
  const k = reviewerKey(planner, planPath);
  const next = load();
  if (k in next) {
    delete next[k];
    commit(next);
  }
  // Even when this browser held nothing: the link may live only at the Agent.
  syncToAgent(planner, planPath, "");
}

/** Does `planner` have any remembered reviewer? Gates the plan-file lookup behind the card. */
export const hasRememberedReviewer = (planner: string, sessions: Session[] = useSessionsStore.getState().sessions): boolean =>
  Object.keys(store).some((k) => k.startsWith(planner + "\u0000") && pending.get(k) !== "") ||
  [...pending].some(([k, v]) => v && k.startsWith(planner + "\u0000")) ||
  !!sessions.find((s) => s.name === planner)?.planReviewers?.length;

export const rememberedReviewer = (
  planner: string,
  planPath: string,
  sessions: Session[] = useSessionsStore.getState().sessions,
): string => {
  const k = reviewerKey(planner, planPath);
  return pending.has(k) ? pending.get(k)! : serverReviewer(sessions, planner, planPath) || store[k] || "";
};

/** Test seam: re-read storage (the module reads it once at import). */
export function reloadReviewersForTest() {
  pending.clear();
  store = load();
  listeners.forEach((l) => l());
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

/** The session to offer "Review again" for: remembered, still in the loaded list, not the
 *  planner itself. null = launch dialog instead. An unloaded list is "unknown", not "gone",
 *  but offering nothing until it loads is the safe side. */
export function liveReviewer(
  planner: string,
  planPath: string,
  sessions: Session[],
  remembered = rememberedReviewer(planner, planPath, sessions),
): Session | null {
  if (!planPath || !remembered || remembered === planner) return null;
  const s = sessions.find((x) => x.name === remembered);
  // resumable:false = archive only, nothing a message could wake.
  return s && (s.alive || s.resumable !== false) ? s : null;
}

export const reviewerLabel = (s: Session) => s.title || s.label || s.name;

export const useHasRememberedReviewer = (planner: string): boolean => {
  const sessions = useSessionsStore((s) => s.sessions);
  return useSyncExternalStore(subscribe, () => hasRememberedReviewer(planner, sessions));
};

/** The reviewer for the plan at `planPath` of `planner`, reactive to both the memory and the
 *  session list. */
export function useReviewer(planner: string, planPath: string): Session | null {
  const sessions = useSessionsStore((s) => s.sessions);
  const remembered = useSyncExternalStore(subscribe, () => rememberedReviewer(planner, planPath, sessions));
  return liveReviewer(planner, planPath, sessions, remembered);
}

export type RereviewResult = { ok: true } | { ok: false; message: string; silent?: boolean };

const READY_TIMEOUT_MS = 30_000;
const READY_POLL_MS = 500;

/** Make the reviewer able to take a message: resume it when stopped and wait for its composer.
 *  Runs BEFORE the planner is rejected on the live card, so a reviewer that will not come up
 *  leaves the plan exactly as it was. `silent` = start() already toasted the reason. */
export async function prepareReviewer(
  reviewer: Session,
  deps = { alive: sessionAlive, start: (n: string) => useSessionsStore.getState().start(n), ready: sessionReady, sleep: (ms: number) => new Promise<void>((r) => setTimeout(r, ms)) },
): Promise<RereviewResult> {
  // Ask the Agent, not the Session snapshot taken at press time: a retry minutes later must see
  // a reviewer that has stopped since. A failed read falls back to the snapshot.
  const alive = (await deps.alive(reviewer.name)) ?? !!reviewer.alive;
  if (alive) return { ok: true };
  if (!(await deps.start(reviewer.name))) return { ok: false, message: "", silent: true };
  for (let waited = 0; waited < READY_TIMEOUT_MS; waited += READY_POLL_MS) {
    if (await deps.ready(reviewer.name)) return { ok: true };
    await deps.sleep(READY_POLL_MS);
  }
  return { ok: false, message: "timeout" };
}

/** Send the revised plan to the reviewer. `question` (not `request`) because the findings are
 *  the answer: a request only owes a reply when blocked, and the planner is waiting. */
export async function sendRereview(
  planner: string,
  planPath: string,
  reviewer: Session,
  plan: string,
  send: typeof sessionPeerSend = sessionPeerSend,
): Promise<RereviewResult> {
  const r = await send(
    reviewer.name,
    planner,
    "question",
    rereviewPrompt({ parent: planner, path: planPath, revision: planFingerprint(plan) }),
  );
  if (r.ok) return { ok: true };
  // A reviewer deleted since the list was read is gone for good: stop offering it.
  if (r.code === "peer_target_unknown") forgetReviewer(planner, planPath);
  return { ok: false, message: r.message || "" };
}


/** The two cards' flows share one order: take the card's lock, make the reviewer ready, check
 *  the plan on screen is still the one pressed, THEN do the card's own precondition (`beforeSend`:
 *  the live card rejects the planner so it is idle rather than behind the approval modal; false =
 *  it failed, send nothing), THEN send, and only a successful send runs `onSent` (the carried
 *  card discards itself there).
 *
 *  `lock` makes a second press during the (up to 30 s) resume a no-op. A failure before the
 *  send leaves the plan card untouched; one AFTER beforeSend hands `onFail` a `retry` that
 *  repeats just the send, because the rejected plan's card is gone and nothing else would. */
export async function runRereview(o: {
  planner: string;
  path: string;
  plan: string;
  reviewer: Session;
  lock: { held: boolean };
  /** False once the card this press came from is no longer the plan on screen. */
  stillCurrent?: () => boolean;
  beforeSend?: () => Promise<boolean>;
  onSent?: () => void;
  onFail: (message: string, retry?: () => Promise<boolean>) => void;
  /** False once a retry must not send any more (see the retry below). */
  retryValid?: () => boolean;
  /** A retry ended for good (sent, or refused as stale): withdraw whatever offered it. */
  onRetryDone?: () => void;
  prepare?: typeof prepareReviewer;
  send?: typeof sessionPeerSend;
}): Promise<boolean> {
  if (o.lock.held) return false;
  o.lock.held = true;
  try {
    const ready = await (o.prepare ?? prepareReviewer)(o.reviewer);
    if (!ready.ok) {
      if (!ready.silent) o.onFail(ready.message);
      return false;
    }
    if (o.stillCurrent && !o.stillCurrent()) return false;
    if (o.beforeSend && !(await o.beforeSend())) return false;
    // The retry handed to onFail after a reject. It is single-use (spent by the first success),
    // single-flight, re-resumes a reviewer that stopped again, and refuses once `retryValid`
    // says the world moved on (a new plan, another review): it would otherwise send a stale
    // revision id for whatever plan the file holds now.
    let spent = false;
    let flying = false;
    const retry = async (): Promise<boolean> => {
      if (spent || flying) return false;
      flying = true;
      try {
        const stale = () => {
          if (o.retryValid && !o.retryValid()) {
            spent = true;
            o.onRetryDone?.();
            return true;
          }
          return false;
        };
        if (stale()) return false;
        const again = await (o.prepare ?? prepareReviewer)(o.reviewer);
        if (!again.ok) {
          if (!again.silent) o.onFail(again.message, retry);
          return false;
        }
        if (stale()) return false;
        return await attempt(true);
      } finally {
        flying = false;
      }
    };
    const attempt = async (isRetry: boolean): Promise<boolean> => {
      const sent = await sendRereview(o.planner, o.path, o.reviewer, o.plan, o.send);
      if (!sent.ok) {
        o.onFail(sent.message, o.beforeSend ? retry : undefined);
        return false;
      }
      spent = true;
      if (isRetry) o.onRetryDone?.();
      o.onSent?.();
      return true;
    };
    return await attempt(false);
  } finally {
    o.lock.held = false;
  }
}
