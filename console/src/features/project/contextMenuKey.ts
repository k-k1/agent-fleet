// Keyboard access to the rail rows' right-click menus. The Menu (ContextMenu) key and
// Shift+F10 are the platform-standard "open the context menu for the focused thing" keys;
// we honor both. For rows that own their menu as React state (repo / session), we synthesize
// a native `contextmenu` event on the focused element so the row's existing onContextMenu
// handler runs unchanged (React catches the bubbled native event). Files drive their menu
// from a single component-level state, so that view reads the key itself and calls setMenu.

/** True for the platform context-menu keys (Menu key / Shift+F10). */
export function isContextMenuKey(e: { key: string; shiftKey: boolean }): boolean {
  return e.key === "ContextMenu" || (e.shiftKey && e.key === "F10");
}

/** Screen coords to anchor a keyboard-opened menu at: just inside the row's leading edge,
 * near its bottom — where a menu drops naturally without covering the row. */
export function menuAnchor(el: Element): { x: number; y: number } {
  const r = el.getBoundingClientRect();
  return { x: Math.round(r.left + Math.min(24, r.width / 2)), y: Math.round(r.bottom - 4) };
}

/** Fire a native contextmenu event on `el` at its anchor, so the row's onContextMenu opens. */
export function synthContextMenu(el: HTMLElement): void {
  const { x, y } = menuAnchor(el);
  el.dispatchEvent(
    new MouseEvent("contextmenu", { bubbles: true, cancelable: true, view: window, button: 2, clientX: x, clientY: y }),
  );
}
