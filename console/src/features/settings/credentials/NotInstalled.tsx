import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { useSettingsUI } from "../store.ts";

// Shown in place of a sign-in whose CLI is not installed. The install itself is on the harness's
// Agents card (ADR 0138 D16); the sign-in cannot run without the binary, so this says why and
// where to go instead of offering a button that would fail.
export function NotInstalled({ kind }: { kind: "kiro" | "muse" }) {
  const tr = useT();
  return (
    <div className="p-body">
      <p className="ps-note ps-note-warn">
        {tr("cred.cli_not_installed", { name: kindDisplayName(kind) })}{" "}
        <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("agents", { target: kind })}>
          {tr("cred.cli_install_on_agents")}
        </button>
      </p>
    </div>
  );
}
