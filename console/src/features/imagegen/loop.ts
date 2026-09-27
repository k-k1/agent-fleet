// The studio's loop on a narrow pane: talk, the agent edits the draft, trial, look, fix. What
// the three tabs owe each other so a phone can stay on the conversation — which pictures are
// new since the results were last looked at, which just finished, and whether a press would
// be refused. Everything here is derived from state the pane already holds; nothing polls.
import { useEffect, useRef, useState } from "react";
import type { ImagegenDraft } from "./draft.ts";

/** Longest prompt summary on the draft bar; the bar is one line on a 390 px phone. */
const SUMMARY_MAX = 80;

/** The prompt as one line: whitespace folded, cut with an ellipsis. Empty stays empty. */
export function promptSummary(prompt: string, max = SUMMARY_MAX): string {
  const one = prompt.replace(/\s+/g, " ").trim();
  return one.length > max ? one.slice(0, max - 1).trimEnd() + "…" : one;
}

export interface PressGate {
  /** A press in flight, or the engine unavailable — the same `busy` the form is given. */
  busy: boolean;
  trialFull: boolean;
  queueFull: boolean;
}

/** The form's own rule for its two buttons (GenerateForm's igen-actions), so the draft bar
 *  never offers a press the form would refuse. */
export function pressBlocked(draft: Pick<ImagegenDraft, "op" | "mask">, g: PressGate): { trial: boolean; enqueue: boolean } {
  const needsMask = draft.op === "inpaint" && !draft.mask.trim();
  return { trial: g.busy || g.trialFull || needsMask, enqueue: g.busy || g.queueFull || needsMask };
}

/** Paths in `now` that `seen` does not hold, in `now`'s order. */
export function unseen(now: readonly string[], seen: ReadonlySet<string>): string[] {
  return now.filter((p) => !seen.has(p));
}

/**
 * How many of `paths` arrived since the member last looked at them. The first list the pane
 * reads is the baseline — pictures that existed before the pane opened are not "new" — and
 * every render while `viewing` marks the whole list read.
 *
 * `ready` says the list has been read at least once; before that an empty list is "not
 * known yet", and taking it as the baseline would badge every existing picture.
 */
export function useUnseenCount(paths: readonly string[], ready: boolean, viewing: boolean): number {
  const [seen, setSeen] = useState<ReadonlySet<string> | null>(null);
  const key = paths.join("\n");
  useEffect(() => {
    if (!ready) return;
    setSeen((s) => {
      if (s && !viewing) return s;
      if (s && paths.every((p) => s.has(p))) return s;
      return new Set([...(s || []), ...paths]);
    });
    // `key` stands for `paths`: a new array of the same pictures is not a change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, ready, viewing]);
  return seen ? unseen(paths, seen).length : 0;
}

/**
 * Calls `onArrive(n)` when `paths` gains n pictures it did not hold on the previous change —
 * the finished jobs' files, found as the difference of two job lists the poller read anyway
 * (decision 2's cadence is untouched). The first ready list is the baseline, as above.
 */
export function useArrivals(paths: readonly string[], ready: boolean, onArrive: (n: number) => void): void {
  const prev = useRef<Set<string> | null>(null);
  const cb = useRef(onArrive);
  cb.current = onArrive;
  const key = paths.join("\n");
  useEffect(() => {
    if (!ready) return;
    const before = prev.current;
    prev.current = new Set(paths);
    if (!before) return;
    const n = unseen(paths, before).length;
    if (n > 0) cb.current(n);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, ready]);
}

/**
 * The conversation tab's mark from the bound session's state: "working" while a turn runs,
 * "ask" when it waits on the member, and "reply" once a turn ended while the member was on
 * another tab — kept until they look.
 */
export type ChatMark = "" | "working" | "ask" | "reply";

export function useChatMark(state: string | undefined, viewing: boolean): ChatMark {
  const [replied, setReplied] = useState(false);
  const last = useRef(state);
  useEffect(() => {
    const was = last.current;
    last.current = state;
    // Only a turn that ran to idle is a reply. working → question / permission / plan is the
    // agent asking, and that question later withdrawn back to idle answered nothing.
    if (was === "working" && (state === "idle" || state === "" || state == null) && !viewing) setReplied(true);
  }, [state, viewing]);
  useEffect(() => {
    if (viewing) setReplied(false);
  }, [viewing]);
  if (state === "working") return "working";
  if (state === "question" || state === "plan" || state === "permission") return "ask";
  return replied ? "reply" : "";
}
