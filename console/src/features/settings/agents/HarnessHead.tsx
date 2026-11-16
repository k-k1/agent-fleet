import { useT } from "../../../lib/i18n/index.ts";
import { getSettings, setSettings, useSettings } from "../../../lib/settings.ts";
import { OnOff, Select } from "../parts/controls.tsx";
import { SettingRow } from "../parts/cardSettings.tsx";
import { useSettingsUI } from "../store.ts";
import { bedrockConnection, type ModelRouteProfile } from "../../repos/bedrockProfiles.ts";
import type { LiveKind } from "../credentials/memberCatalog.ts";

// The rows every harness card on the Agents page opens with (ADR 0138 D16): the member's on/off
// for the harness, its default route, and "Runs on". All of them read the live catalogue, so they
// name only what the policy allows (D10a).

/** A default route the select offers: the route, and the connection it is saved with. */
export interface RouteChoice {
  route: string;
  connection?: string;
  label: string;
}

/**
 * routeChoices are the default routes a harness may take: each route the policy allows it, with
 * the connection a per-connection route needs. A route whose connection the member has none of
 * (no marked AWS profile, no llama.cpp endpoint) is not offered, since a default the launch cannot
 * hold to would only be refused.
 */
export function routeChoices(
  live: LiveKind | undefined,
  label: (route: string) => string,
  profiles: ModelRouteProfile[] | null,
  lcppBinding: string | undefined,
): RouteChoice[] {
  const routes = [...new Set([...(live?.routes ?? []), ...(live?.fixedRoutes ?? [])])].sort();
  const out: RouteChoice[] = [];
  for (const r of routes) {
    if (r === "bedrock") {
      for (const p of profiles ?? []) {
        out.push({ route: r, connection: bedrockConnection(p.id, p.region), label: `${label(r)} — ${p.label}` });
      }
    } else if (r === "member-llamacpp") {
      if (lcppBinding) out.push({ route: r, connection: lcppBinding, label: label(r) });
    } else {
      out.push({ route: r, label: label(r) });
    }
  }
  return out;
}

const choiceKey = (c: { route: string; connection?: string }) => `${c.route}|${c.connection ?? ""}`;

export function HarnessHead({
  kind,
  live,
  profiles,
  lcppBinding,
  connectionsKnown = true,
  ownSwitch,
  onToggled,
}: {
  kind: string;
  live: LiveKind | undefined;
  profiles: ModelRouteProfile[] | null;
  lcppBinding?: string;
  /** The harness has an on/off of its own (llama.cpp's lcppEnabled), drawn by its card. */
  ownSwitch?: boolean;
  /** Whether the member's connections (marked profiles, the llama.cpp binding) have been read: until
   *  then a saved default that names one is not called unusable. */
  connectionsKnown?: boolean;
  /** Called when the switch is turned either way, so the page asks the Agent what is shown now. */
  onToggled: () => void;
}) {
  const tr = useT();
  const s = useSettings();
  const routeLabel = (r: string) => tr(("cred.route." + r) as Parameters<typeof tr>[0]);
  const choices = routeChoices(live, routeLabel, profiles, lcppBinding);
  const saved = s.agentLaunchDefaults[kind]?.route;
  const savedKey = saved ? choiceKey(saved) : "";
  const savedMatches = !saved || choices.some((c) => choiceKey(c) === savedKey);
  // A saved default on a per-connection route is judged only once the connections are read; until
  // then the row shows nothing chosen without calling it unusable.
  const savedUsable = savedMatches || (!connectionsKnown && !!saved?.connection);
  const usable = live?.usableRoutes;

  const setRoute = (key: string) => {
    const cur = getSettings().agentLaunchDefaults;
    const row = { ...cur[kind] };
    const pick = choices.find((c) => choiceKey(c) === key);
    if (pick) row.route = pick.connection ? { route: pick.route, connection: pick.connection } : { route: pick.route };
    else delete row.route;
    setSettings({ agentLaunchDefaults: { ...cur, [kind]: row } });
  };

  return (
    <div className="p-body harness-head">
      {!ownSwitch && (
        <>
          <SettingRow label={tr("agents.enabled")}>
            <OnOff
              value={s.agentEnabled[kind] !== false}
              onChange={(v) => {
                const next = { ...getSettings().agentEnabled };
                if (v) delete next[kind];
                else next[kind] = false;
                setSettings({ agentEnabled: next });
                onToggled();
              }}
            />
          </SettingRow>
          <p className="ps-note">{tr("agents.enabled_note")}</p>
        </>
      )}
      {choices.length > 0 && (
        <>
          <SettingRow label={tr("agents.default_route")}>
            <Select
              value={savedMatches ? savedKey : ""}
              options={[["", tr("agents.default_route_none")], ...choices.map((c): [string, string] => [choiceKey(c), c.label])]}
              onChange={(v: string) => setRoute(v)}
            />
          </SettingRow>
          <p className="ps-note">{tr(savedUsable ? "agents.default_route_note" : "agents.default_route_unusable")}</p>
        </>
      )}
      <p className="ps-note harness-runs-on">
        {usable && usable.length > 0
          ? `${tr("agents.runs_on")} ${usable.map(routeLabel).join(", ")}`
          : tr("agents.runs_on_none")}{" "}
        <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("credentials")}>
          {tr("agents.runs_on_link")}
        </button>
      </p>
    </div>
  );
}
