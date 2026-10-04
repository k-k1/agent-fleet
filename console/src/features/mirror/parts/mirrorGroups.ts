import { echoLanded } from "../pendingEcho.ts";
import { actionable, injectionSource, inQueueOrder, restorable, type queueEntries } from "../stopQueue.ts";
import { coalesceUserActions, groupTurns, isNoise } from "../transcript/model.ts";
import type { Turn } from "../transcript/types.ts";
import type { SendEcho } from "./sendEcho.ts";

// claude writes one logical response as several assistant events (text split by
// tool calls), so merge consecutive same-role turns into one block and drop the
// system-injected user lines (bash i/o, task notifications, slash-command echoes).
// Append any optimistic echoes as synthetic user turns (idx past any real line so keys
// stay unique and they sort last) — the mirror then shows a just-sent prompt at once.
// A queued prompt that matches a pending echo upgrades that echo's badge to "queued"
// (no second bubble); whatever remains was typed straight into the terminal, so it gets
// its own synthetic queued bubble. Multiset take: duplicate texts consume one entry each.
// The bubbles follow the queue's own order, so a peer message queued after the member's
// prompt is drawn after it.
//
// With queuedItems (ADR 0105) each entry also brings its id — the bubble's actions act on it —
// and its origin, so a queued peer or schedule input wears the badge it will wear once it runs.
export function groupWithQueue(turns: Turn[], pendingSends: SendEcho[], queueShown: ReturnType<typeof queueEntries>) {
  const queuedLeft = [...queueShown];
  const takeQueued = (text: string) => {
    const i = queuedLeft.findIndex((q) => q.text.trim() === text);
    if (i < 0) return null;
    return queuedLeft.splice(i, 1)[0];
  };
  const echoRows = pendingSends
    .filter((e) => !echoLanded(e, turns, isNoise)) // hide at render the instant the real turn lands
    .map((e) => {
      const q = takeQueued(e.text);
      const turn: Turn = {
        role: "user",
        text: e.text,
        idx: 1e9 + e.id,
        pending: true,
        queued: !!q,
        ...(q?.item
          ? { queueId: q.item.id, queueActionable: actionable(q.item), queueRestorable: restorable(q.item) }
          : {}),
      };
      return { turn, entry: q };
    });
  const queuedRows = queuedLeft.map((q, i) => {
    const turn: Turn = {
      role: "user",
      text: q.text,
      idx: 2e9 + i,
      queued: true,
      ...(q.item
        ? {
            queueId: q.item.id,
            queueActionable: actionable(q.item),
            queueRestorable: restorable(q.item),
            ...injectionSource(q.item),
          }
        : {}),
    };
    return { turn, entry: q };
  });
  const extras = inQueueOrder([...queuedRows, ...echoRows], queueShown);
  const baseTurns = coalesceUserActions(turns);
  return groupTurns(extras.length ? [...baseTurns, ...extras] : baseTurns);
}
