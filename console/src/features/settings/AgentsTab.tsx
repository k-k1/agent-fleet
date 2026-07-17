import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import type { ReactNode } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { Sparkline } from "../../ui/Sparkline.tsx";
import { fmtTok } from "../../lib/fmttok.ts";
import { fmtNum } from "../../lib/intl.ts";
import { Choice, OnOff, Select } from "./controls.tsx";
import {
  agentLaunchDefault,
  useSettings,
  setSetting,
  setSettings,
  OUTPUT_LANGUAGES,
  ASSISTANT_AGENTS,
} from "../../lib/settings.ts";
import { useEffortOptions, useModelOptions } from "../../lib/agentModels.ts";
import { agentOf } from "../../agents/registry.ts";
import { useConnections } from "./useConnections.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { usePolling } from "./usePolling.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.tsx";
import { useT } from "../../lib/i18n/index.ts";

// AgentsTab is the per-agent home: for Claude / Codex / opencode it combines the
// CONNECTION (auth flow + status) and the BEHAVIOR settings (Remote Control / 通知 /
// RTK / opencode Web UI) in one card, so "set up Claude" is one place. Git-hosting
// providers live in their own GitTab. Connection auth goes through the Agent
// (secrets stored container-side); behavior settings via api/claude/settings +
// api/agents/rtk and apply to NEW sessions. Both need the workspace running.
export function AgentsTab() {
  const tr = useT();
  const toast = useToast();
  // Client-side session pref (タイトル自動提案) — persisted in the local settings
  // store, so it shows regardless of workspace state (unlike the agent behavior
  // cards below, which need the container's Agent/CLI). 既定モデルは claude 固有なので
  // ClaudeCard 内に置く。
  const s = useSettings();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  // Connection auth AND behavior settings both go through the in-container Agent
  // (proxyAgentREST → 502 when the workspace is stopped), so the whole tab requires
  // a running workspace — there's no CP-side DB to edit against while stopped.
  const running = wsState === "running";
  // Shared connection loader (also used by GitTab); reload() refetches + bumps global
  // listeners on connect/disconnect.
  const { conns, reload } = useConnections();
  // Behavior settings, loaded independently so a missing/old endpoint degrades in
  // place (hides that card's toggles) instead of blanking the connect UI. claude:
  // null = loading/unavailable, object = ready. agents: null = loading, false =
  // endpoint missing (older image), object = ready.
  const [claude, setClaude] = useState<any>(null);
  const [codex, setCodex] = useState<any>(null);
  const [agents, setAgents] = useState<any>(null);
  // rtk 効果（トークン節約の累積履歴）: rtk gain のワークスペース集計。WsBar から移設
  // したもので、コンテナ内 Agent が `rtk gain --format json` を叩いた結果。
  // ダイアログを開いた時に1回取得すれば十分（累積で低速変化のため常時ポーリング不要）。
  const [gain, setGain] = useState<any>(null);

  const loadSettings = useCallback(() => {
    api("api/claude/settings")
      .then((c) => setClaude(c && !c.error ? c : null))
      .catch(() => setClaude(null));
    api("api/codex/settings")
      .then((c) => setCodex(c && !c.error ? c : null))
      .catch(() => setCodex(null));
    api("api/agents/rtk")
      .then((a) => setAgents(a && !a.error ? a : false))
      .catch(() => setAgents(false));
    api("api/agents/rtk/gain")
      .then((d) => setGain(d && d.available && !d.error && d.summary ? d : null))
      .catch(() => setGain(null));
  }, []);

  // (Re)load when the workspace is running — including when it transitions
  // stopped→running while this dialog is open, so settings appear without a reopen.
  useEffect(() => {
    if (!running) return;
    reload();
    loadSettings();
  }, [running, reload, loadSettings]);

  // One save handler per settings endpoint — identical error contract, differing
  // only in path + setter.
  const mkUpdate =
    (path: string, setState: (d: any) => void) => async (patch: unknown) => {
      const d = await apiJSON(path, "PUT", patch);
      if (d && d.error) {
        toast(tr("common.save_failed_msg", { msg: d.error.message || "" }));
        return;
      }
      setState(d);
    };
  const updateClaude = mkUpdate("api/claude/settings", setClaude);
  const updateCodex = mkUpdate("api/codex/settings", setCodex);
  const updateAgents = mkUpdate("api/agents/rtk", setAgents);

  // Session prefs render in every state (stopped / loading / running) since they're
  // local, not container-backed.
  const sessionSettings = (
    <section className="ds-group">
      <h4 className="ds-title">{tr("agents.session")}</h4>
      <Row label={tr("agents.auto_title")}>
        <OnOff value={s.autoTitleSuggest} onChange={(v) => setSetting("autoTitleSuggest", v)} />
      </Row>
      <p className="muted ds-note">{tr("agents.note_auto_title")}</p>
      <Row label={tr("agents.output_language")}>
        <Choice
          value={s.outputLanguage}
          options={OUTPUT_LANGUAGES.map(([id, k]) => [id, tr(k)])}
          onChange={(v) => setSetting("outputLanguage", v)}
        />
      </Row>
      <p className="muted ds-note">{tr("agents.note_output_language")}</p>
      <Row label={tr("agents.assistant_agent")}>
        <Choice
          value={s.assistantAgent}
          options={ASSISTANT_AGENTS.map(([id, k]) => [id, tr(k)])}
          onChange={(v) => setSetting("assistantAgent", v)}
        />
      </Row>
      <p className="muted ds-note">{tr("agents.note_assistant_agent")}</p>
      <Row label={tr("agents.assistant_auto_turn")}>
        <OnOff value={s.assistantAutoTurn} onChange={(v) => setSetting("assistantAutoTurn", v)} />
      </Row>
      <p className="muted ds-note">{tr("agents.note_assistant_auto_turn")}</p>
    </section>
  );

  if (!running) {
    return (
      <>
        {sessionSettings}
        <EmptyState
          icon="debug-disconnect"
          title={tr("agents.ws_required_title")}
          hint={tr("agents.ws_required_hint")}
        >
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </EmptyState>
      </>
    );
  }
  if (!conns)
    return (
      <>
        {sessionSettings}
        <p className="muted pad">{tr("common.loading")}</p>
      </>
    );

  return (
    <div className="conns">
      {sessionSettings}
      {gain && <RtkGainPanel gain={gain} />}
      <p className="muted ds-note">{tr("agents.note_apply")}</p>
      <ClaudeCard st={conns.claude} reload={reload} claude={claude} updateClaude={updateClaude} />
      <CodexCard
        st={conns.codex}
        reload={reload}
        codex={codex}
        updateCodex={updateCodex}
        agents={agents}
        updateAgents={updateAgents}
      />
      <OpencodeCard
        st={conns.opencode}
        reload={reload}
        agents={agents}
        updateAgents={updateAgents}
      />
      {agents === false && <p className="ps-note">{tr("agents.rtk_unsupported")}</p>}
    </div>
  );
}

const RTK_HIST_N = 30; // sparkline shows ~the last month of daily savings

// RtkGainPanel: the workspace-level "rtk 効果" summary — a sparkline of daily tokens
// saved plus the cumulative total, average savings %, and the input→output / command
// totals. rtk keeps this history itself (the Agent shells out to `rtk gain --format
// json`); it's a per-container aggregate across this user's agents, so it lives here
// once — next to the per-agent RTK toggles below — rather than in the WsBar. Self-hides
// until gain reads back with something actually saved. Savings read as positive, so the
// sparkline / meter use the ok color (green), not the resource warn/crit scale.
function RtkGainPanel({ gain }: { gain: any }) {
  const tr = useT();
  const s = gain?.summary;
  const saved = s?.total_saved || 0;
  if (!s || saved <= 0) return null;
  const pct = Math.round(s.avg_savings_pct || 0);
  const series = (gain.daily || []).slice(-RTK_HIST_N).map((d: any) => d.saved_tokens);
  return (
    <section className="ds-group rtk-gain">
      <h4 className="ds-title">{tr("agents.rtk_gain_title")}</h4>
      <div className="rtk-gain-head">
        <Sparkline data={series} width={80} height={30} />
        <div className="rtk-gain-headline">
          <b>{fmtTok(saved)}</b>
          <span className="muted">{tr("agents.rtk_cumulative")}</span>
        </div>
      </div>
      <div className="rtk-gain-meter">
        <div className="wu-row-head">
          <span className="muted">{tr("agents.rtk_avg_pct")}</span>
          <span className="wu-pct">{pct}%</span>
        </div>
        <div className="wu-bar">
          <span className="wu-bar-fill" style={{ width: Math.min(100, pct) + "%" }} />
        </div>
      </div>
      <div className="ws-rtk-stats">
        <div className="ws-rtk-stat">
          <span className="muted">{tr("agents.rtk_in_out")}</span>
          <b>
            {fmtTok(s.total_input)} → {fmtTok(s.total_output)}
          </b>
        </div>
        <div className="ws-rtk-stat">
          <span className="muted">{tr("agents.rtk_commands")}</span>
          <b>{fmtNum(s.total_commands || 0)}</b>
        </div>
      </div>
      <p className="muted ds-note">{tr("agents.note_rtk_gain", { n: RTK_HIST_N })}</p>
    </section>
  );
}

// A labeled row for the client-side session settings (mirrors DisplayTab's Row).
function Row({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}

// A labeled settings row inside a card's .p-settings group.
function SettingRow({ label, sub, children }: { label: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="ps-row">
      <span className="ps-label">
        {label}
        {sub && <span className="sub">{sub}</span>}
      </span>
      {children}
    </div>
  );
}

// LaunchDefaults: the common, per-agent starting point. A repo's last-used values
// still win in the launch dialog, so these are useful global defaults without
// repeatedly overwriting deliberate per-repo choices.
function LaunchDefaults({ kind }: { kind: "claude" | "codex" | "opencode" }) {
  const s = useSettings();
  const tr = useT();
  const desc = agentOf(kind);
  const row = agentLaunchDefault(s, kind);
  const models = useModelOptions(kind) || [["", tr("common.default")]] as [string, string][];
  const efforts = useEffortOptions(kind, row.model);
  const update = (patch: Partial<typeof row>) => {
    const next = { ...row, ...patch };
    setSettings({
      agentLaunchDefaults: { ...s.agentLaunchDefaults, [kind]: next },
      // Keep the legacy key in sync while older Console images may still read it.
      ...(kind === "claude" ? { defaultModel: next.model } : {}),
    });
  };
  return (
    <>
      <SettingRow label={tr("agents.default_model")}>
        {/* opencode は候補が数十個になりセグメントだと敷き詰まるため、長いリストは Select に。 */}
        {models.length > 8 ? (
          <Select value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        ) : (
          <Choice value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        )}
      </SettingRow>
      <SettingRow label={tr("agents.default_effort")}>
        <Choice value={row.effort} options={efforts} onChange={(effort) => update({ effort })} />
      </SettingRow>
      {desc.caps.planMode && (
        <SettingRow label={tr("agents.start_mode")}>
          <Choice
            value={row.startMode}
            options={[["normal", desc.defaultModeLabel || tr("agents.mode_normal")], ["plan", "Plan"]]}
            onChange={(startMode) => update({ startMode: startMode === "plan" ? "plan" : "normal" })}
          />
        </SettingRow>
      )}
      <p className="ps-note">{tr("agents.note_launch_defaults")}</p>
    </>
  );
}

// RtkRow: the shared "RTK（トークン節約）" settings row — a toggle when the workspace
// has rtk, else an "unavailable" note. Used by all three agent cards.
function RtkRow({
  available,
  value,
  onChange,
}: {
  available?: boolean;
  value?: boolean;
  onChange: (v: boolean) => void;
}) {
  const tr = useT();
  return (
    <SettingRow label={tr("agents.rtk_row")}>
      {available ? (
        <OnOff value={value} onChange={onChange} />
      ) : (
        <span className="muted">{tr("agents.rtk_unavailable")}</span>
      )}
    </SettingRow>
  );
}

// Claude: OAuth connect (start → approve in a new tab → paste code → complete), plus
// its behavior settings (Remote Control / 通知 / RTK) once connected.
function ClaudeCard({
  st,
  reload,
  claude,
  updateClaude,
}: {
  st: any;
  reload: () => void;
  claude: any;
  updateClaude: (patch: unknown) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [flow, setFlow] = useState<any>(null); // { url, flow_id }
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  const start = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/claude/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.claude_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      window.open(res.url, "_blank", "noopener");
      setFlow({ url: res.url, flow_id: res.flow_id });
    } finally {
      setBusy(false);
    }
  };
  const complete = async () => {
    // OAuth コードは code#state 形式。オートフィル等でコード末尾に URL が
    // 連結されてしまった場合に備え、http(s):// 以降を切り落としてから送る。
    let c = code.trim();
    const u = c.search(/https?:\/\//i);
    if (u > 0) c = c.slice(0, u).trim();
    if (!c) return;
    setBusy(true);
    try {
      const r = await apiJSON("api/connections/claude/complete", "POST", { flow_id: flow.flow_id, code: c });
      if (r && r.error) {
        toast(tr("conn.connect_failed", { msg: String(r.error.message || r.error) }));
        return;
      }
      setFlow(null);
      setCode("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/claude", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="claude"
      name="Claude"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || "connected"}>
            {st.email || "connected"}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : flow ? (
        <>
          <div className="p-desc">{tr("agents.claude_desc_flow")}</div>
          <div className="p-body">
            <Hint>
              {tr("agents.claude_hint_1")}
              <a href={flow.url} target="_blank" rel="noopener" className="flow-link">
                {tr("agents.claude_signin_link")}
              </a>
              {tr("agents.claude_hint_2")}
            </Hint>
            <div className="flow">
              {/* 素の <input> だとパスワードマネージャ/ブラウザのオートフィルが働き、
                  貼り付けた OAuth コード（code#state 形式）の末尾に claude.com の URL を
                  差し込んで壊す事例がある。オートフィルを全面的に無効化しておく。 */}
              <input
                className="cinput"
                type="text"
                name="claude-oauth-code"
                placeholder={tr("agents.paste_code")}
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoComplete="off"
                autoCorrect="off"
                autoCapitalize="off"
                spellCheck={false}
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                autoFocus
              />
              <button disabled={busy} onClick={complete}>
                {tr("agents.complete")}
              </button>
            </div>
          </div>
        </>
      ) : (
        <>
          <div className="p-desc">{tr("agents.claude_desc")}</div>
          <div className="p-body">
            <button disabled={busy} onClick={start}>
              {tr("agents.oauth_connect")}
            </button>
          </div>
        </>
      )}
      <div className="p-settings">
        <div className="ps-title">{tr("agents.settings")}</div>
        <LaunchDefaults kind="claude" />
        {/* Remote Control / 通知 / RTK are workspace-level files (independent of Claude
            auth) — pre-settable, but need the api/claude/settings endpoint loaded. */}
        {claude && (
          <>
            <SettingRow label={tr("agents.remote_control")}>
              <OnOff
                value={claude.remoteControlAtStartup}
                onChange={(v) => updateClaude({ remoteControlAtStartup: v })}
              />
            </SettingRow>
            <SettingRow label={tr("agents.notifications")}>
              <OnOff
                value={claude.agentPushNotifEnabled}
                onChange={(v) => updateClaude({ agentPushNotifEnabled: v })}
              />
            </SettingRow>
            <RtkRow
              available={claude.rtk_available}
              value={claude.rtk_enabled}
              onChange={(v) => updateClaude({ rtk: v })}
            />
          </>
        )}
      </div>
    </ProviderCard>
  );
}

// Codex: ChatGPT subscription (device code) or API key, plus the RTK toggle
// (workspace-level; shown whenever settings load). codex has no command-rewrite
// hook so RTK there is instruction-based.
function CodexCard({
  st,
  reload,
  codex,
  updateCodex,
  agents,
  updateAgents,
}: {
  st: any;
  reload: () => void;
  codex: any;
  updateCodex: (patch: unknown) => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | device | key
  const [dev, setDev] = useState<any>(null); // { user_code, url, flow_id, status }
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);

  const startDevice = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/codex/device/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.codex_auth_failed", { msg: res?.error?.message || tr("agents.codex_device_disabled") }));
        return;
      }
      setMode("device");
      setDev({ user_code: res.user_code, url: res.url, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setDev((d: any) => ({ ...d, status: tr("git.oauth_expired") })),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/codex/device/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setMode("idle");
            reload();
            return { stop: true };
          }
          return { stop: false, nextMs: 2500 };
        },
      });
    } finally {
      setBusy(false);
    }
  };

  const saveKey = async () => {
    if (!key.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/codex/api-key", "POST", { key: key.trim() });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      setKey("");
      setMode("idle");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/codex", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="codex"
      name="Codex"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || (st.method === "apikey" ? tr("agents.codex_apikey_label") : "ChatGPT")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : mode === "device" && dev ? (
        <div className="p-body">
          <DeviceSteps code={dev.user_code} url={dev.url} status={dev.status} />
          <Hint>
            {tr("agents.codex_hint1_1")}
            <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
              {tr("agents.codex_settings_security")}
            </a>
            {tr("agents.codex_hint1_2")}
          </Hint>
        </div>
      ) : mode === "key" ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("agents.openai_key_placeholder")}
              value={key}
              onChange={(e) => setKey(e.target.value)}
              autoFocus
            />
            <button disabled={busy || !key.trim()} onClick={saveKey}>
              {tr("conn.connect")}
            </button>
            <button className="ghost" onClick={() => setMode("idle")}>
              {tr("common.back")}
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.codex_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startDevice}>
                <span className="p-opt-t">
                  {tr("agents.codex_connect_sub")} <span className="p-rec">{tr("git.recommended")}</span>
                </span>
                <span className="p-opt-s">{tr("agents.codex_sub_note")}</span>
              </button>
              <button type="button" className="p-opt" onClick={() => setMode("key")}>
                <span className="p-opt-t">{tr("agents.codex_connect_key")}</span>
                <span className="p-opt-s">{tr("agents.codex_key_note")}</span>
              </button>
            </div>
            <Hint>
              {tr("agents.codex_hint2_1")}
              <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
                {tr("agents.codex_settings_security")}
              </a>
              {tr("agents.codex_hint2_2")}
            </Hint>
          </div>
        </>
      )}
      {/* RTK is a workspace-level flag (independent of Codex auth) — pre-settable. */}
      <div className="p-settings">
        <div className="ps-title">{tr("agents.settings")}</div>
        <LaunchDefaults kind="codex" />
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
        {agents && agents !== false && (
            <>
              <RtkRow
                available={agents.rtk_available}
                value={agents.codex_rtk}
                onChange={(v) => updateAgents({ codex_rtk: v })}
              />
              <p className="ps-note">{tr("agents.codex_rtk_note")}</p>
            </>
        )}
      </div>
    </ProviderCard>
  );
}

// opencode: provider API keys (stored, injected as env at launch), plus the RTK and
// Web UI toggles. "Connected" = at least one key saved.
const OC_PRESETS = [
  ["go", "OpenCode Go", "OPENCODE_API_KEY"],
  ["anthropic", "Anthropic", "ANTHROPIC_API_KEY"],
  ["openai", "OpenAI", "OPENAI_API_KEY"],
  ["openrouter", "OpenRouter", "OPENROUTER_API_KEY"],
  ["google", "Google Gemini", "GEMINI_API_KEY"],
  ["custom", "", ""], // label resolved via i18n (agents.oc_custom) at render
];

function OpencodeCard({
  st,
  reload,
  agents,
  updateAgents,
}: {
  st: any;
  reload: () => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [preset, setPreset] = useState("go");
  const [customEnv, setCustomEnv] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const envs = st?.envs || [];
  const envName =
    preset === "custom" ? customEnv.trim().toUpperCase() : OC_PRESETS.find((p) => p[0] === preset)?.[2] || "";

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

  return (
    <ProviderCard
      id="opencode"
      name="opencode"
      status={<StatusPill on={envs.length > 0}>{envs.length > 0 ? tr("agents.oc_key_count", { count: envs.length }) : tr("conn.disconnected")}</StatusPill>}
    >
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
      <div className="p-settings">
        <div className="ps-title">{tr("agents.settings")}</div>
        <LaunchDefaults kind="opencode" />
        {agents && agents !== false && (
          <RtkRow
            available={agents.rtk_available}
            value={agents.opencode_rtk}
            onChange={(v) => updateAgents({ opencode_rtk: v })}
          />
        )}
      </div>
    </ProviderCard>
  );
}
