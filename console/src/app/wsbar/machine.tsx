import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { type WsMachine } from "../../lib/machine.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useSettingsUI } from "../../features/settings/store.ts";

// MachineDetailsLink: deep link from the resources popover into Settings › Machine, the
// same hand-over UsageBreakdownLink makes from the usage chip. These tiles answer "how much
// am I using"; "of what, and can I have more" is the next question and a 28px sparkline has
// nowhere to answer it — the tab has the box, its limits and an hour of trend.
export function MachineDetailsLink({ onNavigate }: { onNavigate: () => void }) {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <Button
      className="wu-manage"
      onClick={() => {
        onNavigate();
        openSettings("machine");
      }}
    >
      <Icon name="server" /> {tr("wsbar.machine_details")}
    </Button>
  );
}

// useWsMachine reads WHAT the workspace runs on, for the header line of the resources
// popover. Deliberately not part of the 4s tick above: the answer only changes when the
// container is recreated, so it is fetched when the popover is opened and again when the
// workspace starts or stops (a restart is when a re-sized box actually takes effect).
export function useWsMachine(open: boolean, tenant: string | null, running: boolean) {
  const [machine, setMachine] = useState<WsMachine | null>(null);
  useEffect(() => {
    if (!open) return;
    let alive = true;
    api("api/workspace/machine")
      .then((d: any) => alive && setMachine(d && !d.error ? d : null))
      .catch(() => alive && setMachine(null));
    return () => {
      alive = false;
    };
  }, [open, tenant, running]);
  return machine;
}
