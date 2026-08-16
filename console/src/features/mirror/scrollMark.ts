// ミラーの表示位置をセッション単位で覚え、戻ってきたときに同じ内容へ復元する。
//
// なぜ px（scrollTop）をそのまま保存しないか: transcript の高さはほぼ全部が遅れて確定する
// （MarkdownView が innerHTML を書くのは passive effect、そのあと highlight → math →
// mermaid → 画像 decode → web フォント）。さらに再訪時は tail ウィンドウを取り直すので、
// 同じ px が同じ内容を指す保証がない。そこでターン（[data-turn-idx]）をアンカーにし、
// 「どのターンの上端が、ビューポート上端から何 px の位置にいたか」を保存する。
//
// ここは純粋な DOM 関数だけを置く（ストアも含めてストア/React を import しない）。スマホの
// セッション持ち替えは実機でしか触れない一方、位置計算そのものは jsdom で全パターン回せる。

/** 保存した表示位置。atBottom なら復元せず末尾に着地させる（末尾追従を壊さないため）。 */
export interface ScrollMark {
  /** 離脱時に末尾追従していたか。true = 位置ではなく「最新を見ていた」が意図。 */
  atBottom: boolean;
  /** ビューポート上端に掛かっていたターンの idx。 */
  idx: number;
  /** そのターンの上端 − ビューポート上端（px）。ターンの途中まで送っていれば負。 */
  offset: number;
}

/** 楽観エコー / キュー済みプロンプトの合成ターン（MirrorView が 1e9 以降を振る）。戻って
 * きたときには実ターンに置き換わっていて idx が残らないので、アンカーには使わない。 */
const SYNTHETIC_IDX = 1e9;

/** セッション名 → 最後に見ていた位置。タブが生きている間だけ（リロードで消える＝次回は
 * 末尾着地）。echoStore と同じ、モジュールスコープの持ち物。 */
const marks = new Map<string, ScrollMark>();

export function saveMark(session: string, mark: ScrollMark | null): void {
  if (!session) return;
  if (mark) marks.set(session, mark);
  else marks.delete(session);
}

export function loadMark(session: string): ScrollMark | null {
  return (session && marks.get(session)) || null;
}

/** テスト用。 */
export function clearMarks(): void {
  marks.clear();
}

/** いまの表示位置を採る。スクロール容器 el の上端に掛かっている最初のターンが基準。
 * 掛かるターンが無い（空 transcript）／合成ターンしかないときは null＝末尾着地に任せる。 */
export function captureMark(el: HTMLElement | null, atBottom: boolean): ScrollMark | null {
  if (!el) return null;
  const top = el.getBoundingClientRect().top;
  const turns = el.querySelectorAll<HTMLElement>("[data-turn-idx]");
  for (const turn of Array.from(turns)) {
    const r = turn.getBoundingClientRect();
    // 上端より下に「はみ出している」最初のターン = 画面の一番上に見えているターン。
    if (r.bottom <= top + 1) continue;
    const idx = Number(turn.getAttribute("data-turn-idx"));
    if (!Number.isFinite(idx) || idx >= SYNTHETIC_IDX) return null;
    return { atBottom, idx, offset: Math.round(r.top - top) };
  }
  return null;
}

/** ターン idx の上端が、ビューポート上端から offset px の位置に来る scrollTop。まだその
 * ターンが載っていなければ null（＝tail ウィンドウの外。呼び手は末尾へ落とす）。 */
export function scrollTopForTurn(el: HTMLElement | null, idx: number, offset = 0): number | null {
  if (!el) return null;
  const turn = el.querySelector<HTMLElement>(`[data-turn-idx="${idx}"]`);
  if (!turn) return null;
  const delta = turn.getBoundingClientRect().top - el.getBoundingClientRect().top - offset;
  const max = Math.max(0, el.scrollHeight - el.clientHeight);
  return Math.min(max, Math.max(0, el.scrollTop + delta));
}

/** 保存位置へ戻す。戻せたら true（アンカーのターンが無ければ false）。 */
export function applyMark(el: HTMLElement | null, mark: ScrollMark): boolean {
  const top = scrollTopForTurn(el, mark.idx, mark.offset);
  if (top === null || !el) return false;
  el.scrollTop = top;
  return true;
}
