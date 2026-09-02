import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill } from "../parts/providerCard.tsx";
import { useSettingsUI } from "../store.ts";
import { CardSettings, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

// agy (Antigravity CLI, docs/log/32): claude-style OAuth connect (start → approve in a
// new tab → paste code → complete) with an auth-method selector (M1 offers Google
// OAuth only; the GCP-project method lands with M2), plus the shared RTK toggle so
// the card reads like the other agents'. The 実験枠 label is a 採用条件 (docs/log/32
// Track C-3): the Starter pool is tiny and shared with the IDE/Jules wallet, so the
// card must always say so. The quota gauge (残量%) lives in the WS bar next to the
// Claude / Codex usage chips. On unsupported hosts (no RDRAND) the card shows why
// instead of the connect flow.
// CopilotCard: GitHub Copilot CLI（docs/log/36）。専用の認証フローを持たない —
// GitHub 連携（gh 透過認証）に相乗りするので、状態表示と起動既定のみ。接続/切断は
// 連携タブの GitHub 側で行う。
export function CopilotCard({
  running,
  st,
  agents,
  updateAgents,
}: {
  running: boolean;
  st: any;
  agents: any;
  updateAgents: (patch: unknown) => void;
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
            <p className="ps-note">
              {tr("agents.copilot_not_connected")}{" "}
              {/* Copilot rides GitHub auth — jump straight to the Gitホスティング tab. */}
              <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("git")}>
                {tr("agents.copilot_open_git")}
              </button>
            </p>
          )}
        </>
      )}
      <CardSettings>
        <LaunchDefaults kind="copilot" />
        {agents && agents !== false && (
          <>
            <RtkRow
              available={agents.rtk_available}
              value={agents.copilot_rtk}
              onChange={(v) => updateAgents({ copilot_rtk: v })}
            />
            <p className="ps-note">{tr("agents.copilot_rtk_note")}</p>
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}
