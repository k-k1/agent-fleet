// Context menu on a reply-suggestion chip (right click / long press / Menu key). MirrorView and
// ChatView share the behaviour; only the chip row's appearance lives in their own CSS.
//
// Delete and pin are rare actions, so they are collected into one menu reachable from every
// input type rather than an in-chip close button (which only works under @media (hover: hover),
// is mistap-prone on touch, and costs 20px of permanent chip padding):
//   - contextmenu event ... mouse right click
//   - long press (500ms) ... touch. Android may also fire a native contextmenu for the same
//     press; either order opens the same menu, and the timer is cancelled once it is open.
//   - Menu key / Shift+F10 ... keyboard, via the same contextMenuKey.ts convention as the rail.
// When opened by long press, the click fired on lift (which would insert into the composer) is
// swallowed exactly once.
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, MouseEvent as RMouseEvent, TouchEvent as RTouchEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useT } from "../../lib/i18n/index.ts";
import { isContextMenuKey, menuAnchor } from "../project/contextMenuKey.ts";

// How long a press must last to count as a long press: 500ms, matching the browser's own
// long-press (selection / callout).
const LONG_PRESS_MS = 500;
// Moving further than this makes it a horizontal scroll (swipe) of the chip row, not a long press.
const MOVE_TOL = 10;

export type ChipMenuState = { text: string; llm: boolean; x: number; y: number };

export type ChipMenuHandlers = {
  onContextMenu: (e: RMouseEvent) => void;
  onMouseDown: () => void;
  onTouchStart: (e: RTouchEvent) => void;
  onTouchMove: (e: RTouchEvent) => void;
  onTouchEnd: (e: RTouchEvent) => void;
  onTouchCancel: () => void;
};

export type ChipMenu = {
  /** The open menu, or null when closed. Pass it to <SuggestChipMenu menu={…}>. */
  menu: ChipMenuState | null;
  close: () => void;
  /** The event handlers to spread onto the chip <button>. */
  chipProps: (text: string, llm: boolean) => ChipMenuHandlers;
  /** Handles the Menu key / Shift+F10 from the chip onKeyDown; returns true when handled. */
  onKeyDown: (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => boolean;
  /** Whether this click came from the long press that opened the menu. True means do not insert;
   *  the flag is consumed by the call. */
  clickSwallowed: () => boolean;
};

export function useChipMenu(): ChipMenu {
  const [menu, setMenu] = useState<ChipMenuState | null>(null);
  const timer = useRef<number | null>(null);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const swallow = useRef(false);

  const cancelTimer = () => {
    if (timer.current !== null) {
      window.clearTimeout(timer.current);
      timer.current = null;
    }
    origin.current = null;
  };

  const open = (text: string, llm: boolean, x: number, y: number) => {
    cancelTimer();
    setMenu({ text, llm, x, y });
  };

  const chipProps = (text: string, llm: boolean): ChipMenuHandlers => ({
    onContextMenu: (e) => {
      e.preventDefault(); // suppress the browser's own menu and the iOS callout
      // On Android a long press may deliver this native contextmenu first. Only a touch-derived
      // one needs the lift click swallowed, hence the pointerType check: a mouse right click is
      // not followed by a click, so setting the flag here would eat the next left click.
      if ((e.nativeEvent as PointerEvent).pointerType === "touch") swallow.current = true;
      open(text, llm, e.clientX, e.clientY);
    },
    // Clear a stale swallow flag as soon as a mouse interaction starts: no click follows a right
    // click, so leaving it set would eat the next left click.
    onMouseDown: () => {
      swallow.current = false;
    },
    onTouchStart: (e) => {
      swallow.current = false;
      cancelTimer();
      const t = e.touches[0];
      if (!t || e.touches.length > 1) return;
      origin.current = { x: t.clientX, y: t.clientY };
      const { clientX, clientY } = t;
      timer.current = window.setTimeout(() => {
        swallow.current = true; // drop the click fired on lift, which would insert the text
        open(text, llm, clientX, clientY);
      }, LONG_PRESS_MS);
    },
    onTouchMove: (e) => {
      const t = e.touches[0];
      const o = origin.current;
      if (!t || !o) return;
      if (Math.abs(t.clientX - o.x) > MOVE_TOL || Math.abs(t.clientY - o.y) > MOVE_TOL) cancelTimer();
    },
    // Once the long press has fired, preventDefault on touchend so no compatibility click /
    // mousedown is synthesised. A synthesised mousedown counts as an outside press for useDismiss
    // and would close the menu the moment the finger lifts. The swallow flag is kept as a fallback
    // for browsers that ignore this; the next touchstart / mousedown always clears it, so it can
    // never linger and eat a click.
    onTouchEnd: (e) => {
      if (swallow.current && e.cancelable) e.preventDefault();
      cancelTimer();
    },
    onTouchCancel: cancelTimer,
  });

  const onKeyDown = (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean): boolean => {
    if (!isContextMenuKey(e)) return false;
    e.preventDefault();
    const a = menuAnchor(e.currentTarget);
    open(text, llm, a.x, a.y);
    return true;
  };

  return {
    menu,
    close: () => {
      cancelTimer();
      setMenu(null);
    },
    chipProps,
    onKeyDown,
    clickSwallowed: () => {
      const s = swallow.current;
      swallow.current = false;
      return s;
    },
  };
}

interface SuggestChipMenuProps {
  menu: ChipMenuState;
  /** Whether this suggestion is pinned; switches the label and the icon. */
  pinned: boolean;
  onClose: () => void;
  onTogglePin: (text: string) => void;
  onForget: (text: string, llm: boolean) => void;
}

/** The chip menu itself: fixed at the pointer position, clamped to stay on screen. */
export function SuggestChipMenu({ menu, pinned, onClose, onTogglePin, onForget }: SuggestChipMenuProps) {
  const tr = useT();
  const ref = useRef<HTMLUListElement>(null);
  useDismiss([ref], true, onClose);
  useMenuRoving(ref, true);
  // Reposition on every render, before paint: the parent re-renders on a poll and each time
  // restores the raw coordinates as an inline style, so a one-shot clamp would be pushed off screen.
  useLayoutEffect(() => {
    if (ref.current) placeFixed(ref.current, menu.x, menu.y);
  });
  const run = (fn: () => void) => {
    onClose();
    fn();
  };
  return createPortal(
    <ul
      className="ui-menu suggest-menu"
      ref={ref}
      style={{ left: menu.x, top: menu.y }}
      role="menu"
      onMouseDown={(e) => e.stopPropagation()}
    >
      <li>
        <button type="button" className="ui-menu-item" onClick={() => run(() => onTogglePin(menu.text))}>
          <Icon name={pinned ? "pinned" : "pin"} />
          {pinned ? tr("mirror.suggest_unpin") : tr("mirror.suggest_pin")}
        </button>
      </li>
      <li className="ui-menu-sep" />
      <li>
        <button type="button" className="ui-menu-item danger" onClick={() => run(() => onForget(menu.text, menu.llm))}>
          <Icon name="trash" />
          {tr("mirror.suggest_forget_item")}
        </button>
      </li>
    </ul>,
    document.body,
  );
}
