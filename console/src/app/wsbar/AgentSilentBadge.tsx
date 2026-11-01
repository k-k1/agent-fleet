import { useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT } from "../../lib/i18n/index.ts";

// "Not responding" — the container is up but the Agent inside it has not answered the Control
// Plane for minutes (#2518; typically memory starvation). Nothing the member does in the
// Console will work until it is stopped and started again, so the popover says so and offers
// the stop (which confirms, as the power button does). Mounted only while the CP says so.
export function AgentSilentBadge({ onStop }: { onStop: () => Promise<void> }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));
  return (
    <div className="ws-stale ws-agentsilent" ref={ref}>
      <Button className="ws-stale-pill" onClick={() => setOpen((o) => !o)} aria-expanded={open} title={tr("wsbar.agentsilent.title")}>
        <Icon name="warning" />
        <span className="lbl">{tr("wsbar.agentsilent.badge")}</span>
      </Button>
      {open && (
        <div className="ws-stale-pop">
          <div className="ws-stale-txt">{tr("wsbar.agentsilent.body")}</div>
          <div className="ws-stale-actions">
            <Button
              className="ws-stale-go"
              onClick={() => {
                setOpen(false);
                void onStop();
              }}
            >
              {tr("wsbar.agentsilent.stop")}
            </Button>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {tr("wsbar.agentsilent.later")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
