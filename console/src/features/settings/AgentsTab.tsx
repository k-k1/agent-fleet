import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import type { ReactNode } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { Choice, OnOff, Row, Select } from "./controls.tsx";
import {
  agentLaunchDefault,
  useSettings,
  setSetting,
  setSettings,
} from "../../lib/settings.ts";
import { useEffortOptions, useModelOptions } from "../../lib/agentModels.ts";
import { agentOf } from "../../agents/registry.ts";
import { useConnections } from "./useConnections.ts";
import { useSettingsUI } from "./store.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { usePolling } from "./usePolling.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.tsx";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";

// AgentsTab is the per-agent home. Each card is split into two levels so the two
// concerns read as a hierarchy rather than one flat block:
//   1. CONNECTION (top) — the auth flow + status. Needs the workspace running (secrets
//      are stored container-side via the Agent; the REST proxy 502s while stopped).
//   2. 動作設定 (a collapsed disclosure, below) — the per-agent BEHAVIOR: client-side
//      launch defaults (model / effort / start-mode, in the local settings store) plus
//      the container-backed toggles (Remote Control / 通知 / RTK / nudge). Launch
//      defaults are client-only, so the cards render even while stopped — you can set a
//      default model before starting; only the connection + runtime toggles wait for the
//      workspace. Git-hosting agents live in GitTab; the rtk 効果 analytics that used to
//      sit here moved out (monitoring is not a setting).
export function AgentsTab() {
  const tr = useT();
  const toast = useToast();
  // Client-side session pref (タイトル自動提案) — persisted in the local settings
  // store, so it shows regardless of workspace state (unlike the container-backed
  // toggles, which need the Agent/CLI). 既定モデルは各カードの 動作設定 内。
  const s = useSettings();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  // Connection auth AND the behavior toggles both go through the in-container Agent
  // (proxyAgentREST → 502 when stopped), so those wait for a running workspace. The
  // client launch defaults do not (see CardSettings, rendered in every state).
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
    </section>
  );

  // While running but the connection snapshot hasn't loaded yet, hold the cards back a
  // beat (avoids a flash of "未接続" idle flows). Stopped renders the cards immediately
  // (degraded): their launch defaults are reachable, connection waits for start.
  const loading = running && !conns;

  return (
    <div className="conns">
      {sessionSettings}
      {!running && (
        <div className="agents-ws-hint">
          <p className="muted ds-note">{tr("agents.ws_required_hint")}</p>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </div>
      )}
      {loading ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <>
          {running && <p className="muted ds-note">{tr("agents.note_apply")}</p>}
          <ClaudeCard running={running} st={conns?.claude} reload={reload} claude={claude} updateClaude={updateClaude} />
          <CodexCard
            running={running}
            st={conns?.codex}
            reload={reload}
            codex={codex}
            updateCodex={updateCodex}
            agents={agents}
            updateAgents={updateAgents}
          />
          <CopilotCard running={running} st={conns?.copilot} agents={agents} updateAgents={updateAgents} />
          <AgyCard running={running} st={conns?.agy} reload={reload} agents={agents} updateAgents={updateAgents} />
          <OpencodeCard
            running={running}
            st={conns?.opencode}
            reload={reload}
            agents={agents}
            updateAgents={updateAgents}
          />
          {running && agents === false && <p className="ps-note">{tr("agents.rtk_unsupported")}</p>}
        </>
      )}
    </div>
  );
}

// A labeled settings row inside a card's 動作設定 group.
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

// CardSettings: the per-agent 動作設定 disclosure — collapsed by default so the card
// reads as "connect" first, with behavior a deliberate second level. Its body is the
// client launch defaults (always usable) + any container-backed toggles the card passes.
function CardSettings({ children }: { children?: ReactNode }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className={"p-settings" + (open ? " open" : "")}>
      <button type="button" className="ps-disclosure" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="ps-caret" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
        {tr("agents.behavior")}
      </button>
      {open && <div className="ps-body">{children}</div>}
    </div>
  );
}

// The connection body shown while the workspace is stopped: launch defaults below stay
// reachable, but the auth flow (Agent-proxied) waits for start.
function ConnPaused() {
  const tr = useT();
  return <div className="p-desc muted">{tr("agents.conn_paused")}</div>;
}

// LaunchDefaults: the common, per-agent starting point. A repo's last-used values
// still win in the launch dialog, so these are useful global defaults without
// repeatedly overwriting deliberate per-repo choices.
function LaunchDefaults({ kind }: { kind: "claude" | "codex" | "agy" | "opencode" | "copilot" }) {
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
      {/* agy は effort 相当がモデル名に織り込まれている（(Medium) 等）ため行ごと出さない。 */}
      {desc.caps.effort && (
        <SettingRow label={tr("agents.default_effort")}>
          <Choice value={row.effort} options={efforts} onChange={(effort) => update({ effort })} />
        </SettingRow>
      )}
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
  running,
  st,
  reload,
  claude,
  updateClaude,
}: {
  running: boolean;
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
      name={kindDisplayName("claude")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : st?.connected ? (
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
      <CardSettings>
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
      </CardSettings>
    </ProviderCard>
  );
}

// agy (Antigravity CLI, docs/32): claude-style OAuth connect (start → approve in a
// new tab → paste code → complete) with an auth-method selector (M1 offers Google
// OAuth only; the GCP-project method lands with M2), plus the shared RTK toggle so
// the card reads like the other agents'. The 実験枠 label is a 採用条件 (docs/32
// Track C-3): the Starter pool is tiny and shared with the IDE/Jules wallet, so the
// card must always say so. The quota gauge (残量%) lives in the WS bar next to the
// Claude / Codex usage chips. On unsupported hosts (no RDRAND) the card shows why
// instead of the connect flow.
// CopilotCard: GitHub Copilot CLI（docs/36）。専用の認証フローを持たない —
// GitHub 連携（gh 透過認証）に相乗りするので、状態表示と起動既定のみ。接続/切断は
// 連携タブの GitHub 側で行う。
function CopilotCard({
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

function AgyCard({
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
  const [flow, setFlow] = useState<any>(null); // { url, flow_id }
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  // Fixed to "oauth" while M1; the selector ships disabled so the M2 wiring
  // (method: "gcp-project" + project_id) has its place already cut.
  const method = "oauth";

  const start = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/agy/start", "POST", { method });
      if (!res || res.error || !res.url) {
        toast(tr("agents.agy_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      window.open(res.url, "_blank", "noopener");
      setFlow({ url: res.url, flow_id: res.flow_id });
    } finally {
      setBusy(false);
    }
  };
  const complete = async () => {
    // Same autofill guard as ClaudeCard: cut anything from http(s):// on, in case
    // a password manager appended a URL to the pasted authorization code.
    let c = code.trim();
    const u = c.search(/https?:\/\//i);
    if (u > 0) c = c.slice(0, u).trim();
    if (!c) return;
    setBusy(true);
    try {
      const r = await apiJSON("api/connections/agy/complete", "POST", { flow_id: flow.flow_id, code: c });
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
    await raw("api/connections/agy", { method: "DELETE" });
    reload();
  };

  const unsupported = st?.supported === false;
  return (
    <ProviderCard
      id="agy"
      name={kindDisplayName("agy")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {/* 実験枠 label — always visible, connected or not (採用条件). */}
      <p className="ps-note ps-note-warn agy-exp">{tr("agents.agy_exp_label")}</p>
      {!running ? (
        <ConnPaused />
      ) : unsupported ? (
        <div className="p-desc">{tr("agents.agy_unsupported", { reason: st?.reason || "" })}</div>
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || "connected"}>
            {st.email || "connected"}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : flow ? (
        <>
          <div className="p-desc">{tr("agents.agy_desc_flow")}</div>
          <div className="p-body">
            <Hint>
              {tr("agents.claude_hint_1")}
              <a href={flow.url} target="_blank" rel="noopener" className="flow-link">
                {tr("agents.claude_signin_link")}
              </a>
              {tr("agents.claude_hint_2")}
            </Hint>
            <div className="flow">
              <input
                className="cinput"
                type="text"
                name="agy-oauth-code"
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
          <div className="p-desc">{tr("agents.agy_desc")}</div>
          <div className="p-body">
            <div className="flow">
              <select className="cinput" value={method} disabled title={tr("agents.agy_method_label")}>
                <option value="oauth">{tr("agents.agy_method_oauth")}</option>
                <option value="gcp-project" disabled>
                  {tr("agents.agy_method_gcp")}
                </option>
              </select>
              <button disabled={busy} onClick={start}>
                {tr("agents.oauth_connect")}
              </button>
            </div>
          </div>
        </>
      )}
      {/* RTK is a workspace-level flag (independent of agy auth) — pre-settable,
          same block shape as the Codex / opencode cards. */}
      <CardSettings>
        <LaunchDefaults kind="agy" />
        {agents && agents !== false && (
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

// Codex: ChatGPT subscription (device code) or API key, plus the RTK toggle
// (workspace-level; shown whenever settings load). codex has no command-rewrite
// hook so RTK there is instruction-based.
function CodexCard({
  running,
  st,
  reload,
  codex,
  updateCodex,
  agents,
  updateAgents,
}: {
  running: boolean;
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
      name={kindDisplayName("codex")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : st?.connected ? (
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
      <CardSettings>
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
      </CardSettings>
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
      name={kindDisplayName("opencode")}
      status={
        running ? (
          <StatusPill on={envs.length > 0}>
            {envs.length > 0 ? tr("agents.oc_key_count", { count: envs.length }) : tr("conn.disconnected")}
          </StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : (
        <>
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
        </>
      )}
      <CardSettings>
        <LaunchDefaults kind="opencode" />
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
