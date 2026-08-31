// コンポーザーの textarea を内容の高さに合わせる（頭打ちは CSS の max-height、その先は
// textarea の内部スクロール）。ミラー / アシスタントチャット / メモキューの 3 つの入力欄が使う。
//
// 素直に書くと「height:auto にする → scrollHeight を読む → その値を height に入れる」だが、
// この “計測のあいだだけ rows 相当（2 行）まで縮む” が外へ漏れる。ミラーのコンポーザーは
// 転写（.mirror-body・flex:1）の兄弟なので、縮んだ瞬間だけ転写の clientHeight が入力欄の
// ぶん伸び、末尾に貼りついていたビューの scrollTop がブラウザに切り詰められる。高さを戻して
// も scrollTop は戻らない ＝ 1 打鍵ごとに末尾から浮く。浮く量は「入力欄の高さ − 2 行」なので、
// 入力欄が縦に伸びているときほど大きい（実測 154px で「最新へ」まで出た）。
//
// Chromium ではスクロールアンカリングがこの切り詰めを打ち消すので表に出ない — が、それは
// たまたまであって仕様ではない。アンカリングを持たない / 抑止されたエンジンでは素通しで出る
// （実測: .mirror-body に overflow-anchor:none を当てると 1 打鍵目で gap=154px・
// scripts/mirror-scroll の typing シナリオはこの条件で見る）。
//
// なので計測のあいだは入力欄の親（コンポーザーの行）を min-height で止め、縮みが外へ出ない
// ようにする。“縮ませない” だけなので、border-box の値を渡す誤差で数 px 大きく見積もっても
// 害はない — 容器が縮む向きに scrollTop の切り詰めは起きない。
export function autoGrowTextarea(el: HTMLTextAreaElement | null): void {
  if (!el) return;
  const row = el.parentElement;
  const frozen = row ? Math.ceil(row.getBoundingClientRect().height) : 0;
  const prevMin = row ? row.style.minHeight : "";
  if (row && frozen > 0) row.style.minHeight = frozen + "px";
  try {
    el.style.height = "auto";
    el.style.height = el.scrollHeight + "px";
  } finally {
    if (row && frozen > 0) row.style.minHeight = prevMin;
  }
}
