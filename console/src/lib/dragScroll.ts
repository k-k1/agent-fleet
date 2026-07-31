import { useCallback, useEffect, useState } from "react";
import type { RefObject } from "react";

// 横1行の列（返信サジェストのチップ行など）を掴んで左右にスクロールできるようにする。
// タッチのスワイプは overflow-x:auto のネイティブ挙動（慣性つき）に任せるので、ここで面倒を
// 見るのはマウスのドラッグと縦ホイールだけ。ドラッグ直後の click は握りつぶす — チップを掴んで
// 流しただけで候補が差し込まれる/送信されるのを防ぐため。

const DRAG_THRESHOLD = 4; // これ以上動いたら「ドラッグ」とみなす（px）。以下ならただのクリック。
const LINE_PX = 16; // deltaMode=line の 1 行を px 換算（ホイール1ノッチ＝3行相当）

type WheelLike = Pick<WheelEvent, "deltaX" | "deltaY" | "deltaMode" | "ctrlKey">;
type ScrollBox = Pick<HTMLElement, "scrollLeft" | "scrollWidth" | "clientWidth">;

// 縦ホイールを横スクロール量（px）に翻訳する。0 を返したら「この列では扱わない」＝
// preventDefault せずに親（会話ログ/ペイン）へ流す、の意。
//   - あふれていない行は掴むものが無いので素通し
//   - 横方向が優勢（トラックパッドの横スワイプ）はネイティブの慣性に任せる
//   - Ctrl+ホイールはブラウザのピンチズーム
//   - 端に着いていて動かせない向きは奪わない（overscroll-behavior だけでは止まって見えるため）
export function wheelScrollDelta(e: WheelLike, el: ScrollBox): number {
  const max = el.scrollWidth - el.clientWidth;
  if (max <= 0) return 0;
  if (e.ctrlKey) return 0;
  if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) return 0;
  const raw = e.deltaMode === 1 ? e.deltaY * LINE_PX : e.deltaMode === 2 ? e.deltaY * el.clientWidth : e.deltaY;
  if (!raw) return 0;
  const clamped = Math.max(-el.scrollLeft, Math.min(max - el.scrollLeft, raw));
  return Math.abs(clamped) < 1 ? 0 : clamped;
}

// 返す関数を対象要素の `ref` に渡す（`<div ref={useDragScroll(rowRef)}>`）。
//
// ★ref オブジェクトを見るだけの effect にしてはいけない。この行は条件付きレンダーで、
// ストリーミング中（チャット）や AUQ/plan のロック中（ミラー）は DOM から消えてまた戻る。
// ref への代入は再レンダーも effect も起こさないので、effect は「最初に見えた要素」に
// リスナーを付けたきりになり、初回にその要素が無ければ二度と付かず、戻ってきた新しい
// 要素にも付かない（＝ホイール横スクロールが黙って死ぬ）。要素を state で持ち、
// コールバック ref で出入りを検知して付け替える。
// 呼び出し側が同じ要素を querySelector 等で使えるよう、渡された ref にも実体を書く。
export function useDragScroll<T extends HTMLElement>(ref?: RefObject<T | null>): (el: T | null) => void {
  const [node, setNode] = useState<T | null>(null);
  const attach = useCallback(
    (el: T | null) => {
      if (ref) ref.current = el;
      setNode(el);
    },
    [ref],
  );

  useEffect(() => {
    const el = node;
    if (!el) return;
    let startX = 0;
    let startLeft = 0;
    let dragging = false;
    let moved = false;

    const onDown = (e: PointerEvent) => {
      if (e.pointerType !== "mouse" || e.button !== 0) return; // タッチ/ペンはネイティブに任せる
      dragging = true;
      moved = false; // 新しい操作の始まり（前回のドラッグ痕を残さない）
      startX = e.clientX;
      startLeft = el.scrollLeft;
    };
    const onMove = (e: PointerEvent) => {
      if (!dragging) return;
      const dx = e.clientX - startX;
      if (!moved && Math.abs(dx) < DRAG_THRESHOLD) return; // 微動はクリックとして通す
      moved = true;
      el.setPointerCapture?.(e.pointerId); // 子ボタンの上を通っても追従させる
      el.scrollLeft = startLeft - dx;
      e.preventDefault(); // ドラッグ中のテキスト選択を止める
    };
    const onUp = () => {
      dragging = false;
    };
    // 縦ホイールで左右に流す（狭いペインではチップ行があふれるのが普通なので、掴まずに送れる手段を出す）。
    const onWheel = (e: WheelEvent) => {
      const dx = wheelScrollDelta(e, el);
      if (!dx) return; // 扱わない分は親のスクロールに任せる
      el.scrollLeft += dx;
      e.preventDefault();
    };
    // ドラッグ後に届く click（＝チップの差し込み）を子へ渡す前に捨てる。capture で先に受ける。
    const onClick = (e: MouseEvent) => {
      if (!moved) return;
      moved = false;
      e.preventDefault();
      e.stopPropagation();
    };

    el.addEventListener("pointerdown", onDown);
    el.addEventListener("pointermove", onMove);
    el.addEventListener("pointerup", onUp);
    el.addEventListener("pointercancel", onUp);
    el.addEventListener("click", onClick, true);
    el.addEventListener("wheel", onWheel, { passive: false }); // preventDefault するので passive 不可
    return () => {
      el.removeEventListener("pointerdown", onDown);
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onUp);
      el.removeEventListener("click", onClick, true);
      el.removeEventListener("wheel", onWheel);
    };
  }, [node]);

  return attach;
}
