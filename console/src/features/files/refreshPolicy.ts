// The one place that holds the timing of FILES auto-refresh, so that the side that fires
// (sessionRefresh.ts) and the sides that read (ProjectFiles / FilesChanges) see the same
// numbers.
//
// The policy: events pull the trigger, intervals are the safety net. The end of a turn
// (sessionRefresh.ts) is the primary trigger; the other two only cover what events cannot
// reach — someone looking while a session is still running, and someone who was away.
// None of them is a periodic poll (Console<->CP traffic reduction, docs/build/02 §2.3).
//
// Each number follows from what it waits on, not from a guess at how it feels:
//   - COALESCE / MIN_GAP ... the unit is an agent's turn boundary, not a human action
//   - WORKING_TICK ......... one round trip per directory on screen, so deliberately
//     slower than editor/probe.ts (12s), which watches a single file
//   - REVALIDATE_GAP ....... the floor at which repeated alt-tabbing is not a poll

/** Wait that folds several sessions finishing at once into a single refresh. */
export const COALESCE_MS = 400;

/** Shortest interval between re-reads of the same working copy, so a burst of short turns
 *  cannot pile up round trips. */
export const MIN_GAP_MS = 3000;

/**
 * Interval at which a running session's working copy is re-read at low frequency even
 * mid-turn. The timer stops entirely once no session is running, so this is not a
 * resident poller.
 */
export const WORKING_TICK_MS = 20_000;

/**
 * Shortest interval for revalidating on return to the tab/window. The end-of-turn signal
 * is the primary trigger and this is the "it finished while I was away" safety net — and
 * also the only practical way to pick up sessions that carry no state (shell / SSM) and
 * changes made outside Agent Fleet.
 */
export const REVALIDATE_GAP_MS = 10_000;
