// features/chat/tts — エージェント回答の音声読み上げ（docs/log/24 + ADR0013）。
//
// ストリーミング中の delta を受け取り、句点で「確定した文」だけを切り出して CP の
// /api/tts/synthesize（VOICEVOX/ずんだもん）へ逐次投げる。合成は in-flight を絞り
// （backpressure）、再生は到着順ではなく文の連番順に固定。準備できたバッファは
// AudioContext の時計で「前の終了時刻 + SENTENCE_GAP」に先行予約し、文間の隙間を
// 設計値に固定する（onended 駆動の start() ではイベントループ分のジッタが毎回入る）。
// stop() で in-flight fetch を abort・再生（予約済み含む）停止・キュー破棄。
//
// Markdown/コードブロック/URL は読み上げ用にプレーン化して除く（plainify）。

// 実体は parts/ の 5 枚に分かれている（このファイルは索引で、規則を持たない）。
// 依存は一方向で、逆流しない:
//   ttsOptions（設定 → TtsOptions）
//     → ttsVoices（キャラ・声プール・感情スタイル）
//       → ttsAudio（合成キャッシュ・AudioContext・出力音量）
//         → ttsPlay（startTts・グローバル停止・アナウンス直列キュー）
//           → ttsNarration（朗読モード）
// 呼び出し側は分割前と同じく "features/chat/tts.ts" から import する。
export { stopTtsForReplacement } from "./ttsControl.ts";
export type { TtsController, TtsEndReason, TtsStopReason } from "./ttsControl.ts";
export * from "./parts/ttsOptions.ts";
export * from "./parts/ttsVoices.ts";
export * from "./parts/ttsAudio.ts";
export * from "./parts/ttsPlay.ts";
export * from "./parts/ttsNarration.ts";
