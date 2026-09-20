import { useState } from "react";
import { api, apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useSettings, setSettings } from "../../../lib/settings.ts";
import { useOpencodeAppliedRoute } from "../../../lib/agentModels.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { Choice } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, IssueLink } from "../parts/providerCard.tsx";
import { usePolling } from "../parts/usePolling.ts";
import { SettingRow, CardSettings, ThinkingRow, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

// opencode: two independent auth paths that coexist (docs/log/54) —
//   1. opencode account: OAuth device flow through `opencode serve`'s integration
//      API. Approval happens entirely in the browser (mode="auto", opencode polls the
//      token itself), so like Cursor there is no code to paste; we show the URL and
//      poll api/connections/opencode/oauth/poll.
//   2. provider API keys: stored and injected as env at launch (unchanged).
// "Connected" = either path is set up. Plus the RTK and Web UI toggles.
// [presetId, label, envVar, issueUrl]. issueUrl is the provider's fixed API-key page
// (empty = none / handled elsewhere — "go" keeps its own opencode.ai/auth hint below).
// The first entry's label is resolved via i18n (agents.oc_preset_opencode) at render: ONE
// key pays for both opencode.ai routes, and calling it "OpenCode Go" put the word Go in two
// meanings on one card — a route above, a key here — while hiding that the same key is what
// Zen bills against.
const OC_PRESETS = [
  ["go", "", "OPENCODE_API_KEY", ""],
  ["anthropic", "Anthropic", "ANTHROPIC_API_KEY", "https://console.anthropic.com/settings/keys"],
  ["openai", "OpenAI", "OPENAI_API_KEY", "https://platform.openai.com/api-keys"],
  ["openrouter", "OpenRouter", "OPENROUTER_API_KEY", "https://openrouter.ai/keys"],
  ["google", "Google Gemini", "GEMINI_API_KEY", "https://aistudio.google.com/apikey"],
  ["sakana", "Sakana AI", "SAKANA_API_KEY", "https://console.sakana.ai/api-keys"],
  ["custom", "", "", ""], // label resolved via i18n (agents.oc_custom) at render
];

// One OPENCODE_API_KEY opens both opencode.ai billing routes, so `opencode models`
// returns the Go subscription's ids (opencode-go/…) alongside Zen's metered ones
// (opencode/…) — with 10 of the 16 Go models colliding by name. A Go subscriber rarely
// wants the metered twins in the list at all, so this shapes it. The Agent reads the
// same preference from ui-prefs, which is what makes it apply to the MCP list_models an
// assistant picks from — the path that actually caused a launch on the wrong route.
// It only shapes the MENU: an explicitly requested model id is still honored verbatim.
//
// ⚠️ TWO controls over ONE stored value (settings.opencodeCatalog), because they are two
// different decisions and the single 4-way control said otherwise:
//
//   - the switch is about the KIND. "off" ignores every stored key and the account login,
//     for good, which is a workspace-policy decision;
//   - the route list is about opencode.ai ONLY. None of its values can take another vendor's
//     key away (`keepForUsage` passes anthropic/… through on all of them), yet sitting at the
//     top of the card as "which allowance to use" they read as if they governed everything.
//
// The routes are a vertical list rather than a segmented control so all four are readable at
// once: this is a choice about money, and the old shape showed the note for the selected one
// only — you had to click a route to find out what it meant.
const OC_ROUTES = ["own", "free", "go", "zen"] as const;

function OpencodeUsageRows() {
  const s = useSettings();
  const tr = useT();
  const selected = s.opencodeCatalog;
  const off = selected === "off";
  // The route to return to when the switch goes back on. Kept in the component rather than
  // persisted: turning opencode off is not a reason to forget the route, and "own" is the
  // right landing place for a workspace switching it on for the first time — it is the only
  // value that reaches no opencode.ai service until the user asks for one.
  const [lastRoute, setLastRoute] = useState<string>(off ? "own" : selected);

  return (
    <>
      <SettingRow label={tr("agents.oc_enabled")}>
        <Choice
          value={off ? "off" : "on"}
          options={[
            ["off", tr("agents.oc_enabled_off")],
            ["on", tr("agents.oc_enabled_on")],
          ]}
          onChange={(v) => setSettings({ opencodeCatalog: v === "off" ? "off" : (lastRoute as any) })}
        />
      </SettingRow>
      <p className="ps-note">{tr(off ? "agents.oc_enabled_note_off" : "agents.oc_enabled_note_on")}</p>
      {!off && (
        <>
          <SettingRow label={tr("agents.oc_usage")} />
          <div className="p-opts p-opts-col" role="radiogroup" aria-label={tr("agents.oc_usage")}>
            {OC_ROUTES.map((v) => (
              <button
                key={v}
                type="button"
                role="radio"
                data-route={v}
                aria-checked={v === selected}
                className={"p-opt" + (v === selected ? " is-on" : "")}
                onClick={() => {
                  setLastRoute(v);
                  setSettings({ opencodeCatalog: v });
                }}
              >
                <span className="p-opt-t">{tr(`agents.oc_usage_${v}`)}</span>
                <span className="p-opt-s">{tr(`agents.oc_usage_note_${v}`)}</span>
              </button>
            ))}
          </div>
        </>
      )}
    </>
  );
}

// The empty-menu rescue, said out loud. `Catalog` re-shapes with Zen when the selected route
// would leave the picker empty — an account with no Go contract that selects Go is shown the
// METERED twins — and until now the card went on claiming the selected route while the launch
// list disagreed with it. The Agent reports what it applied (GET /agents/opencode/models
// `route`); anything else is not a mismatch worth a warning.
// A lookup rather than a template literal: `applied` comes off the wire, and an Agent newer
// than this Console could name a route whose label does not exist here.
const ROUTE_LABEL = {
  off: "agents.oc_usage_off",
  own: "agents.oc_usage_own",
  free: "agents.oc_usage_free",
  go: "agents.oc_usage_go",
  zen: "agents.oc_usage_zen",
} as const;

// The same routes as a status chip. Separate strings because the chip has room for a word,
// not a sentence: the list's "None (my own keys)" reads as a description where it belongs and
// as a riddle next to a green dot.
const PILL_LABEL = {
  off: "agents.oc_usage_off",
  own: "agents.oc_pill_own",
  free: "agents.oc_pill_free",
  go: "agents.oc_pill_go",
  zen: "agents.oc_pill_zen",
} as const;

function OpencodeRouteFallback() {
  const tr = useT();
  const selected = useSettings().opencodeCatalog;
  const applied = useOpencodeAppliedRoute();
  const label = ROUTE_LABEL[applied as keyof typeof ROUTE_LABEL];
  if (!label || applied === selected || selected === "off") return null;
  return (
    <p className="ps-note ps-note-warn">
      {tr("agents.oc_route_fallback", { chosen: tr(ROUTE_LABEL[selected]), applied: tr(label) })}
    </p>
  );
}

// Keys reach `opencode serve` as environment and the engine block as a config file, both
// read once at start, so a change made here does nothing to the daemon already running.
// Taking the restart automatically is what this replaced: it drains, and a session still
// answering when the drain times out loses that turn. Whoever just changed a setting is the
// one who can judge whether now is the moment, so it is offered rather than taken.
function OpencodeRestartRow({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const pending = st?.restart_required;
  if (!pending) return null;

  const apply = async () => {
    setBusy(true);
    const res = await apiJSON("api/connections/opencode/serve/restart", "POST", {});
    setBusy(false);
    if (res && res.error) {
      toast(tr("agents.oc_restart_failed", { msg: errDetail(res.error) }));
      return;
    }
    toast(tr("agents.oc_restart_done"));
    reload();
  };

  return (
    <div className="p-body">
      <Hint>{tr("agents.oc_restart_pending", { count: (pending.reasons || []).length })}</Hint>
      <div className="flow">
        <button type="button" disabled={busy} onClick={() => void apply()}>
          {tr("agents.oc_restart_apply")}
        </button>
      </div>
      <p className="ps-note">{tr("agents.oc_restart_note")}</p>
    </div>
  );
}

// The quota affordance (docs/log/54 §54.7). opencode.ai's quota page assumes a browser session
// and there is no API to pull the numbers from (measured: the page 302s to /auth/authorize, and
// the console-side API has no usage endpoint). So all the Console can hold is the workspace ID,
// a link to that page, and whatever quota information an error carried when a limit was hit.
// The ID can be typed in or learned automatically from a failure; learning never overwrites a
// hand-entered value.
function OpencodeWorkspaceRow({ st, reload }: { st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const id = st?.workspace_id || "";
  const url = st?.workspace_url || "";
  const limit = st?.last_limit;

  const save = async (value: string) => {
    const res = await apiJSON("api/connections/opencode/workspace", "PUT", { id: value });
    if (res && res.error) {
      toast(tr("common.save_failed_msg", { msg: errDetail(res.error) }));
      return;
    }
    setEditing(false);
    setDraft("");
    reload();
  };

  return (
    <div className="p-body">
      {id && url ? (
        <>
          <div className="p-who">
            <a href={url} target="_blank" rel="noopener" className="flow-link">
              {tr("agents.oc_ws_open")}
            </a>
            <button className="ghost" onClick={() => { setDraft(id); setEditing(true); }}>
              {tr("agents.oc_ws_edit")}
            </button>
          </div>
          {limit && (limit.name || limit.reset_at) && (
            <Hint>
              {tr("agents.oc_ws_limit", {
                name: limit.name || tr("agents.oc_ws_limit_unknown"),
                at: limit.reset_at ? new Date(limit.reset_at).toLocaleString() : "-",
              })}
            </Hint>
          )}
        </>
      ) : (
        <div className="p-desc">{tr("agents.oc_ws_desc")}</div>
      )}
      {(editing || !id) && (
        <div className="flow">
          <input
            className="cinput"
            placeholder={tr("agents.oc_ws_placeholder")}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <button disabled={!draft.trim()} onClick={() => void save(draft.trim())}>
            {tr("common.save")}
          </button>
          {id && (
            <button className="ghost" onClick={() => { setEditing(false); setDraft(""); }}>
              {tr("common.cancel")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export function OpencodeCard({
  running,
  st,
  reload,
  agents,
  updateAgents,
}: {
  running: boolean;
  st: any;
  reload: () => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const s = useSettings();
  const [preset, setPreset] = useState("go");
  const [customEnv, setCustomEnv] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [flow, setFlow] = useState<any>(null); // { url, flow_id, instructions, status } while a sign-in is in flight
  const [oauthBusy, setOauthBusy] = useState(false);
  const envs = st?.envs || [];
  const account = !!st?.oauth;
  const accountOff = !!st?.oauth_disabled;
  const usage = s.opencodeCatalog; // off | own | free | go | zen — see OpencodeUsageRows
  const off = usage === "off";
  const usesOpencodeAI = !off && usage !== "own";
  // On "own" the opencode.ai key is not injected at all (auth.go's env()), so offering to
  // store one here would be a form whose result does nothing. The preset falls back to the
  // first remaining entry rather than sticking on a hidden one.
  const presets = usesOpencodeAI ? OC_PRESETS : OC_PRESETS.filter((p) => p[0] !== "go");
  const active = presets.some((p) => p[0] === preset) ? preset : String(presets[0][0]);
  const envName =
    active === "custom" ? customEnv.trim().toUpperCase() : presets.find((p) => p[0] === active)?.[2] || "";
  const issueUrl = presets.find((p) => p[0] === active)?.[3] || "";

  const add = async () => {
    if (!envName || !key.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/opencode", "PUT", { env: envName, key: key.trim() });
      if (res && res.error) {
        toast(tr("common.save_failed_msg", { msg: errDetail(res.error) }));
        return;
      }
      setKey("");
      setCustomEnv("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (env: string) => {
    await raw(`api/connections/opencode/${encodeURIComponent(env)}`, { method: "DELETE" });
    reload();
  };

  const startAccountLogin = async () => {
    setOauthBusy(true);
    try {
      const res = await api("api/connections/opencode/oauth/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.oc_account_failed", { msg: res?.error ? `: ${errDetail(res.error)}` : "" }));
        return;
      }
      setFlow({
        url: res.url,
        flow_id: res.flow_id,
        code: res.user_code || "",
        instructions: res.instructions || "",
        status: tr("git.oauth_waiting"),
      });
      poll({
        deadlineMs: 10 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setFlow((f: any) => (f ? { ...f, status: tr("git.oauth_expired") } : f)),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/opencode/oauth/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setFlow(null);
            reload();
            return { stop: true };
          }
          // failed / expired are terminal states decided by opencode — waiting changes nothing.
          if (p && (p.status === "failed" || p.status === "expired")) {
            setFlow(null);
            toast(tr("agents.oc_account_denied", { msg: p.message ? `: ${p.message}` : "" }));
            return { stop: true };
          }
          return { stop: false, nextMs: 2500 };
        },
      });
    } finally {
      setOauthBusy(false);
    }
  };
  const cancelAccountLogin = async () => {
    const id = flow?.flow_id;
    setFlow(null);
    if (id) await apiJSON("api/connections/opencode/oauth/cancel", "POST", { flow_id: id }).catch(() => {});
  };
  const disconnectAccount = async () => {
    await raw("api/connections/opencode/oauth", { method: "DELETE" });
    setFlow(null);
    reload();
  };

  // The route is ALWAYS named, not just on the two values that used to name themselves: with
  // Go and Zen reduced to "2 keys" the card could not say which of the two bills for the next
  // turn, which is the question this card exists to answer.
  const pill = [
    tr(PILL_LABEL[usage]),
    !off && envs.length > 0 ? tr("agents.oc_key_count", { count: envs.length }) : "",
    usesOpencodeAI && account ? tr("agents.oc_account_only") : "",
  ]
    .filter(Boolean)
    .join(" / ");
  // Mirrors the Agent's usability rule (auth.go's connected): free needs nothing, "own" needs
  // a key that is NOT the opencode.ai one, anything else takes a key or the account. The one
  // input the Console cannot see is a self-hosted engine, which also makes "own" usable — so
  // this pill can read disconnected on a deployment where a launch works. It summarises;
  // registry.ts asks the Agent.
  const directKeys = envs.filter((e: string) => e !== "OPENCODE_API_KEY");
  const live =
    !off &&
    (usage === "free" ? true : usage === "own" ? directKeys.length > 0 : envs.length > 0 || account);

  return (
    <ProviderCard
      id="opencode"
      name={kindDisplayName("opencode")}
      status={running ? <StatusPill on={live}>{pill || tr("conn.disconnected")}</StatusPill> : undefined}
    >
      {!running ? (
        <ConnPaused />
      ) : (
        <>
          <OpencodeRestartRow st={st} reload={reload} />
          <div className="p-body">
            <OpencodeUsageRows />
            <OpencodeRouteFallback />
          </div>
          {/* Off hides the credentials entirely rather than greying them: everything below
              is ignored while it is selected, and a form that still invites a key is the
              clearest way to suggest otherwise. The switch's own note carries the
              explanation, so there is nothing else to draw here. */}
          {!off && (
          <>
          {/* Only the sign-in needs an introduction of its own. own / free used to carry one
              too, which repeated what the route they had just selected already said. */}
          {usesOpencodeAI && <div className="p-desc">{tr("agents.oc_account_desc")}</div>}
          {/* The account sign-in is opencode.ai's, so it is not offered on the route that
              declines opencode.ai — showing a connect button whose result would be ignored is
              how the old card made the route look like it governed less than it does. */}
          {usesOpencodeAI && (
          <div className="p-body">
            {accountOff ? (
              <div className="p-desc">{tr("agents.oc_account_disabled")}</div>
            ) : account ? (
              <div className="p-who">
                <span className="p-em" title={st?.oauth_label || ""}>
                  {st?.oauth_label || tr("agents.oc_account_connected")}
                </span>
                <DisconnectButton onClick={disconnectAccount} />
              </div>
            ) : flow ? (
              <>
                {/* opencode polls the token itself (mode="auto") and the verification URL
                    already carries the code, so the approval page shows it pre-filled
                    (measured) — the user compares it and approves, pasting nothing. Hence the
                    confirm shape; when the code can't be extracted the steps degrade to
                    just the link. */}
                <DeviceSteps confirm code={flow.code || undefined} url={flow.url} status={flow.status} />
                {!flow.code && flow.instructions && <Hint>{flow.instructions}</Hint>}
                <div className="flow">
                  <button type="button" onClick={cancelAccountLogin}>
                    {tr("common.cancel")}
                  </button>
                </div>
              </>
            ) : (
              <div className="p-opts">
                <button type="button" className="p-opt" disabled={oauthBusy} onClick={startAccountLogin}>
                  <span className="p-opt-t">{tr("agents.oc_account_connect")}</span>
                  <span className="p-opt-s">{tr("agents.oc_account_connect_note")}</span>
                </button>
              </div>
            )}
            <p className="ps-note">{tr("agents.oc_account_note")}</p>
          </div>
          )}
          {/* The usage page is opencode.ai's Go allowance, so it belongs to the routes that
              can spend it. */}
          {usesOpencodeAI && usage !== "free" && <OpencodeWorkspaceRow st={st} reload={reload} />}
          <div className="p-desc">{tr(usesOpencodeAI ? "agents.oc_desc" : "agents.oc_desc_own")}</div>
          <div className="p-body">
            {active === "go" && (
              <Hint>
                <a href="https://opencode.ai/auth" target="_blank" rel="noopener" className="flow-link">
                  opencode.ai/auth
                </a>
                {tr("agents.oc_hint")}
              </Hint>
            )}
            {issueUrl && <IssueLink url={issueUrl} />}
            {envs.length > 0 && (
              <ul className="oc-keys">
                {envs.map((e: string) => (
                  <li key={e}>
                    <code>{e}</code>
                    {/* Stored but not injected on free / own (auth.go's env()). Saying so
                        here is the difference between "my key is set up" and "my key is set
                        up and being used", which the list could not tell apart. */}
                    {!usesOpencodeAI && e === "OPENCODE_API_KEY" && (
                      <span className="oc-key-idle">{tr("agents.oc_key_not_injected")}</span>
                    )}
                    <button className="icon danger" title={tr("common.delete")} onClick={() => remove(e)}>
                      ✕
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="flow">
              <select className="cinput" value={active} onChange={(e) => setPreset(e.target.value)}>
                {presets.map(([v, label]) => (
                  <option key={v} value={v}>
                    {v === "custom" ? tr("agents.oc_custom") : v === "go" ? tr("agents.oc_preset_opencode") : label}
                  </option>
                ))}
              </select>
              {active === "custom" && (
                <input
                  className="cinput"
                  placeholder={tr("agents.oc_env_placeholder")}
                  value={customEnv}
                  onChange={(e) => setCustomEnv(e.target.value)}
                />
              )}
              <input
                className="cinput"
                type="password"
                placeholder={envName ? tr("agents.oc_key_value", { env: envName }) : tr("agents.oc_key_fallback")}
                value={key}
                onChange={(e) => setKey(e.target.value)}
              />
              <button disabled={busy || !envName || !key.trim()} onClick={add}>
                {tr("conn.connect")}
              </button>
            </div>
          </div>
          </>
          )}
        </>
      )}
      <CardSettings>
        <LaunchDefaults kind="opencode" />
        <ThinkingRow kind="opencode" />
        {agents && agents !== false && (
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
