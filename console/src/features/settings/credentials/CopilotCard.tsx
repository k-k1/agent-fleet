import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill } from "../parts/providerCard.tsx";
import { useSettingsUI } from "../store.ts";
import { ConnPaused } from "../agents/AgentCardParts.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// agy (Antigravity CLI, docs/log/32): claude-style OAuth connect (start → approve in a
// new tab → paste code → complete) with an auth-method selector (M1 offers Google
// OAuth only; the GCP-project method lands with M2). Its RTK toggle is on the Agents page. The "experimental tier" label is an adoption condition
// (docs/log/32 Track C-3): the Starter pool is tiny and shared with the IDE/Jules wallet, so the
// card must always say so. The quota gauge (remaining %) lives in the WS bar next to the
// Claude / Codex usage chips. On unsupported hosts (no RDRAND) the card shows why
// instead of the connect flow.
// CopilotCard: GitHub Copilot CLI (docs/log/36). It has no auth flow of its own — it rides on
// the GitHub connection (gh transparent auth), so this card only shows status and the launch
// defaults. Connect and disconnect happen on the GitHub side of the connections tab.
export function CopilotCard({
  running,
  st,
}: {
  running: boolean;
  st: ProviderConn | undefined;
}) {
  const tr = useT();
  const unsupported = st?.supported === false;
  return (
    <ProviderCard
      id="copilot"
      name={kindDisplayName("copilot")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : unsupported ? (
        <div className="p-desc">{tr("agents.copilot_unsupported", { reason: st?.reason || "" })}</div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.copilot_desc")}</div>
          {!st?.connected && (
            <p className="ps-note p-card-note">
              {tr("agents.copilot_not_connected")}{" "}
              {/* Copilot rides GitHub auth — jump straight to the Git hosting tab. */}
              <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("git")}>
                {tr("agents.copilot_open_git")}
              </button>
            </p>
          )}
        </>
      )}
    </ProviderCard>
  );
}
