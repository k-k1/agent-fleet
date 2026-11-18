import type { ReactNode } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { Choice, OnOff } from "../parts/controls.tsx";
import { ProviderCard } from "../parts/providerCard.tsx";
import { CliInstall } from "./CliInstall.tsx";
import { SettingRow, CardSettings, ThinkingRow, StreamRepliesRow, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";
import type {
  AgentRtkPatch,
  AgentRtkSettings,
  ClaudeSettings,
  ClaudeSettingsPatch,
  CodexSettings,
  CodexSettingsPatch,
} from "../api.ts";

// The Agents page's per-harness cards (ADR 0138 D16): what a harness does, never what it signs in
// with. Sign-ins, keys and endpoints are on Settings > Model credentials (credentials/), grouped
// by who is paid. Launch defaults are client-side and render in every workspace state; the
// container-backed toggles wait for their settings endpoint to answer.

export function ClaudeBehavior({
  head,
  claude,
  updateClaude,
}: {
  claude: Partial<ClaudeSettings> | null;
  updateClaude: (patch: ClaudeSettingsPatch) => void;
  head?: ReactNode;
}) {
  const tr = useT();
  const s = useSettings();
  return (
    <ProviderCard id="claude" name={kindDisplayName("claude")} status={undefined}>
      {head}
      <CardSettings>
        <LaunchDefaults kind="claude" />
        <ThinkingRow kind="claude" />
        <StreamRepliesRow kind="claude" />
        <SettingRow label={tr("agents.claude_abort_resume")}>
          <OnOff
            value={s.claudeAbortAutoResume}
            onChange={(v) => setSetting("claudeAbortAutoResume", v)}
          />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_claude_abort_resume")}</p>
        <SettingRow label={tr("agents.rate_limit_resume")}>
          <OnOff value={s.rateLimitAutoResume} onChange={(v) => setSetting("rateLimitAutoResume", v)} />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_claude_rate_limit_resume")}</p>
        {/* Remote Control / notifications / idle compaction / RTK are workspace-level files (independent of Claude
            auth) — pre-settable, but need the api/claude/settings endpoint loaded. */}
        {claude && (
          <>
            <SettingRow label={tr("agents.remote_control")}>
              <OnOff
                value={claude.remoteControlAtStartup}
                onChange={(v) => updateClaude({ remoteControlAtStartup: v })}
              />
            </SettingRow>
            <p className="ps-note">{tr("agents.note_claude_remote_control")}</p>
            <SettingRow label={tr("agents.notifications")}>
              <OnOff
                value={claude.agentPushNotifEnabled}
                onChange={(v) => updateClaude({ agentPushNotifEnabled: v })}
              />
            </SettingRow>
            {/* An Agent that predates the field omits it; showing OnOff with undefined would
                read as "off" while the CLI is in fact compacting. */}
            {typeof claude.idleCompaction === "boolean" && (
              <>
                <SettingRow label={tr("agents.idle_compaction")}>
                  <OnOff
                    value={claude.idleCompaction}
                    onChange={(v) => updateClaude({ idleCompaction: v })}
                  />
                </SettingRow>
                <p className="ps-note">{tr("agents.note_claude_idle_compaction")}</p>
              </>
            )}
            <RtkRow
              available={claude.rtk_available}
              value={claude.rtk_enabled}
              onChange={(v) => updateClaude({ rtk: v })}
            />
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}

export function CodexBehavior({
  head,
  codex,
  updateCodex,
  agents,
  updateAgents,
}: {
  codex: Partial<CodexSettings> | null;
  updateCodex: (patch: CodexSettingsPatch) => void;
  agents: Partial<AgentRtkSettings> | false | null;
  updateAgents: (patch: AgentRtkPatch) => void;
  head?: ReactNode;
}) {
  const tr = useT();
  const s = useSettings();
  return (
    <ProviderCard id="codex" name={kindDisplayName("codex")} status={undefined}>
      {head}
      {/* RTK is a workspace-level flag (independent of Codex auth) — pre-settable. */}
      <CardSettings>
        <LaunchDefaults kind="codex" />
        <ThinkingRow kind="codex" />
        {/* One switch shared with the Claude card (uiprefs.rateLimitAutoResume): what it
            governs is one behaviour of the workspace, and it is shown here because a Codex
            user has no reason to open the Claude card to find it (docs/log/47 §4-12). */}
        <SettingRow label={tr("agents.rate_limit_resume")}>
          <OnOff value={s.rateLimitAutoResume} onChange={(v) => setSetting("rateLimitAutoResume", v)} />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_codex_rate_limit_resume")}</p>
        {codex && (
          <>
            <SettingRow label={tr("agents.codex_nudge")}>
              <OnOff
                value={codex.rate_limit_model_nudge}
                onChange={(v) => updateCodex({ rate_limit_model_nudge: v })}
              />
            </SettingRow>
            <p className={`ps-note${codex.rate_limit_model_nudge ? " ps-note-warn" : ""}`}>
              {codex.rate_limit_model_nudge ? tr("agents.codex_nudge_on") : tr("agents.codex_nudge_off")}
            </p>
          </>
        )}
        {agents && (
          <>
            <RtkRow
              available={agents.rtk_available}
              value={agents.codex_rtk}
              onChange={(v) => updateAgents({ codex_rtk: v })}
            />
            <p className="ps-note">{tr("agents.codex_rtk_note")}</p>
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}

export function CursorBehavior({ head }: { head?: ReactNode }) {
  return (
    <ProviderCard id="cursor" name={kindDisplayName("cursor")} status={undefined}>
      {head}
      <CardSettings>
        <LaunchDefaults kind="cursor" />
      </CardSettings>
    </ProviderCard>
  );
}

export function CopilotBehavior({
  head,
  agents,
  updateAgents,
}: {
  agents: Partial<AgentRtkSettings> | false | null;
  updateAgents: (patch: AgentRtkPatch) => void;
  head?: ReactNode;
}) {
  const tr = useT();
  return (
    <ProviderCard id="copilot" name={kindDisplayName("copilot")} status={undefined}>
      {head}
      <CardSettings>
        <LaunchDefaults kind="copilot" />
        {agents && (
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

export function KiroBehavior({ head, running = false, supported }: { head?: ReactNode; running?: boolean; supported?: boolean }) {
  return (
    <ProviderCard id="kiro" name={kindDisplayName("kiro")} status={undefined}>
      {head}
      <CliInstall kind="kiro" running={running} supported={supported} />
      <CardSettings>
        <LaunchDefaults kind="kiro" />
      </CardSettings>
    </ProviderCard>
  );
}

export function AgyBehavior({
  head,
  agents,
  updateAgents,
}: {
  agents: Partial<AgentRtkSettings> | false | null;
  updateAgents: (patch: AgentRtkPatch) => void;
  head?: ReactNode;
}) {
  const tr = useT();
  return (
    <ProviderCard id="agy" name={kindDisplayName("agy")} status={undefined}>
      {head}
      {/* RTK is a workspace-level flag (independent of agy auth) — pre-settable,
          same block shape as the Codex / opencode cards. */}
      <CardSettings>
        <LaunchDefaults kind="agy" />
        {agents && (
          <>
            <RtkRow
              available={agents.rtk_available}
              value={agents.agy_rtk}
              onChange={(v) => updateAgents({ agy_rtk: v })}
            />
            <p className="ps-note">{tr("agents.agy_rtk_note")}</p>
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}

export function OpencodeBehavior({
  head,
  agents,
  updateAgents,
}: {
  agents: Partial<AgentRtkSettings> | false | null;
  updateAgents: (patch: AgentRtkPatch) => void;
  head?: ReactNode;
}) {
  return (
    <ProviderCard id="opencode" name={kindDisplayName("opencode")} status={undefined}>
      {head}
      <CardSettings>
        <LaunchDefaults kind="opencode" />
        <ThinkingRow kind="opencode" />
        {agents && (
          <RtkRow
            available={agents.rtk_available}
            value={agents.opencode_rtk}
            onChange={(v) => updateAgents({ opencode_rtk: v })}
          />
        )}
      </CardSettings>
    </ProviderCard>
  );
}

export function LcppBehavior({ head }: { head?: ReactNode }) {
  const tr = useT();
  const s = useSettings();
  return (
    <ProviderCard id="lcpp" name={kindDisplayName("lcpp")} status={undefined}>
      {head}
      {/* Whether llama.cpp may be launched at all: a harness switch, so it stays here while the
          member's endpoint is on Model credentials. The Agent enforces the same ui-prefs key. */}
      <div className="p-body">
        <SettingRow label={tr("agents.lcpp_enabled")}>
          <Choice
            value={s.lcppEnabled ? "on" : "off"}
            options={[
              ["off", tr("agents.lcpp_enabled_off")],
              ["on", tr("agents.lcpp_enabled_on")],
            ]}
            onChange={(v) => setSetting("lcppEnabled", v === "on")}
          />
        </SettingRow>
        <p className="ps-note">{tr(s.lcppEnabled ? "agents.lcpp_enabled_note_on" : "agents.lcpp_enabled_note_off")}</p>
      </div>
      <CardSettings>
        <LaunchDefaults kind="lcpp" />
      </CardSettings>
    </ProviderCard>
  );
}

export function MuseBehavior({ head, running = false, supported }: { head?: ReactNode; running?: boolean; supported?: boolean }) {
  return (
    <ProviderCard id="muse" name={kindDisplayName("muse")} status={undefined}>
      {head}
      <CliInstall kind="muse" running={running} supported={supported} />
      {/* The launch defaults stay reachable in every state, the same as every other card: they
          are client-side settings, so they work while the workspace is stopped and before the
          binary is installed. The model picker is empty until both are true, which is the
          honest answer — the catalogue is the signed-in account's. */}
      <CardSettings>
        <LaunchDefaults kind="muse" />
      </CardSettings>
    </ProviderCard>
  );
}
