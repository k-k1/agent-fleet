// The activity bar's context menu (ADR 0142 decision 11): one checkable row per view, which turns
// that view's icon on or off, and a reset. A row that cannot be switched stays in the list, greyed
// out with the reason written beside it, so the reason is readable by keyboard and on a phone.
import { useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";
import type { RailView } from "../core/store/leftRail.ts";
import { Icon } from "../ui/Icon.tsx";
import { placeFixed } from "../lib/placeFixed.ts";
import { useDismiss } from "../lib/useDismiss.ts";
import { useMenuRoving } from "../lib/useMenuRoving.ts";
import { useT } from "../lib/i18n/index.ts";
import type { MsgKey } from "../lib/i18n/index.ts";
import type { MenuEntry } from "./activity/visibility.ts";

export interface BarMenuAnchor {
  x: number;
  y: number;
}

export interface BarMenuRow extends MenuEntry {
  icon: string;
  name: string;
  /** What waits on the member in this view although its icon is off; empty otherwise. */
  waiting: string;
}

const LOCK_TEXT: Record<NonNullable<MenuEntry["lock"]>, MsgKey> = {
  current: "rail.lock.current",
  last: "rail.lock.last",
  unavailable: "rail.lock.no_scheduler",
};

export function ActivityBarMenu({
  anchor,
  rows,
  canReset,
  onToggle,
  onReset,
  onClose,
}: {
  anchor: BarMenuAnchor;
  rows: BarMenuRow[];
  canReset: boolean;
  onToggle: (id: RailView, on: boolean) => void;
  onReset: () => void;
  onClose: () => void;
}) {
  const tr = useT();
  const ref = useRef<HTMLUListElement>(null);
  useDismiss([ref], true, onClose);
  useMenuRoving(ref, true);
  // Every render, before paint: a row's text changing width must not push the menu off screen.
  useLayoutEffect(() => {
    if (ref.current) placeFixed(ref.current, anchor.x, anchor.y);
  });
  return createPortal(
    <ul
      className="ui-menu actbar-menu"
      ref={ref}
      style={{ left: anchor.x, top: anchor.y }}
      aria-label={tr("rail.menu_title")}
      onMouseDown={(e) => e.stopPropagation()}
    >
      <li className="ui-menu-caption" role="presentation">
        {tr("rail.menu_title")}
      </li>
      {rows.map((r) => (
        <li key={r.id} role="none">
          <button
            type="button"
            role="menuitemcheckbox"
            aria-checked={r.checked}
            aria-disabled={r.lock ? true : undefined}
            className="ui-menu-item actbar-menu-item"
            data-menu-view={r.id}
            onClick={() => {
              if (!r.lock) onToggle(r.id, !r.checked);
            }}
          >
            <Icon name="check" className={r.checked ? "wset-check" : "wset-check wset-check--off"} />
            <Icon name={r.icon} />
            <span className="actbar-menu-name">{r.name}</span>
            {r.waiting && <span className="actbar-menu-waiting">{r.waiting}</span>}
            {r.lock && <span className="actbar-menu-reason">{tr(LOCK_TEXT[r.lock])}</span>}
          </button>
        </li>
      ))}
      <li className="ui-menu-sep" role="separator" />
      <li role="none">
        <button
          type="button"
          role="menuitem"
          aria-disabled={canReset ? undefined : true}
          className="ui-menu-item actbar-menu-item"
          data-menu-reset=""
          onClick={() => {
            if (canReset) onReset();
          }}
        >
          <Icon name="discard" />
          <span className="actbar-menu-name">{tr("rail.menu_reset")}</span>
        </button>
      </li>
    </ul>,
    document.body,
  );
}
