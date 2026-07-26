import { useEffect } from "react";
import type { RefObject } from "react";

// 横1行の列（返信サジェストのチップ行など）を掴んで左右にスクロールできるようにする。
// タッチのスワイプは overflow-x:auto のネイティブ挙動（慣性つき）に任せるので、ここで面倒を
// 見るのはマウスのドラッグだけ。ドラッグ直後の click は握りつぶす — チップを掴んで流しただけで
// 候補が差し込まれる/送信されるのを防ぐため。

const DRAG_THRESHOLD = 4; // これ以上動いたら「ドラッグ」とみなす（px）。以下ならただのクリック。

export function useDragScroll(ref: RefObject<HTMLElement | null>): void {
  useEffect(() => {
    const el = ref.current;
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
    return () => {
      el.removeEventListener("pointerdown", onDown);
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onUp);
      el.removeEventListener("click", onClick, true);
    };
  }, [ref]);
}
