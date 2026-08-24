// 操作ビーコン（docs/75 P3）— 「人が今 Console を触っている」を Workspace のアイドル
// 時計へ伝える。
//
// なぜ要るか: アイドル自動停止の在席判定は端末の**打鍵**に絞ってある（開きっぱなしの
// タブが永久に Workspace を温めるのを止めるため）。その結果、打鍵も送信もせずミラーで
// 過去ログを読み続けている人が「不在」に見える。読んでいる最中にコンテナが止まると
// Agent ごと落ちるので、転写すら取れなくなる（ミラーが「停止中は履歴を取得できません」
// に変わる）。
//
// ★送るのは「タブが開いている」ではなく「人が操作した」。この 2 つを取り違えると、
// P3 が消したはずの「開いたまま忘れられたタブが課金し続ける」がそのまま戻る。だから:
//   - document が**可視**のときだけ（裏のタブ・別ウィンドウの裏は送らない）
//   - **実操作**のときだけ（isTrusted・プログラム的な合成イベントは数えない）
//   - **60 秒に 1 回**まで（スクロール 1 回ごとに POST しない）
// 逆に、スクロールとクリックを数えるのは意図的: 読むという行為は打鍵を伴わない。
import { workspaceAttention } from "../core/api/client.ts";

/** ビーコンの最小間隔。CP 側でも 5 秒に畳まれるが、無駄な往復は手前で止める。 */
export const ATTENTION_INTERVAL_MS = 60_000;

/** 人の操作と見なすイベント。keydown はここに要る — 端末の外（コンポーザ、検索、
 *  モーダル）で打つキーは端末 WS を通らないので、打鍵判定には現れない。 */
const GESTURES = ["pointerdown", "keydown", "wheel", "touchstart"] as const;

/** shouldBeacon は「今このイベントで送ってよいか」の純関数。テストで固定する。 */
export function shouldBeacon(
  trusted: boolean,
  visible: boolean,
  now: number,
  lastSent: number,
  interval = ATTENTION_INTERVAL_MS,
): boolean {
  if (!trusted) return false; // 合成イベント（自動テスト・拡張・自前の dispatch）
  if (!visible) return false; // 裏のタブは「見ていない」
  return now - lastSent >= interval;
}

/** wireAttentionBeacon installs the listeners; returns the unsubscribe.
 *
 * requireTrusted はテスト用の継ぎ目。jsdom の dispatchEvent が作るイベントは
 * isTrusted=false で固定（own かつ non-configurable なので偽装できない）ため、
 * 「送る側」の配線を確かめるにはここを開けるしかない。既定は true で、本番の
 * 呼び出し側（App のブート）は引数を渡さない — 合成イベントを数えないことは
 * 別のテストで固定してある。 */
export function wireAttentionBeacon({ requireTrusted = true } = {}): () => void {
  // 起動直後は送らない（0 ではなく now）: 画面を開いただけで在席を主張しない。
  // 最初の操作から数え始める — 開いたまま放置されたタブは 1 回も送らない。
  let lastSent = Date.now();
  const onGesture = (e: Event) => {
    const trusted = e.isTrusted || !requireTrusted;
    if (!shouldBeacon(trusted, document.visibilityState === "visible", Date.now(), lastSent)) return;
    lastSent = Date.now();
    // 応答は見ない。停止処理と競合したときの 409 は「在席の記録が 1 回落ちた」だけで、
    // 次の操作でまた届く。ここでトーストを出すと、無関係な操作に無関係なエラーが出る。
    void workspaceAttention();
  };
  for (const type of GESTURES) {
    window.addEventListener(type, onGesture, { capture: true, passive: true });
  }
  return () => {
    for (const type of GESTURES) window.removeEventListener(type, onGesture, { capture: true });
  };
}
