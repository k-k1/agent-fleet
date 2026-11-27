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
    const doc = menu.ownerDocument;
    menu.setAttribute("role", "menu");
    const items = () =>
      Array.from(menu.querySelectorAll<HTMLElement>(".ui-menu-item")).filter(
        (el) => !(el as HTMLButtonElement).disabled && el.offsetParent !== null,
      );
    const initial = items();
    // An item that declares its own role (a checkable one is a menuitemcheckbox) keeps it.
    for (const el of initial) if (!el.hasAttribute("role")) el.setAttribute("role", "menuitem");
    let focused: { item: HTMLElement; index: number } | undefined;
    const onFocusIn = (e: FocusEvent) => {
      const item = e.target as HTMLElement;
      const index = items().indexOf(item);
      focused = index < 0 ? undefined : { item, index };
    };
    const onFocusOut = (e: FocusEvent) => {
      const leaving = focused;
      if (!leaving || e.target !== leaving.item) return;
      if (e.relatedTarget) {
        focused = undefined;
      } else {
        // Removing a focused node can fire focusout before it leaves the menu.
        queueMicrotask(() => {
          if (focused === leaving && menu.contains(leaving.item) && doc.activeElement !== leaving.item) {
            focused = undefined;
          }
        });
      }
    };
    // Polling can remove a focused item without delivering a focus event to the menu.
    const observer = new MutationObserver(() => {
      if (!focused || !menu.isConnected) return;
      const list = items();
      if (menu.contains(focused.item)) {
        const index = list.indexOf(focused.item);
        if (index >= 0) focused.index = index;
        return;
      }
      const { index } = focused;
      focused = undefined;
      if (doc.activeElement !== doc.body) return;
      list[Math.min(index, list.length - 1)]?.focus({ preventScroll: true });
    });
    observer.observe(menu, { childList: true, subtree: true });
    menu.addEventListener("focusin", onFocusIn);
    menu.addEventListener("focusout", onFocusOut);
    initial[0]?.focus();

    const onKey = (e: KeyboardEvent) => {
      const list = items();
      if (list.length === 0) return;
      const idx = list.indexOf(doc.activeElement as HTMLElement);
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
    return () => {
      observer.disconnect();
      menu.removeEventListener("focusin", onFocusIn);
      menu.removeEventListener("focusout", onFocusOut);
      menu.removeEventListener("keydown", onKey);
    };
  }, [open, menuRef]);
}
