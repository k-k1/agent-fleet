// useMenuRoving — keyboard navigation for a context menu (.ui-menu with .ui-menu-item
// buttons), docs: keymap redesign P3. On open it focuses the first item; ↑/↓ move
// (wrapping), Home/End jump to ends, and Enter/Space activate natively (items are
// <button>). It also stamps role=menu / role=menuitem so assistive tech announces the
// menu correctly — done here so the ~5 ad-hoc menus don't each repeat the markup.
// Pair with useDismiss, which these menus already use for Esc + outside-click close.
import { useEffect } from "react";
import type { RefObject } from "react";

export function useMenuRoving(menuRef: RefObject<HTMLElement | null>, open: boolean): void {
  useEffect(() => {
    if (!open) return;
    const root = menuRef.current;
    if (!root) return;
    // The ref may point at the .ui-menu itself or at a wrapper that contains it.
    const menu = root.classList.contains("ui-menu") ? root : root.querySelector<HTMLElement>(".ui-menu");
    if (!menu) return;
    menu.setAttribute("role", "menu");
    const items = () =>
      Array.from(menu.querySelectorAll<HTMLElement>(".ui-menu-item")).filter(
        (el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null,
      );
    const initial = items();
    for (const el of initial) el.setAttribute("role", "menuitem");
    initial[0]?.focus();

    const onKey = (e: KeyboardEvent) => {
      const list = items();
      if (list.length === 0) return;
      const idx = list.indexOf(document.activeElement as HTMLElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        list[idx < 0 ? 0 : (idx + 1) % list.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        list[idx < 0 ? list.length - 1 : (idx - 1 + list.length) % list.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        list[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        list[list.length - 1].focus();
      }
    };
    menu.addEventListener("keydown", onKey);
    return () => menu.removeEventListener("keydown", onKey);
  }, [open, menuRef]);
}
