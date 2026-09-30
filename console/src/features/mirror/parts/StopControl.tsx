import { useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { useDismiss } from "../../../lib/useDismiss.ts";
import { useMenuRoving } from "../../../lib/useMenuRoving.ts";
import { placeFixed } from "../../../lib/placeFixed.ts";

/** The chat's stop control (ADR 0105).
 *
 *  The button is never disabled while a stop is in flight: the moment right after the first
 *  stop is exactly when a second one is needed (decision 2). A Managed session also gets a
 *  menu carrying "stop and discard the queue" (decision 3). The menu is there whether or not
 *  the chat shows a queue, because the server may already hold input the last poll missed;
 *  what the chat does see only decides whether it is emphasised. Terminal (CLI) has no menu:
 *  the queue lives inside the CLI and Agent Fleet cannot empty it (decision 6). */
export function StopControl({
  managed,
  queuedCount,
  onStop,
  onDiscard,
}: {
  managed: boolean;
  /** How many queued inputs the chat shows right now. */
  queuedCount: number;
  onStop: () => void;
  onDiscard: () => void;
}) {
  const [open, setOpen] = useState(false);
  const moreRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  useDismiss([moreRef, menuRef], open, () => setOpen(false));
  useMenuRoving(menuRef, open);
  useLayoutEffect(() => {
    const el = menuRef.current;
    const anchor = moreRef.current;
    if (!open || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    // Right-aligned under the caret: the control sits at the right edge of the chat.
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 2);
  });
  const hot = queuedCount > 0;
  return (
    <span className="mirror-stop-ctl">
      <button
        type="button"
        className="ghost mirror-stop"
        title={tr(managed ? "mirror.stop_run_managed" : "mirror.stop_run")}
        onClick={onStop}
      >
        <Icon name="debug-stop" /> {tr("chat.stop")}
      </button>
      {managed && (
        <button
          type="button"
          ref={moreRef}
          className={"ghost mirror-stop-more" + (hot ? " hot" : "")}
          title={tr("mirror.stop_more")}
          aria-label={tr("mirror.stop_more")}
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <Icon name="chevron-down" />
          {hot && <span className="mirror-stop-count">{queuedCount}</span>}
        </button>
      )}
      {open &&
        createPortal(
          <ul className="ui-menu mirror-stop-menu" ref={menuRef} role="menu" onMouseDown={(e) => e.stopPropagation()}>
            <li>
              <button
                type="button"
                role="menuitem"
                className={"ui-menu-item danger" + (hot ? " hot" : "")}
                title={tr("mirror.stop_discard_title")}
                onClick={() => {
                  setOpen(false);
                  onDiscard();
                }}
              >
                <Icon name="trash" /> {tr("mirror.stop_discard")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
    </span>
  );
}
