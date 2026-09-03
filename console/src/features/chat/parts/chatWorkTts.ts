import { useTtsStore } from "../../../core/store/tts.ts";
import { stopTtsForReplacement, type TtsController, type TtsEndReason } from "../tts.ts";

/** 1 ターン分の「作業過程の小声読み」を回すキュー。ターンごとに 1 つ作って使い捨てる。 */
export interface WorkStepTts {
  /** 確定した作業過程を 1 本積み、鳴らせるなら鳴らし始める。 */
  push(text: string): void;
  /** 再生中・未再生をまとめて破棄し、購読も解く（最終回答の到着・中断・エラー）。 */
  close(): void;
}

// 作業過程は確定順に 1 本ずつ読む。次の step が来ても再生中の step は止めず、
// 最終回答が来た時点でだけ再生中・未再生をまとめて破棄して通常声へ譲る。
// 他の読み上げに置換された場合も、それを即座に置換し返さず、グローバル再生が
// 空いてから続きを再開する。
export function createWorkStepTts({
  workMode,
  makeTts,
  getPaneTts,
  setPaneTts,
}: {
  /** settings.ttsWorkRead — "off" のときキューは何もしない。 */
  workMode: string;
  makeTts: (work?: boolean, onEnd?: (reason: TtsEndReason) => void) => TtsController | null;
  /** この会話がいまペインの再生枠を握っているなら、そのコントローラ。 */
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
