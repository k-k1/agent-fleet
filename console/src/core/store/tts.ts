// core/store/tts — 音声読み上げ（docs/log/24）のグローバル状態。再生はアプリ全体で 1 本だけ
// （チャットの回答も FileView の選択範囲も同じ 1 つの再生に集約）。TopBar が speaking を購読して
// 「読み上げ中＋停止」を出し、停止ボタンは stop() を叩く。エンジン（features/chat/tts.ts）が
// 非 React から setActive/setSpeaking を呼ぶ。
import { create } from "zustand";
import type { TtsController } from "../../features/chat/tts.ts";
import { getSettings, setSetting } from "../../lib/settings.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";

interface TtsStore {
  speaking: boolean; // 音声を再生中/合成キューに積んでいる間 true
  preparing: boolean; // 最初の音が鳴る前の合成待ち（TopBar のぐるぐる表示用）
  source: string; // 何を読み上げているかのラベル（"チャット" / "選択範囲" 等）
  voice: string; // 読んでいる声のキャラ名（"ずんだもん" 等。セッション別の声の判別用。"" = 非表示）
  sessionName: string; // 読み上げの発生元セッション名（ID）。左ペインの行アイコン判定用。"" = 非セッション（チャット/選択範囲等）
  purpose: "reading" | "session-notification" | "usage-notification" | "manual";
  active: TtsController | null; // 現在の再生コントローラ（内部管理・購読対象外）
  setActive(c: TtsController | null, source: string, voice?: string, sessionName?: string, purpose?: TtsStore["purpose"]): void;
  setSpeaking(v: boolean): void;
  setPreparing(v: boolean): void;
  stop(): void;
}

export const useTtsStore = create<TtsStore>((set, get) => ({
  speaking: false,
  preparing: false,
  source: "",
  voice: "",
  sessionName: "",
  purpose: "reading",
  active: null,
  setActive: (c, source, voice = "", sessionName = "", purpose = "reading") => set({ active: c, source, voice, sessionName, purpose }),
  setSpeaking: (v) => set((s) => (s.speaking === v ? s : { speaking: v })),
  setPreparing: (v) => set((s) => (s.preparing === v ? s : { preparing: v })),
  stop: () => get().active?.stop(),
}));

// 読み上げ ON/OFF トグル（TopBar のスピーカーボタンとキーボードコマンドで共有）。
// 再生中に押す＝黙らせたい意思として、停止＋その発生源フラグを OFF にする（停止だけだと
// ttsEnabled が ON のまま残り次の回答でまた鳴る、という主因を避ける）。アイドル時は素直に
// ttsEnabled をトグル。挙動は TopBar の元 onClick と同一。
export function toggleTtsPlayback(): void {
  const st = useTtsStore.getState();
  const busy = st.speaking || st.preparing;
  if (busy) {
    st.stop();
    if (st.purpose === "session-notification") setSetting("ttsSessionNotify", false);
    else if (st.purpose === "usage-notification") setSetting("usageResetNotify", false);
    else if (st.purpose !== "manual") setSetting("ttsEnabled", false);
    // Pressed while playing = "silence it now": report the stop rather than a specific
    // on/off, since which switch flipped depends on what was playing.
    toast(t("keys.toast.ttsStopped"), { kind: "success" });
    return;
  }
  const next = !getSettings().ttsEnabled;
  setSetting("ttsEnabled", next);
  toast(t(next ? "keys.toast.ttsOn" : "keys.toast.ttsOff"), { kind: "success" });
}
