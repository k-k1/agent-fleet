import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { useDismiss } from "../../../lib/useDismiss.ts";
import { useMenuRoving } from "../../../lib/useMenuRoving.ts";
import { placeFixed } from "../../../lib/placeFixed.ts";

/**
 * SplitSend is Send with a small attached chevron while a turn runs. The chevron opens a menu
 * with the one alternative to sending — hold the draft in the pre-send queue (#1083). The menu is
 * portalled to the body and fixed-positioned, so neither the composer's nor the pane's overflow
 * clips it; it opens upward when there is no room below (the composer sits at the bottom).
 */
export function SplitSend({
  disabled,
  onSend,
  onQueue,
}: {
  disabled: boolean;
  onSend: () => void;
  onQueue: () => void;
}) {
  const [open, setOpen] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const refocus = useRef(false);
  useDismiss([moreRef, menuRef], open, () => setOpen(false));
  useMenuRoving(menuRef, open);
  useLayoutEffect(() => {
    const el = menuRef.current;
    const anchor = moreRef.current;
    if (!open || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    const fitsBelow = a.bottom + 2 + el.offsetHeight + 8 <= window.innerHeight;
    // Right-aligned to the chevron: the column sits at the right edge of the pane.
    placeFixed(el, a.right - el.offsetWidth, fitsBelow ? a.bottom + 2 : a.top - el.offsetHeight - 2);
  });
  // A menu that closes by Esc or a pick hands focus back to the chevron; an outside press keeps
  // the focus wherever the press put it.
  const close = (back: boolean) => {
    refocus.current = back;
    setOpen(false);
  };
  useLayoutEffect(() => {
    if (!open && refocus.current) {
      refocus.current = false;
      moreRef.current?.focus();
    }
  }, [open]);
  // Disabling the chevron while its menu is open (the draft was cleared) closes the menu.
  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);
  return (
    <div className="mirror-send-split">
      <button type="button" className="btn primary mirror-send" disabled={disabled} onClick={onSend} title={tr("chat.send")}>
        <Icon name="send" />
      </button>
      <button
        type="button"
        ref={moreRef}
        className="btn primary mirror-send-more"
        disabled={disabled}
        title={tr("mirror.queue_add_hint")}
        aria-label={tr("mirror.queue_add")}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            setOpen(true);
          }
        }}
      >
        <Icon name="chevron-down" />
      </button>
      {open &&
        createPortal(
          <ul
            className="ui-menu mirror-send-menu"
            ref={menuRef}
            role="menu"
            onMouseDown={(e) => e.stopPropagation()}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.stopPropagation();
                close(true);
              }
            }}
            onBlur={(e) => {
              // Focus left the menu for somewhere that is not the chevron: put it away.
              const to = e.relatedTarget as Node | null;
              if (to && !menuRef.current?.contains(to) && to !== moreRef.current) close(false);
            }}
          >
            <li>
              <button
                type="button"
                role="menuitem"
                className="ui-menu-item"
                title={tr("mirror.queue_add_hint")}
                onClick={() => {
                  close(true);
                  onQueue();
                }}
              >
                <span>{tr("mirror.queue_add")}</span>
                <kbd className="mirror-send-menu-key">Alt+Enter</kbd>
              </button>
            </li>
          </ul>,
          document.body,
        )}
    </div>
  );
}
