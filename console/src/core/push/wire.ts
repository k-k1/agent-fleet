// core/push/wire — 統合 push チャネルのフレームをストアへ配線する（通信量削減
// P3）。transport（events.ts）はストアを import しない — ストア側は逆向きに
// pushHealthy/pushStamp を import してポーリングをフォールバック化するので、
// 双方向 import を避けるため配線だけをこのモジュールに分離している。
// stats ストリームだけはグローバルストアを持たず（4s 毎の値変化で全体を再描画
// しないため — WsBar のコメント参照）、WsBar が自分で onPush 購読する。
import { onPush, onPushConnect } from "./events.ts";
import { useWorkspaceStore } from "../store/workspace.ts";
import { useTenantStore } from "../store/tenant.ts";
import { useSessionsStore } from "../../features/sessions/store.ts";
import { applyPushedNotifications } from "../../features/notifications/store.ts";

/** Register the store-apply handlers. Returns the cleanup (StrictMode-safe). */
export function wirePushApply(): () => void {
  const un = [
    onPush("workspace", (d) => useWorkspaceStore.getState().applyPush(d || {})),
    onPush("sessions", (d) => useSessionsStore.getState().applyList(d?.sessions || [])),
    onPush("notifications", (d) => applyPushedNotifications(d || {})),
    // 再接続は「CP が再起動したかもしれない」の合図 — フレームでは運ばれない
    // whoami（デプロイ capability 込み）を読み直す。本体側で間引く。
    onPushConnect(() => void useTenantStore.getState().refreshWhoami()),
  ];
  return () => un.forEach((u) => u());
}
