// features/chat/ttsAvailability — 「このデプロイに VOICEVOX（ずんだもん）があるか」の判定。
//
// 取得（ttsStatus.ts）と分けてあるのは、あちらが core/api/client を読み込む＝module scope で
// localStorage に触れ、node 環境の vitest から import できないため（ttsCache.ts を tts.ts から
// 分離したのと同じ理由）。判定はここに置いて素でテストする。

export interface TtsProviderStatus {
  ready: boolean; // いま合成できるか
  enabled?: boolean; // 管理者トグル（voicevox のみ。false = ルーティング停止）
  managed?: boolean; // ECS オンデマンド管理下か（voicevox のみ）
  state?: string; // managed のときの ECS サービス状態（running/starting/stopped）
}
export interface TtsStatus {
  voicevox: TtsProviderStatus;
  polly: TtsProviderStatus;
}

// voicevoxAvailable は「このデプロイに VOICEVOX エンジンがあるか」。null = まだ分からない。
//
// ready ではなく available であることが肝: ECS オンデマンド管理下（managed）なら、いま停止して
// いても管理者が起動すれば使えるので「ある」。ready も managed も無いのは、エンジンを一台も
// 用意していないデプロイ＝ずんだもんは今後も鳴らない。
// 管理者トグルの enabled は見ない（それは「今は使わない」であって「無い」ではない）。
export function voicevoxAvailable(st: TtsStatus | null): boolean | null {
  if (!st) return null;
  return st.voicevox.ready || st.voicevox.managed === true;
}
