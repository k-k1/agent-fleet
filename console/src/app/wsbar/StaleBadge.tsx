import { useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT } from "../../lib/i18n/index.ts";

// "Restart required" — the backend moved on while this container kept running. Sits
// right of the state chip (and dots the power button next to it) because the
// fix IS the power toggle; the popover explains the cost and offers the
// stop→start in one click. Label folds away on a phone, tap target stays.
// Mounted only while the bar says the workspace is stale, so unmounting is what resets `open`.
export function StaleBadge({ onRestart }: { onRestart: () => Promise<void> }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  return (
    <div className="ws-stale" ref={ref}>
      <Button
        className="ws-stale-pill"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        title={tr("wsbar.stale.title")}
      >
        <Icon name="refresh" />
        <span className="lbl">{tr("wsbar.stale.badge")}</span>
      </Button>
      {open && (
        <div className="ws-stale-pop">
          <div className="ws-stale-txt">{tr("wsbar.stale.body")}</div>
          <div className="ws-stale-actions">
            <Button
              className="ws-stale-go"
              onClick={() => {
                setOpen(false);
                void onRestart();
              }}
            >
              {tr("wsbar.stale.restart")}
            </Button>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {tr("wsbar.stale.later")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
