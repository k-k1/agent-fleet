// core/push/events — 統合 push チャネルの受信ハブ（通信量削減 P3）。
//
// CP の GET /api/events (SSE) 1 本から workspace / sessions / stats /
// notifications / workitems のフレームを受け、登録されたハンドラ（wire.ts がストアへ配線）
// に配る。フレームの data は既存 REST 応答と同一 shape なので、適用ロジックは
// ポーリング経路と共用できる。
//
// フォールバック方針: 既存の常時ポーラーは削除しない。各ポーラーは
// pushHealthy() の間だけ tick をスキップし、ストリームが切れた瞬間（旧 CP の
// 404 / ネットワーク断 / 非表示化）から従来どおりポーリングが引き継ぐ —
// 新 Console × 旧 CP の版ずれでも機能が欠けない。
//
// 受信は EventSource ではなく fetch ストリーム（chat stream と同方式）。
// fetch ラッパーが cookie 認証・X-AF-Tenant ヘッダ・401→AuthExpiredModal latch
// を注入してくれるので、WS のような query param 認証の別扱いが要らない。
import { rel } from "../api/client.ts";

export type PushStream = "workspace" | "sessions" | "stats" | "notifications" | "workitems";
// data は stream 毎の REST 応答 shape そのもの。検証は適用側（ストア）の責務。
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Handler = (data: any) => void;

const handlers = new Map<PushStream, Set<Handler>>();

/** Register a handler for one stream. Returns the unsubscriber. */
export function onPush(stream: PushStream, h: Handler): () => void {
  let set = handlers.get(stream);
  if (!set) handlers.set(stream, (set = new Set()));
  set.add(h);
  return () => set?.delete(h);
}

let healthy = false;
/** True while the push stream is live — pollers skip their tick then. */
export const pushHealthy = (): boolean => healthy;

// ストリーム確立ハンドラ。フレームでは運ばれず「起動時に 1 回だけ読む」類の
// 状態（whoami のデプロイ capability など）を、CP 再起動＝再接続の後に読み直す
// ためのフック。ここでもストアは import しない（配線は wire.ts の責務）。
const connectHandlers = new Set<() => void>();

/** Register a callback fired each time the stream (re)connects. Returns the
 * unsubscriber. */
export function onPushConnect(h: () => void): () => void {
  connectHandlers.add(h);
  return () => {
    connectHandlers.delete(h);
  };
}

// stream 毎の受信カウンタ。ポーラーは fetch 前後で比較し、fetch 中に push
// フレームが適用されていたら自分の（より古いかもしれない）結果を捨てる —
// 遅いモバイル回線で数秒遅れて届いたポーリング応答が push を上書きして
// 「次の変化まで古い表示のまま」になる穴を塞ぐ。
const stamps = new Map<PushStream, number>();
export const pushStamp = (stream: PushStream): number => stamps.get(stream) || 0;

const RETRY_MAX = 30000;
const RETRY_UNSUPPORTED = 300000; // 旧 CP（404/405）: 5 分毎に再確認するだけ
const WATCHDOG_MS = 45000; // サーバは静穏でも ~20s 毎に ping — 2 回欠けたら死んだ扱い

let stopped = true;
let ctrl: AbortController | null = null;
let retryTimer = 0;
let watchdogTimer = 0;
let backoff = 1000;

function dispatch(frame: string): void {
  // SSE フレーム 1 個（\n\n 区切り済み）。コメント行（": ping"）は無視。
  const line = frame.startsWith("data:") ? frame.slice(5).trim() : "";
  if (!line) return;
  let obj: { stream?: string; data?: unknown };
  try {
    obj = JSON.parse(line);
  } catch {
    return;
  }
  const stream = obj.stream as PushStream | undefined;
  if (!stream || obj.data == null) return;
  stamps.set(stream, (stamps.get(stream) || 0) + 1);
  for (const h of handlers.get(stream) || []) {
    try {
      h(obj.data);
    } catch {
      /* 1 個のハンドラ例外でストリームを殺さない */
    }
  }
}

function scheduleRetry(delay: number): void {
  window.clearTimeout(retryTimer);
  retryTimer = window.setTimeout(() => void connect(), delay);
}

async function connect(): Promise<void> {
  if (stopped || document.hidden || ctrl) return;
  const my = new AbortController();
  ctrl = my;
  const armWatchdog = () => {
    window.clearTimeout(watchdogTimer);
    watchdogTimer = window.setTimeout(() => my.abort(), WATCHDOG_MS);
  };
  let unsupported = false;
  const startedAt = Date.now();
  try {
    // 接続確立フェーズにも期限を付ける — 無応答プロキシでヘッダが返らないと、
    // watchdog（read 毎に再武装）に到達できず永久に再接続しない。確立後は従来
    // どおり read 側の armWatchdog が引き継ぐので、長寿命ストリームは切れない。
    armWatchdog();
    const res = await fetch(rel("api/events"), { signal: my.signal });
    const ct = res.headers.get("Content-Type") || "";
    if (!res.ok || !ct.startsWith("text/event-stream") || !res.body) {
      // 旧 CP はこのルートを知らない（404）。エラー表示はしない — ポーラーが
      // そのまま現役なので機能は落ちず、単に従来の通信量に戻るだけ。
      unsupported = res.status === 404 || res.status === 405;
      // 読まない body は明示的に解放する（接続をぶら下げたままにしない）。
      void res.body?.cancel().catch(() => {});
      return;
    }
    healthy = true;
    for (const h of connectHandlers) {
      try {
        h();
      } catch {
        /* 1 個のハンドラ例外でストリームを殺さない（dispatch と同じ方針） */
      }
    }
    armWatchdog();
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      armWatchdog();
      buf += dec.decode(value, { stream: true });
      let idx: number;
      while ((idx = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        // restartPush/非表示化で abort された後の残りフレームは旧テナントの
        // ものでありうる — 適用しない。
        if (my.signal.aborted) return;
        dispatch(frame);
      }
    }
  } catch {
    /* abort（非表示化・テナント切替・watchdog）またはネットワーク断 — 下で再接続 */
  } finally {
    window.clearTimeout(watchdogTimer);
    healthy = false;
    if (ctrl === my) ctrl = null;
    if (!stopped && !document.hidden) {
      // 30 秒以上生きたストリームの切断は一時的とみなし即再接続、即死は指数
      // バックオフ（サーバ再起動の嵐を作らない）。
      if (Date.now() - startedAt > 30000) backoff = 1000;
      const delay = unsupported ? RETRY_UNSUPPORTED : backoff;
      backoff = Math.min(backoff * 2, RETRY_MAX);
      scheduleRetry(delay);
    }
  }
}

/** Drop the current stream (if any) and reconnect shortly — tenant switch. Call
 * BEFORE resetting tenant-scoped stores so no old-tenant frame lands after. */
export function restartPush(): void {
  window.clearTimeout(retryTimer);
  backoff = 1000;
  if (ctrl) ctrl.abort();
  else void connect();
}

/** Boot wiring (App effect). Connects while visible, disconnects while hidden
 * (the fallback pollers keep their existing hidden-tab behavior), reconnects on
 * network recovery. Returns the cleanup — StrictMode-safe. */
export function startPushChannel(): () => void {
  stopped = false;
  const onVis = () => {
    if (document.hidden) ctrl?.abort();
    else restartPush();
  };
  const onOnline = () => restartPush();
  document.addEventListener("visibilitychange", onVis);
  window.addEventListener("online", onOnline);
  void connect();
  return () => {
    stopped = true;
    document.removeEventListener("visibilitychange", onVis);
    window.removeEventListener("online", onOnline);
    window.clearTimeout(retryTimer);
    window.clearTimeout(watchdogTimer);
    ctrl?.abort();
    ctrl = null;
    healthy = false;
  };
}
