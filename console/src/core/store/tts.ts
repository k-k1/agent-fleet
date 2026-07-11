// core/store/tts — 音声読み上げ（docs/24）のグローバル状態。再生はアプリ全体で 1 本だけ
// （チャットの回答も FileView の選択範囲も同じ 1 つの再生に集約）。TopBar が speaking を購読して
// 「読み上げ中＋停止」を出し、停止ボタンは stop() を叩く。エンジン（features/chat/tts.ts）が
// 非 React から setActive/setSpeaking を呼ぶ。
import { create } from "zustand";
import type { TtsController } from "../../features/chat/tts.ts";

interface TtsStore {
  speaking: boolean; // 音声を再生中/合成キューに積んでいる間 true
  preparing: boolean; // 最初の音が鳴る前の合成待ち（TopBar のぐるぐる表示用）
  source: string; // 何を読み上げているかのラベル（"チャット" / "選択範囲" 等）
  voice: string; // 読んでいる声のキャラ名（"ずんだもん" 等。セッション別の声の判別用。"" = 非表示）
  active: TtsController | null; // 現在の再生コントローラ（内部管理・購読対象外）
  setActive(c: TtsController | null, source: string, voice?: string): void;
  setSpeaking(v: boolean): void;
  setPreparing(v: boolean): void;
  stop(): void;
}

export const useTtsStore = create<TtsStore>((set, get) => ({
  speaking: false,
  preparing: false,
  source: "",
  voice: "",
  active: null,
  setActive: (c, source, voice = "") => set({ active: c, source, voice }),
  setSpeaking: (v) => set((s) => (s.speaking === v ? s : { speaking: v })),
  setPreparing: (v) => set((s) => (s.preparing === v ? s : { preparing: v })),
  stop: () => get().active?.stop(),
}));
