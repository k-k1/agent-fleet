// タッチの横スワイプ認識 — 左ペインの出し入れ（従来）と、スマホでの稼働中セッション
// ローテート（← スワイプ）。
//
// App.tsx の useEffect に直書きされていた認識ロジックをここへ出した。理由は検証性で、
// スマホのジェスチャは実機でしか触れない一方、判定そのもの（どの向き・どの距離・どの
// 状態でどれが発火するか）は window イベントさえ与えれば jsdom で全パターン回せる。
// 画面状態の読み取りと副作用は SwipeSurfaces で注入する（ここはストアを一切 import
// しない）。
//
// 規則:
// - スマホ（≤760px）: 左ペインはオフキャンバス drawer。閉→左 1/3 から右で開く／開→左で閉じる。
//   drawer が閉じている間の ← は「稼働中セッションを 1 件送る」に使う。
// - タブレット（>760px かつタッチ）: 同じエッジスワイプがデスクトップのレールを
//   overlay として出し入れする。マウス機は TouchEvent を出さないので不活性。
// - 縦優先（|dx| <= |dy| は無視）＝スクロールを奪わない。長押し窓（500ms）を超えたら
//   候補を取り消す＝ミラーの選択ハンドルのドラッグをエッジスワイプに化けさせない。
import { swipeBlocked } from "./swipeGuard.ts";

/** 画面状態の読み取りと副作用（App.tsx がストアに配線する）。 */
export interface SwipeSurfaces {
  /** スマホ幅（≤760px）か。 */
  phone(): boolean;
  /** タッチ主体の端末か（スマホ幅でないときのタブレット判定に使う）。 */
  coarse(): boolean;
  /** モーダルが出ているか — 出ている間は背後のレールを触らせない。 */
  modal(): boolean;
  drawerOpen(): boolean;
  railOpen(): boolean;
  /** セッションのローテートを許す状況か（切り離しタブでは false）。 */
  rotatable(): boolean;
  setDrawer(open: boolean): void;
  openRailOverlay(): void;
  closeRail(): void;
  rotateNext(): void;
}

/** レール開閉に要る横移動量。 */
export const SWIPE_DIST = 50;
/** セッションの持ち替えに要る横移動量。画面が丸ごと入れ替わり戻すのに手間がかかるので、
 * レール開閉より長くして、拾い読み中の小さな横ぶれで発火しないようにする。 */
export const ROTATE_DIST = 70;
/** これを超えたら候補を取り消す（ブラウザの長押し窓）。 */
export const LONG_PRESS_MS = 500;

/** win にジェスチャ認識を取り付ける。戻り値は解除（StrictMode の二重実行に耐える）。 */
export function installSwipeGestures(win: Window, s: SwipeSurfaces): () => void {
  let sx = 0,
    sy = 0,
    mode: "open" | "close" | null = null,
    // このジェスチャが駆動する面（touchstart で確定）: スマホの drawer か、
    // タブレットのデスクトップレール（overlay）か。
    drawer = false,
    // ← でセッションをローテートしてよいか（同じく touchstart で確定）。
    rotate = false,
    longPressTimer: number | null = null;

  const cancelGesture = () => {
    mode = null;
    rotate = false;
    if (longPressTimer !== null) {
      win.clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  };

  // ローカル変数は touch — i18n の t を隠さない名前に（App.tsx から引き継いだ約束）。
  const onStart = (e: TouchEvent) => {
    const touch = e.touches[0];
    cancelGesture();
    const phone = s.phone();
    // スマホ幅より上では、タッチ端末（タブレット）のときだけ有効にする — マウス機に
    // エッジスワイプのレールは要らない。
    const tablet = !phone && s.coarse();
    drawer = phone;
    if (touch && (phone || tablet) && !s.modal()) {
      const isOpen = phone ? s.drawerOpen() : s.railOpen();
      if (isOpen) mode = "close";
      else if (touch.clientX < Math.min(win.innerWidth * 0.33, 160)) mode = "open";
      rotate = phone && !isOpen && s.rotatable() && !swipeBlocked(e.target);
    }
    if (touch) {
      sx = touch.clientX;
      sy = touch.clientY;
      if (mode || rotate) {
        longPressTimer = win.setTimeout(cancelGesture, LONG_PRESS_MS);
      }
    }
  };

  const onMove = (e: TouchEvent) => {
    if (!mode && !rotate) return;
    const touch = e.touches[0];
    if (!touch) return;
    const dx = touch.clientX - sx;
    const dy = touch.clientY - sy;
    if (Math.abs(dx) <= Math.abs(dy)) return;
    if (mode === "open" && dx > SWIPE_DIST) {
      if (drawer) s.setDrawer(true);
      else s.openRailOverlay();
      cancelGesture();
    } else if (mode === "close" && dx < -SWIPE_DIST) {
      if (drawer) s.setDrawer(false);
      else s.closeRail();
      cancelGesture();
    } else if (rotate && dx < -ROTATE_DIST) {
      // 左端始まりの ← は mode==="open"（右スワイプ待ち）と両立するが、向きで
      // ここに落ちるので取り合いにはならない。
      s.rotateNext();
      cancelGesture();
    }
  };

  const onEnd = () => cancelGesture();
  win.addEventListener("touchstart", onStart, { passive: true });
  win.addEventListener("touchmove", onMove, { passive: true });
  win.addEventListener("touchend", onEnd, { passive: true });
  win.addEventListener("touchcancel", onEnd, { passive: true });
  return () => {
    win.removeEventListener("touchstart", onStart);
    win.removeEventListener("touchmove", onMove);
    win.removeEventListener("touchend", onEnd);
    win.removeEventListener("touchcancel", onEnd);
    cancelGesture();
  };
}
