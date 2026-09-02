import { useState } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useSettings, setSettings } from "../../lib/settings.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { Choice } from "./controls.tsx";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, IssueLink } from "./providerCard.tsx";
import { usePolling } from "./usePolling.ts";
import { SettingRow, CardSettings, ThinkingRow, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

// opencode: two independent auth paths that coexist (docs/log/54) —
//   ① opencode アカウント: OAuth device flow through `opencode serve`'s integration
//      API. Approval happens entirely in the browser (mode="auto", opencode polls the
//      token itself), so like Cursor there is no code to paste; we show the URL and
//      poll api/connections/opencode/oauth/poll.
//   ② provider API keys: stored and injected as env at launch (unchanged).
// "Connected" = either path is set up. Plus the RTK and Web UI toggles.
// [presetId, label, envVar, issueUrl]. issueUrl is the provider's fixed API-key page
// (empty = none / handled elsewhere — "go" keeps its own opencode.ai/auth hint below).
const OC_PRESETS = [
  ["go", "OpenCode Go", "OPENCODE_API_KEY", ""],
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
function OpencodeUsageRow() {
  const s = useSettings();
  const tr = useT();
  return (
    <>
      <SettingRow label={tr("agents.oc_usage")}>
        <Choice
          value={s.opencodeCatalog}
          options={[
            ["off", tr("agents.oc_usage_off")],
            ["free", tr("agents.oc_usage_free")],
            ["go", tr("agents.oc_usage_go")],
            ["zen", tr("agents.oc_usage_zen")],
          ]}
          onChange={(v) =>
            setSettings({ opencodeCatalog: v === "off" || v === "free" || v === "go" ? v : "zen" })
          }
        />
      </SettingRow>
      <p className="ps-note">{tr(`agents.oc_usage_note_${s.opencodeCatalog}`)}</p>
    </>
  );
}

// 利用枠の導線（docs/log/54 §54.7）。opencode.ai の利用枠ページはブラウザセッション前提で、
// 数値を取り込む API が無い（実測: ページは /auth/authorize へ 302、console 側 API に
// usage は無い）。だから Console が持てるのは workspace ID と、そこへのリンクと、上限に
// 当たったときにエラーが運んできた枠情報だけ。ID は手入力でも、失敗から自動で学習しても
// 埋まる（学習が手入力を上書きすることはない）。
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
      toast(tr("common.save_failed_msg", { msg: String(res.error.message || res.error) }));
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
  const envName =
    preset === "custom" ? customEnv.trim().toUpperCase() : OC_PRESETS.find((p) => p[0] === preset)?.[2] || "";
  const issueUrl = OC_PRESETS.find((p) => p[0] === preset)?.[3] || "";

  const add = async () => {
    if (!envName || !key.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/opencode", "PUT", { env: envName, key: key.trim() });
      if (res && res.error) {
        toast(tr("common.save_failed_msg", { msg: String(res.error.message || res.error) }));
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
        toast(tr("agents.oc_account_failed", { msg: res?.error?.message ? `: ${res.error.message}` : "" }));
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
          // failed / expired は opencode 側で確定した終状態 — 待ち続けても変わらない。
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

  const usage = s.opencodeCatalog; // off | free | go | zen（課金経路の選択・docs/log/54）
  const off = usage === "off";
  const pill = [
    off ? tr("agents.oc_usage_off") : usage === "free" ? tr("agents.oc_usage_free") : "",
    !off && envs.length > 0 ? tr("agents.oc_key_count", { count: envs.length }) : "",
    !off && account ? tr("agents.oc_account_only") : "",
  ]
    .filter(Boolean)
    .join(" / ");

  return (
    <ProviderCard
      id="opencode"
      name={kindDisplayName("opencode")}
      status={
        running ? (
          <StatusPill on={!off && (usage === "free" || envs.length > 0 || account)}>
            {pill || tr("conn.disconnected")}
          </StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : (
        <>
          <div className="p-body">
            <OpencodeUsageRow />
          </div>
          {off ? (
            <div className="p-desc">{tr("agents.oc_off_desc")}</div>
          ) : usage === "free" ? (
            <div className="p-desc">{tr("agents.oc_free_desc")}</div>
          ) : (
            <div className="p-desc">{tr("agents.oc_account_desc")}</div>
          )}
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
                    (実測) — the user compares it and approves, pasting nothing. Hence the
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
          {usage !== "free" && !off && <OpencodeWorkspaceRow st={st} reload={reload} />}
          <div className="p-desc">{tr("agents.oc_desc")}</div>
          <div className="p-body">
            {preset === "go" && (
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
                    <button className="icon danger" title={tr("common.delete")} onClick={() => remove(e)}>
                      ✕
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="flow">
              <select className="cinput" value={preset} onChange={(e) => setPreset(e.target.value)}>
                {OC_PRESETS.map(([v, label]) => (
                  <option key={v} value={v}>
                    {v === "custom" ? tr("agents.oc_custom") : label}
                  </option>
                ))}
              </select>
              {preset === "custom" && (
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
