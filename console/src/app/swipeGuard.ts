// 横スワイプを画面ジェスチャとして横取りしてよいか — スマホの ← スワイプ（稼働中
// セッションのローテート、App.tsx）の入口ガード。
//
// window で拾う passive リスナなので、指がどこに置かれていても touchmove は届く。
// そのまま反応すると「コードブロックを横スクロールしたつもりがセッションが変わる」
// のような取り違えが起きるので、ジェスチャの起点になった要素から祖先を辿り、横方向の
// 操作をすでに持っている面の上なら見送る:
//   - ブラウザペイン（.browser-stage）— タッチは中の Chromium に転送している
//   - 入力欄 / contenteditable — キャレットや選択のドラッグ
//   - 横スクロールできる要素（pre, テーブル, タブ列, エディタ…）
//   - 明示オプトアウト [data-no-swipe]
// 判定は起点だけで行う（touchstart 時に 1 回）。純粋な DOM 関数なので dom vitest で
// 単体テストできる。

const EDITABLE_TAGS = new Set(["INPUT", "TEXTAREA", "SELECT"]);

/** その要素自身が編集面か。isContentEditable ではなく属性を見るのは、祖先を辿る
 * ループが継承を賄っており、かつ jsdom が isContentEditable を実装していないため
 * （実ブラウザだけで通るガードにしない）。 */
function editable(el: Element): boolean {
  if (EDITABLE_TAGS.has(el.tagName)) return true;
  const ce = el.getAttribute("contenteditable");
  return ce !== null && ce !== "false";
}

/** その要素自身が横スクロール可能か（内容がはみ出し、かつ overflow-x が動く設定）。 */
function scrollsHorizontally(el: Element): boolean {
  if (el.scrollWidth <= el.clientWidth + 1) return false;
  const ox = el.ownerDocument.defaultView?.getComputedStyle(el).overflowX;
  return ox === "auto" || ox === "scroll" || ox === "overlay";
}

/** ジェスチャの起点 target から見て、横スワイプを横取りしてはいけないか。 */
export function swipeBlocked(target: EventTarget | null): boolean {
  let el: Element | null = target instanceof Element ? target : null;
  // 深さ上限は保険（想定外に深い DOM でも touchstart を止めない）。
  for (let depth = 0; el && depth < 60; depth++, el = el.parentElement) {
    if (editable(el)) return true;
    if (el.hasAttribute("data-no-swipe")) return true;
    if (el.classList.contains("browser-stage")) return true;
    if (scrollsHorizontally(el)) return true;
  }
  return false;
}
