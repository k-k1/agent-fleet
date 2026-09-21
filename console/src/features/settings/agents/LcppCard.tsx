import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { setSetting, useSettings } from "../../../lib/settings.ts";
import { ProviderCard, StatusPill } from "../parts/providerCard.tsx";
import { Choice } from "../parts/controls.tsx";
import { CardSettings, LaunchDefaults, SettingRow } from "./AgentCardParts.tsx";

// LcppCard (docs/log/105 §106.2). llama.cpp has no sign-in, no version drift and no adoption
// flow (ADR 0093 決定 10), so unlike every other agent card there is no connect step at all —
// just the user's own on/off switch plus the two client-side launch settings every kind gets
// (LaunchDefaults already includes HiddenModelsRow).
//
// The switch sits ABOVE CardSettings' collapsed disclosure rather than inside it: it disables
// the whole kind (registry.ts's available() is the signpost, HandleCreateSession the actual
// create-time gate), which is a workspace-policy decision, not a launch default — same
// reasoning as OpencodeUsageRows' "usage" switch (OpencodeCard.tsx). Unlike opencode's switch
// there is no billing-route radio group below it: lcpp has nothing else to choose.
export function LcppCard() {
  const tr = useT();
  const s = useSettings();
  const enabled = s.lcppEnabled;
  return (
    <ProviderCard
      id="lcpp"
      name={kindDisplayName("lcpp")}
      status={
        <StatusPill on={enabled}>
          {enabled ? tr("agents.lcpp_enabled_on") : tr("agents.lcpp_enabled_off")}
        </StatusPill>
      }
    >
      <SettingRow label={tr("agents.lcpp_enabled")}>
        <Choice
          value={enabled ? "on" : "off"}
          options={[
            ["off", tr("agents.lcpp_enabled_off")],
            ["on", tr("agents.lcpp_enabled_on")],
          ]}
          onChange={(v) => setSetting("lcppEnabled", v === "on")}
        />
      </SettingRow>
      <p className="ps-note">{tr(enabled ? "agents.lcpp_enabled_note_on" : "agents.lcpp_enabled_note_off")}</p>
      <CardSettings>
        <LaunchDefaults kind="lcpp" />
      </CardSettings>
    </ProviderCard>
  );
}
