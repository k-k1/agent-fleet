import { useRef } from "react";
import type { Dispatch, ReactNode, SetStateAction } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT } from "../../lib/i18n/index.ts";

type OpenState = { open: boolean; setOpen: Dispatch<SetStateAction<boolean>> };

// The ⋯ overflow popover on a phone: the pane buttons the bar had no room for, then the stats
// and the port preview. The open state lives in the bar because the preview reads key off it.
export function MobileMore({
  open,
  setOpen,
  phoneFold,
  splitDownBtn,
  closeAllBtn,
  homeBtn,
  overviewBtn,
  imagegenBtn,
  statsBlock,
  previewPop,
}: OpenState & {
  phoneFold: number;
  splitDownBtn: ReactNode;
  closeAllBtn: ReactNode;
  homeBtn: ReactNode;
  overviewBtn: ReactNode;
  imagegenBtn: ReactNode;
  statsBlock: ReactNode;
  previewPop: ReactNode;
}) {
  const tr = useT();
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  return (
    <div className="ws-more" ref={ref}>
      <Button variant="ghost" className="ws-more-btn" title={tr("wsbar.more_title")} onClick={() => setOpen((o) => !o)}>
        <Icon name="ellipsis" />
      </Button>
      {open && (
        <div className="ws-more-pop">
          {/* The pane buttons the bar had no room for (the phone fold); a tap runs the action
              and closes the popover. In bar order, so the popover reads like the bar did. */}
          {phoneFold > 0 && (
            <div
              className="ws-more-actions"
              onClick={(e) => {
                // Closing unmounts the focused button; hand focus back to the trigger so a
                // keyboard user is not dropped on <body>. Only when focus is still here: an
                // action that moved it elsewhere (a pane) keeps it.
                if (e.currentTarget.contains(document.activeElement)) {
                  ref.current?.querySelector<HTMLElement>(".ws-more-btn")?.focus();
                }
                setOpen(false);
              }}
            >
              {phoneFold > 1 && splitDownBtn}
              {phoneFold > 1 && closeAllBtn}
              {homeBtn}
              {overviewBtn}
              {imagegenBtn}
            </div>
          )}
          {/* statsBlock is always a truthy Fragment (each chip hides itself by returning
              null), so emptiness cannot be tested here; the padding of an empty block is
              folded away by CSS :empty (wsbar.css). */}
          <div className="ws-more-stats">{statsBlock}</div>
          {previewPop}
        </div>
      )}
    </div>
  );
}

// The desktop ⋯ button, which folds the resource / profile chips and the port preview once the
// bar runs out of width (wsBarFold.ts). The chips in the bar stay mounted, hidden by CSS, so
// nothing re-fetches as the window crosses the boundary; the popover mounts its copies only
// while open.
export function DesktopMore({
  open,
  setOpen,
  foldMore,
  children,
}: OpenState & { foldMore: boolean; children: ReactNode }) {
  const tr = useT();
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  return (
    <div className="ws-more ws-more-desk" ref={ref}>
      <Button
        variant="ghost"
        className="ws-more-btn"
        title={tr("wsbar.more_title")}
        aria-label={tr("wsbar.more_title")}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="ellipsis" />
      </Button>
      {open && foldMore && <div className="ws-more-pop">{children}</div>}
    </div>
  );
}
