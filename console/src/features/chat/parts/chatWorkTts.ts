import { useTtsStore } from "../../../core/store/tts.ts";
import { stopTtsForReplacement, type TtsController, type TtsEndReason } from "../tts.ts";

/** Queue that drives the quiet reading of one turn's work trace. Create one per turn and
 * throw it away afterwards. */
export interface WorkStepTts {
  /** Enqueue one settled work-trace step and start playing it if playback is free. */
  push(text: string): void;
  /** Discard both playing and pending items and drop the subscription (final answer
   * arrived, aborted, or errored). */
  close(): void;
}

// Work-trace steps are read one at a time in the order they settle. A newly arriving step
// does not stop the one playing; only the final answer discards playing and pending items
// together and yields to the normal voice. If another reading preempts us, do not preempt it
// straight back — wait for global playback to free up, then resume.
export function createWorkStepTts({
  workMode,
  makeTts,
  getPaneTts,
  setPaneTts,
}: {
  /** settings.ttsWorkRead — when "off" the queue does nothing. */
  workMode: string;
  makeTts: (work?: boolean, onEnd?: (reason: TtsEndReason) => void) => TtsController | null;
  /** The controller, if this conversation currently holds the pane's playback slot. */
  getPaneTts: () => TtsController | null;
  setPaneTts: (ctl: TtsController | null) => void;
}): WorkStepTts {
  const workQueue: string[] = [];
  let workCurrent: TtsController | null = null;
  let workClosed = false;
  let unsubscribeWork = () => {};
  let pumpWork = () => {};
  pumpWork = () => {
    if (workMode === "off" || workClosed || workCurrent || !workQueue.length) return;
    const st = useTtsStore.getState();
    if (st.active || st.speaking) return;
    const text = workQueue.shift()!;
    const c = makeTts(true, (reason) => {
      if (workCurrent === c) workCurrent = null;
      if (getPaneTts() === c) setPaneTts(null);
      if (reason === "explicit") {
        workClosed = true;
        workQueue.length = 0;
        unsubscribeWork();
      } else {
        queueMicrotask(pumpWork);
      }
    });
    if (!c) return;
    workCurrent = c;
    setPaneTts(c);
    c.push(text);
    c.flush();
  };
  unsubscribeWork =
    workMode === "off"
      ? () => {}
      : useTtsStore.subscribe(() => {
          queueMicrotask(pumpWork);
        });
  return {
    push(text: string) {
      workQueue.push(text);
      pumpWork();
    },
    close() {
      unsubscribeWork();
      if (workClosed) return;
      workClosed = true;
      workQueue.length = 0;
      stopTtsForReplacement(workCurrent);
      if (getPaneTts() === workCurrent) setPaneTts(null);
      workCurrent = null;
    },
  };
}
