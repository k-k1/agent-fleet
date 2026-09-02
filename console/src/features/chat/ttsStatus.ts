// features/chat/ttsStatus — エンジン到達性（GET /api/tts/status）のクライアントキャッシュ。
//
// 「このデプロイに VOICEVOX（ずんだもん）があるか」を知る唯一の取得点。設定画面がずんだもん系の
// 項目を出すかどうかの判断に使う — VOICEVOX を立てていないデプロイ（ECS の既定構成では
// AF_TTS_ECS_SERVICE が未設定で、そもそもエンジンが存在しない）では、話者もキャラクターも
// 感情スタイルも選べて一切効かず、auto は日本語まで含めて Polly に落ちる。
//
// ttsSpeakers（実カタログ）と分けてあるのは、あちらの null が「まだ取得していない」と
// 「エンジンが無い」を区別できないため。判断材料は status の方に揃える。
// 判定そのもの（voicevoxAvailable）は ttsAvailability.ts にある（node 環境でテストするため）。
import { api } from "../../core/api/client.ts";
import type { TtsStatus } from "./ttsAvailability.ts";

export type { TtsProviderStatus, TtsStatus } from "./ttsAvailability.ts";
export { voicevoxAvailable, pollyAvailable } from "./ttsAvailability.ts";

const TTL = 30_000; // 管理者がエンジンを起動/停止しうるので、設定画面を開き直せば追随する程度に
let cache: TtsStatus | null = null;
let at = 0;
let inflight: Promise<TtsStatus | null> | null = null;

// ttsStatusCache は同期 getter（未取得・期限切れは null）。React の初期値用。
export function ttsStatusCache(): TtsStatus | null {
  return Date.now() - at < TTL ? cache : null;
}

export async function loadTtsStatus(): Promise<TtsStatus | null> {
  const fresh = ttsStatusCache();
  if (fresh) return fresh;
  if (!inflight) {
    inflight = api("api/tts/status")
      .then((d) => {
        const p = d?.providers;
        if (p && typeof p === "object") {
          cache = { voicevox: p.voicevox ?? { ready: false }, polly: p.polly ?? { ready: false } };
          at = Date.now();
        }
        return cache;
      })
      .catch(() => null) // 取得できない（古い CP・オフライン）→ 判断しない＝従来どおり全部出す
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}
