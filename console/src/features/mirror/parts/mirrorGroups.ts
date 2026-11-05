import { quoteImagePaths, withoutClaudeInvisible } from "../../../lib/pastedImages.ts";
import { echoLanded } from "../pendingEcho.ts";
import { actionable, injectionSource, inQueueOrder, restorable, type queueEntries } from "../stopQueue.ts";
import { coalesceUserActions, groupTurns, isNoise, stripStudioSignal } from "../transcript/model.ts";
import type { Turn } from "../transcript/types.ts";
import type { SendEcho } from "./sendEcho.ts";

// sameQueuedPrompt says whether a queue entry is the prompt an echo stands for. On the claude
// TUI route the queue holds the text the Agent typed, which is not the text the Console sent:
// the attachment line's image paths are backticked (QuoteImagePaths) and the studio signal
// line rides along, while the echo keeps the bare paths. An exact compare misses, and the
// unpaired echo and queue row then fold into one bubble showing every thumbnail twice.
// So the echo is put through the same rewrite and compared whole. Undoing backticks on the
// queue side instead, or comparing with samePastedPrompt, also pairs prompts the member wrote
// differently (a backticked path of their own, words after a quoted instruction wording).
// claude also queues the prompt without the invisible characters it strips, so for an echo of a
// claude session (cleansInvisible) both sides are compared without them; for other kinds they
// still tell two prompts apart.
function sameQueuedPrompt(queued: string, echo: string, cleansInvisible = false): boolean {
  const q = stripStudioSignal(queued).trim();
  const e = stripStudioSignal(echo).trim();
  if (q === e || q === quoteImagePaths(e)) return true;
  if (!cleansInvisible) return false;
  const qc = withoutClaudeInvisible(q).trim();
  const ec = withoutClaudeInvisible(e).trim();
  return qc === ec || qc === quoteImagePaths(ec);
}

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
export function groupWithQueue(
  turns: Turn[],
  pendingSends: SendEcho[],
  queueShown: ReturnType<typeof queueEntries>,
  breaks: number[] = [],
) {
  const queuedLeft = [...queueShown];
  // An exact match is taken before a copy that differs only in invisible characters, as
  // pickEcho does for the echoes.
  const takeQueued = (text: string, cleansInvisible?: boolean) => {
    let i = queuedLeft.findIndex((q) => sameQueuedPrompt(q.text, text));
    if (i < 0 && cleansInvisible) i = queuedLeft.findIndex((q) => sameQueuedPrompt(q.text, text, true));
    if (i < 0) return null;
    return queuedLeft.splice(i, 1)[0];
  };
  const echoRows = pendingSends
    .filter((e) => !echoLanded(e, turns, isNoise)) // hide at render the instant the real turn lands
    .map((e) => {
      const q = takeQueued(e.text, e.cleansInvisible);
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
  return groupTurns(extras.length ? [...baseTurns, ...extras] : baseTurns, breaks);
}
