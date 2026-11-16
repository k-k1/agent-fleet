import { useEffect } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { ProviderCard, StatusPill } from "../parts/providerCard.tsx";
import { useSettingsUI } from "../store.ts";
import type { ModelRouteProfile } from "../../repos/bedrockProfiles.ts";
import { useEnginesStore } from "../../engines/store.ts";
import { stateWord } from "../../engines/wire.ts";

// Read-only cards on Model credentials (ADR 0138 D16): credentials the member does not enter on
// this page. A Bedrock profile is marked as a model route on the AWS profiles page, where the mark
// stays; the deployment's engine is the operator's.

/** BedrockCard lists the AWS profiles marked as a model route, with the way to change the mark. */
export function BedrockCard({ profiles }: { profiles: ModelRouteProfile[] }) {
  const tr = useT();
  return (
    <ProviderCard
      id="amazon-bedrock"
      name="Amazon Bedrock"
      status={<StatusPill on={profiles.length > 0}>{tr("cred.readonly")}</StatusPill>}
    >
      <div className="p-body">
        {profiles.length === 0 ? (
          <p className="ps-note">{tr("cred.bedrock_none")}</p>
        ) : (
          <ul className="oc-keys" data-testid="cred-bedrock-profiles">
            {profiles.map((p) => (
              <li key={p.id}>
                <span>{p.label}</span>
                <span className="muted">{p.region || tr("cred.bedrock_region_own")}</span>
              </li>
            ))}
          </ul>
        )}
        <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
          {tr("cred.bedrock_edit")}
        </button>
      </div>
    </ProviderCard>
  );
}

/** EngineCard is the deployment's own chat engine, as the engines indicator reports it. */
export function EngineCard() {
  const tr = useT();
  const rows = useEnginesStore((s) => s.rows);
  const refresh = useEnginesStore((s) => s.refresh);
  useEffect(() => {
    if (rows === null) void refresh();
  }, [rows, refresh]);
  const chat = (rows ?? []).filter((r) => r.api === "chat");
  return (
    <ProviderCard
      id="af-engine"
      name={tr("cred.engine_name")}
      status={<StatusPill on={chat.length > 0}>{tr("cred.readonly")}</StatusPill>}
    >
      <div className="p-body">
        {chat.length === 0 ? (
          <p className="ps-note">{tr("cred.engine_none")}</p>
        ) : (
          <ul className="oc-keys">
            {chat.map((r) => (
              <li key={r.key}>
                {tr("cred.engine_state", {
                  key: r.key,
                  state: tr(`engine.state_${stateWord(r)}` as Parameters<typeof tr>[0]),
                })}
              </li>
            ))}
          </ul>
        )}
      </div>
    </ProviderCard>
  );
}
