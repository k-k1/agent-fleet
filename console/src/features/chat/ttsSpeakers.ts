// features/chat/ttsSpeakers — エンジン実カタログ（GET /api/tts/speakers = VOICEVOX の
// /speakers のプロキシ）のクライアントキャッシュ。設定のキャラクター選択・セッション声の
// プール・朗読ビューの声一覧が「実エンジンの speaker 番号」で動くための単一の取得点
// （静的に番号を持つと実エンジンとずれる。docs/log/24）。
//
// 取得は初参照時に一度だけ。失敗（エンジン停止中の 502 等）は 30s の負キャッシュで再試行
// する — 読み上げのたびに連打しない。取得できるまでは null のままで、呼び手（tts.ts の
// voiceCharacters）は静的フォールバックで動く。
import { api } from "../../core/api/client.ts";

export interface SpeakerStyle {
  id: string; // speaker 番号（合成リクエストの voice に渡す値）
  name: string; // スタイル名（ノーマル・あまあま等）
}
export interface Speaker {
  name: string; // キャラ名（設定 ttsVoicePool のキー）
  styles: SpeakerStyle[]; // トーク用スタイルのみ（歌唱系は CP が除外済み）
}

let cache: Speaker[] | null = null;
let inflight: Promise<Speaker[] | null> | null = null;
let failedAt = 0;

// speakersCatalog は同期 getter（未取得は null を返しつつ裏で取得をキック）。
// React からは loadSpeakers() を await して再レンダにつなぐ。
export function speakersCatalog(): Speaker[] | null {
  if (!cache) void loadSpeakers();
  return cache;
}

export async function loadSpeakers(): Promise<Speaker[] | null> {
  if (cache) return cache;
  if (Date.now() - failedAt < 30_000) return null;
  if (!inflight) {
    inflight = api("api/tts/speakers")
      .then((d) => {
        const list = Array.isArray(d?.speakers) ? (d.speakers as Speaker[]) : null;
        if (list && list.length) cache = list;
        else failedAt = Date.now();
        return cache;
      })
      .catch(() => {
        failedAt = Date.now();
        return null;
      })
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}
