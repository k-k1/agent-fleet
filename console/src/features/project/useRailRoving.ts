// useRailRoving — arrow-key navigation over the rail's repo/session tree (docs: keymap
// redesign P3a). The tree is nested RepoNodes with per-node fold state, so instead of
// centralizing that state we drive selection off the DOM: every navigable row carries
// data-rail-row, and querySelectorAll returns them in visual order (collapsed nodes'
// children simply aren't rendered). Additive — Tab access to the rows is untouched;
// this adds ↑↓ roving, ←→ expand/collapse, Enter/Space to open. The focused row shows
// the app-wide :focus-visible ring, so no selection state is threaded through the tree.
import { useCallback, useRef } from "react";
import type { KeyboardEvent as RKeyboardEvent } from "react";

export function useRailRoving() {
  const ref = useRef<HTMLUListElement>(null);
  const rows = (): HTMLElement[] => Array.from(ref.current?.querySelectorAll<HTMLElement>("[data-rail-row]") ?? []);
  const focusFirst = useCallback(() => rows()[0]?.focus(), []);

  // The fold caret for the node a row belongs to (repo rows only; sessions have none).
  const caretFor = (row: HTMLElement): HTMLElement | null =>
    row.closest(".proj-node")?.querySelector<HTMLElement>(":scope > .proj-node-head .proj-node-caret") ?? null;

  const onKeyDown = useCallback((e: RKeyboardEvent<HTMLElement>) => {
    const list = rows();
    if (list.length === 0) return;
    const cur = document.activeElement as HTMLElement;
    const idx = list.indexOf(cur);
    // Only rove when a rail row itself is focused. A widget opened from a rail row
    // (e.g. the LaunchModal, or any input inside it) is a React-tree descendant of
    // this <ul>, so even though the Modal portals to <body>, React bubbles its
    // synthetic keydowns up to this handler. Without this guard Home/End/arrows would
    // yank focus out of the modal into the tree while the user is typing.
    if (idx < 0) return;
    const move = (to: number) => {
      const t = list[Math.max(0, Math.min(to, list.length - 1))];
      if (t) {
        e.preventDefault();
        t.focus();
      }
    };
    switch (e.key) {
      case "ArrowDown":
        return move(idx + 1);
      case "ArrowUp":
        return move(idx - 1);
      case "Home":
        return move(0);
      case "End":
        return move(list.length - 1);
      case "Enter":
      case " ":
        e.preventDefault();
        cur.click();
        return;
      case "ArrowRight": {
        const isRepo = cur.classList.contains("repo-card");
        const caret = isRepo ? caretFor(cur) : null;
        // Collapsed repo → expand; otherwise step to the next (child) row.
        if (caret && caret.getAttribute("aria-expanded") !== "true") {
          e.preventDefault();
          caret.click();
        } else {
          move(idx + 1);
        }
        return;
      }
      case "ArrowLeft": {
        const isRepo = cur.classList.contains("repo-card");
        const caret = isRepo ? caretFor(cur) : null;
        if (caret && caret.getAttribute("aria-expanded") === "true") {
          // Expanded repo → collapse.
          e.preventDefault();
          caret.click();
        } else if (!isRepo) {
          // Session row → jump to its parent repo row.
          const parent = cur.closest(".proj-node")?.querySelector<HTMLElement>(":scope > .proj-node-head [data-rail-row]");
          if (parent) {
            e.preventDefault();
            parent.focus();
          }
        } else {
          move(idx - 1);
        }
        return;
      }
    }
  }, []);

  return { ref, onKeyDown, focusFirst };
}
