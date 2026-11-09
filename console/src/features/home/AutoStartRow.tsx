// AutoStartRow — Home's line for a workspace the Control Plane started because the member
// signed in (#2031). It replaces the modal (WsStartingDialog) for that start: Home stays
// usable while the boot runs, and the boot phase is shown here instead.
//
// It also carries the one refusal worth explaining, a reached workspace limit; the other
// reasons (tenant off, opted out, state unknown) are silent by design.
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useWorkspaceStore, wsPreparing } from "../../core/store/workspace.ts";
import { phaseKey } from "../../lib/bootPhase.ts";
import { useSigninStartStore } from "./signinStart.ts";

export function AutoStartRow() {
  const tr = useT();
  const auto = useSigninStartStore((s) => s.auto);
  const blocked = useSigninStartStore((s) => s.blocked);
  const state = useWorkspaceStore((s) => s.state);
  const bootPhase = useWorkspaceStore((s) => s.bootPhase);
  if (auto && wsPreparing(state, bootPhase)) {
    return (
      <div className="home-autostart" role="status">
        <Icon name="loading" spin />
        <span>{bootPhase ? tr(phaseKey(bootPhase)) : tr("home.autostart_starting")}</span>
        {bootPhase && <span className="home-autostart-phase">{bootPhase}</span>}
      </div>
    );
  }
  if (blocked && state === "stopped") {
    return (
      <div className="home-autostart home-autostart-blocked" role="status">
        <Icon name="info" />
        <span>{tr("home.autostart_quota")}</span>
        {blocked.message && <span className="home-autostart-phase">{blocked.message}</span>}
      </div>
    );
  }
  return null;
}
