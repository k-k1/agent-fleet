// 横スワイプを画面ジェスチャとして横取りしてよいか — スマホの ← スワイプ（稼働中
// セッションのローテート、App.tsx）の入口ガード。
//
// window で拾う passive リスナなので、指がどこに置かれていても touchmove は届く。
// そのまま反応すると「コードブロックを横スクロールしたつもりがセッションが変わる」
// のような取り違えが起きるので、ジェスチャの起点になった要素から祖先を辿り、横方向の
// 操作をすでに持っている面の上なら見送る:
//   - ブラウザペイン（.browser-stage）— タッチは中の Chromium に転送している
//   - 入力欄 / contenteditable — キャレットや選択のドラッグ
//   - 横に振る面（pre, テーブル, タブ列, サジェストのチップ行…）＝ pansHorizontally
//   - 明示オプトアウト [data-no-swipe]
// 逆に、読むために縦へ送るスクロール容器は [data-swipe-y] を付けて「横のはみ出しは
// 事故」と宣言でき、その要素は横スクローラとして数えない（pansHorizontally の注記）。
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

/** その要素自身が「横に振る面」か（内容がはみ出し、かつ overflow-x が動く設定）。
 *
 * overflow-x の計算値だけで見ると縦スクローラまで巻き込む。CSS は overflow-x/y の片方が
 * visible でもう片方が非 visible なら visible を auto に計算するので、`overflow-y: auto`
 * しか書いていない要素でも overflow-x は "auto" として読める（実ブラウザで実測）。つまり
 * 実質 scrollWidth > clientWidth だけの判定になる。
 *
 * それで起きていたのが「特定のセッションだけスワイプでの切り替えが効かない」: 折り返し
 * 位置を持たない長い文字列（sha256:… / クエリ付き URL / 長い識別子）が転写に 1 つ混ざる
 * だけで、転写のスクロール容器（.mirror-body）が横へはみ出す（幅 390px の実測で
 * sw=633/cw=390）。この容器は転写のあらゆる点の祖先なので、ふつうの段落の上を払っても
 * 弾かれ、しかも scrollWidth は転写全体の値だから、その 1 行が画面外へ流れても、
 * セッションを開き直しても直らなかった。
 *
 * そこで縦に送る面は [data-swipe-y] で「横のはみ出しは事故」と宣言できるようにし、その
 * 要素は横スクローラとして数えない。判定そのものは変えない — 横にも縦にも本当に振る面
 * （コードビュー・diff・クランプした ASCII モックアップ）を「縦にスクロールしないものだけ
 * が横スクローラ」のような推測で素通りさせないため。 */
function pansHorizontally(el: Element): boolean {
  if (el.hasAttribute("data-swipe-y")) return false;
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
    if (pansHorizontally(el)) return true;
  }
  return false;
}
