// The menu behind a category head's ⋯ button, right-click and Menu / Shift+F10 key. It holds the
// actions the head used to show as a row of buttons (the rail is too narrow for them next to the
// name). An action that cannot act now stays listed, disabled, with its reason on a second line.
import { useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { useT } from "../../lib/i18n/index.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { Icon } from "../../ui/Icon.tsx";
import type { MemoQueue } from "../../types/memo.ts";
import { useRunNow, type RunNowResult } from "./runNow.ts";

export interface CatHeadMenuAt {
  x: number;
  y: number;
  /** Set when opened from the ⋯ button: the menu hangs under it, right-aligned. */
  anchor?: DOMRect;
}

interface Props {
  at: CatHeadMenuAt;
  categoryId: string;
  isQueue: boolean;
  queue: MemoQueue | undefined;
  /** Memos a Send would take; 0 hides Send. */
  sendCount: number;
  /** Why the settings dialog cannot open now ("" when it can). */
  settingsNote: string;
  onClose: () => void;
  onSend: () => void;
  onRunResult: (r: RunNowResult) => void;
  onHistory: () => void;
  onSettings: () => void;
  onRename: () => void;
  onDelete: () => void;
}

export function CatHeadMenu(p: Props) {
  const tr = useT();
  const ref = useRef<HTMLUListElement>(null);
  const run = useRunNow(p.categoryId, p.queue);
  // Where focus was when the menu opened (the ⋯ button, for a keyboard user). A pick that leaves the menu's own
  // item focused hands it back on unmount; a rename field or dialog opened by the pick takes focus afterwards.
  const opener = useRef<Element | null>(typeof document === "undefined" ? null : document.activeElement);
  useLayoutEffect(
    () => () => {
      const el = opener.current;
      const cur = document.activeElement;
      if (el instanceof HTMLElement && el !== document.body && el.isConnected && (!cur || cur === document.body || ref.current?.contains(cur))) el.focus();
    },
    [],
  );
  useDismiss(ref, true, p.onClose);
  useMenuRoving(ref, true);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const bounds = el.closest<HTMLElement>(".app-rail") ?? document.querySelector<HTMLElement>(".app-rail");
    if (p.at.anchor) placeFixed(el, p.at.anchor.right - el.offsetWidth, p.at.anchor.bottom + 2, bounds);
    else placeFixed(el, p.at.x, p.at.y, bounds);
  });

  const item = (cls: string, icon: string, label: string, onPick: () => void, reason = "", danger = false, off = reason !== "") => (
    <li>
      <button
        type="button"
        className={"ui-menu-item memo-cat-menu-item " + cls + (danger ? " danger" : "")}
        role="menuitem"
        // aria-disabled, not disabled: the reason stays reachable by keyboard.
        aria-disabled={off ? true : undefined}
        onClick={() => {
          if (off) return;
          p.onClose();
          onPick();
        }}
      >
        <Icon name={icon} /> {label}
        {reason && <span className="memo-cat-menu-why">{reason}</span>}
      </button>
    </li>
  );

  const runOff = run.reason !== "" || run.busy;
  return createPortal(
    <ul className="ui-menu memo-cat-menu" ref={ref} style={{ left: p.at.x, top: p.at.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
      {p.sendCount > 0 && item("memo-cat-menu-send", "send", `${tr("common.send")} (${p.sendCount})`, p.onSend)}
      {p.isQueue &&
        item(
          "memo-cat-run",
          "play",
          tr("memo.cat_menu_run"),
          () => void run.press().then((r) => r && p.onRunResult(r)),
          run.reason,
          false,
          runOff,
        )}
      {p.isQueue && item("memo-cat-menu-act", "history", tr("memo.autorun_deliveries"), p.onHistory)}
      {item("memo-cat-menu-act", "settings-gear", tr("memo.autorun_settings"), p.onSettings, p.settingsNote)}
      {item("memo-cat-rename", "edit", tr("memo.rename_category_item"), p.onRename)}
      <li className="ui-menu-sep" aria-hidden="true" />
      {item("memo-cat-menu-del", "trash", tr("memo.delete_category"), p.onDelete, "", true)}
    </ul>,
    document.body,
  );
}
