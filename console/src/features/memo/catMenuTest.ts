// Test helper: the category head keeps only ⋯; its actions live in a menu. `headMenuItem` opens that menu
// (synchronously, so existing `click(find…)` call sites keep their shape) and returns the item by class
// or by the start of its label.
import { act } from "react";

export function headMenuItem(by: { cls?: string; label?: string }): HTMLElement {
  let menu = document.querySelector(".memo-cat-menu");
  if (!menu) {
    const more = document.querySelector<HTMLElement>(".memo-cat-more");
    if (!more) throw new Error("no category head ⋯ button");
    act(() => {
      more.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    menu = document.querySelector(".memo-cat-menu");
  }
  const items = [...(menu?.querySelectorAll<HTMLElement>(".ui-menu-item") ?? [])];
  const it = items.find((e) => (by.cls ? e.classList.contains(by.cls) : false) || (by.label ? (e.textContent ?? "").trim().startsWith(by.label) : false));
  if (!it) throw new Error(`no head menu item ${by.cls ?? by.label}`);
  return it;
}
