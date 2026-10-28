import { useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT } from "../../lib/i18n/index.ts";

// SlotMoveNotice tells a member that the next start moves their workspace to a new slot
// (ecs-ec2 replacement reservation, #1473). There is nothing for them to do but know why that
// start takes longer, so it is a notice with no action, styled like the restart-needed pill.
export function SlotMoveNotice() {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  return (
    <div className="ws-stale ws-slotmove" ref={ref}>
      <Button className="ws-stale-pill" onClick={() => setOpen((o) => !o)} aria-expanded={open} title={tr("wsbar.slotmove.title")}>
        <Icon name="info" />
        <span className="lbl">{tr("wsbar.slotmove.badge")}</span>
      </Button>
      {open && (
        <div className="ws-stale-pop">
          <div className="ws-stale-txt">{tr("wsbar.slotmove.body")}</div>
          <div className="ws-stale-actions">
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {tr("wsbar.slotmove.ok")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
