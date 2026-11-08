// Agent memory > Settings: the sessions' switch with the auto-sync status, and the outside-client
// setting. Both are ui-prefs, so this is also the one view that works while the workspace is
// stopped (#1735).

import { useT } from "../../../../lib/i18n/index.ts";
import { AgentMemorySwitch, ExternalMemorySwitch } from "../memoryChanges.tsx";
import { ConsolidationSettings } from "../memoryProposals.tsx";

export function AgentSettingsView({ running }: { running: boolean }) {
  const tr = useT();
  return (
    <>
      <p className="muted ds-hint">{tr("mem.af_intro")}</p>
      <AgentMemorySwitch />
      <ExternalMemorySwitch />
      <ConsolidationSettings running={running} />
    </>
  );
}
