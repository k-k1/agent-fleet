import { useCallback, useEffect, useRef, useState } from "react";
import { useToast } from "../components/ToastProvider.jsx";
import type { ReactNode } from "react";
import { api, apiJSON, raw, ocwebURL } from "../api.js";
import { useApp } from "../state.jsx";
import EmptyState from "../components/EmptyState.jsx";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.jsx";

// AgentsTab is the per-agent home: for Claude / Codex / opencode it combines the
// CONNECTION (auth flow + status) and the BEHAVIOR settings (Remote Control / 通知 /
// RTK / opencode Web UI) in one card, so "set up Claude" is one place. Git-hosting
// providers live in their own GitTab. Connection auth goes through the Agent
// (secrets stored container-side); behavior settings via api/claude/settings +
// api/agents/rtk and apply to NEW sessions. Both need the workspace running.
export default function AgentsTab() {
  const toast = useToast();
  // opencode web state is shared with the WS bar via the app context, so toggling
  // here updates the bar's "open" entry immediately (and vice-versa).
  const { bumpConn, ocweb, setOcweb, refreshOcweb, wsState, startWs } = useApp();
  // Connection auth AND behavior settings both go through the in-container Agent
  // (proxyAgentREST → 502 when the workspace is stopped), so the whole tab requires
  // a running workspace — there's no CP-side DB to edit against while stopped.
  const running = wsState === "running";
  const [conns, setConns] = useState<any>(null); // api/connections (null = loading)
  // Behavior settings, loaded independently so a missing/old endpoint degrades in
  // place (hides that card's toggles) instead of blanking the connect UI. claude:
  // null = loading/unavailable, object = ready. agents: null = loading, false =
  // endpoint missing (older image), object = ready.
  const [claude, setClaude] = useState<any>(null);
  const [agents, setAgents] = useState<any>(null);

  // Connect/disconnect refreshes the connection state (+ global listeners). Behavior
  // settings don't change on connect, so they load once — no flash on reconnect.
  const reload = useCallback(() => {
    api("api/connections")
      .then(setConns)
      .catch(() => setConns({}));
    bumpConn();
  }, [bumpConn]);

  const loadSettings = useCallback(() => {
    api("api/claude/settings")
      .then((c) => setClaude(c && !c.error ? c : null))
      .catch(() => setClaude(null));
    api("api/agents/rtk")
      .then((a) => setAgents(a && !a.error ? a : false))
      .catch(() => setAgents(false));
    refreshOcweb();
  }, [refreshOcweb]);

  // (Re)load when the workspace is running — including when it transitions
  // stopped→running while this dialog is open, so settings appear without a reopen.
  useEffect(() => {
    if (!running) return;
    reload();
    loadSettings();
  }, [running, reload, loadSettings]);

  const updateClaude = async (patch: unknown) => {
    const d = await apiJSON("api/claude/settings", "PUT", patch);
    if (d && d.error) {
      toast("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setClaude(d);
  };
  const updateAgents = async (patch: unknown) => {
    const d = await apiJSON("api/agents/rtk", "PUT", patch);
    if (d && d.error) {
      toast("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setAgents(d);
  };
  const updateOcweb = async (patch: unknown) => {
    const d = await apiJSON("api/agents/opencode-web", "PUT", patch);
    if (d && d.error) {
      toast("保存に失敗: " + (d.error.message || ""));
      return;
    }
    setOcweb(d);
  };

  if (!running) {
    return (
      <EmptyState
        as="div"
        icon="debug-disconnect"
        message="設定はワークスペース内で実行されます"
        hint="接続とエージェント設定はコンテナ内の Agent / CLI を経由するため、ワークスペースの起動が必要です。"
        action={{
          label: wsState.endsWith("…") ? "起動中…" : "ワークスペースを起動",
          icon: "play",
          onClick: startWs,
          disabled: wsState.endsWith("…"),
        }}
      />
    );
  }
  if (!conns) return <p className="muted pad">読み込み中…</p>;

  return (
    <div className="conns">
      <p className="muted ds-note">
        接続の変更は即時、挙動設定は各エージェントの新しいセッションから反映されます。
      </p>
      <ClaudeCard st={conns.claude} reload={reload} claude={claude} updateClaude={updateClaude} />
      <CodexCard st={conns.codex} reload={reload} agents={agents} updateAgents={updateAgents} />
      <OpencodeCard
        st={conns.opencode}
        reload={reload}
        agents={agents}
        updateAgents={updateAgents}
        ocweb={ocweb}
        updateOcweb={updateOcweb}
      />
      {agents === false && (
        <p className="ps-note">
          このワークスペースのイメージはエージェント設定 API（rtk / opencode web）に未対応です。イメージを再ビルドして「作り直す」と有効になります。
        </p>
      )}
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

function OnOff({ value, onChange }: { value?: boolean; onChange: (v: boolean) => void }) {
  const opts: [boolean, string][] = [
    [true, "オン"],
    [false, "オフ"],
  ];
  return (
    <div className="seg choice-seg">
      {opts.map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          className={"seg-btn" + (!!value === v ? " active" : "")}
          onClick={() => onChange(v)}
        >
          {label}
        </button>
      ))}
    </div>
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
  const toast = useToast();
  const [flow, setFlow] = useState<any>(null); // { url, flow_id }
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  const start = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/claude/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast("Claude 認証開始に失敗: " + (res?.error?.message || ""));
        return;
      }
      window.open(res.url, "_blank", "noopener");
      setFlow({ url: res.url, flow_id: res.flow_id });
    } finally {
      setBusy(false);
    }
  };
  const complete = async () => {
    if (!code.trim()) return;
    setBusy(true);
    try {
      const r = await apiJSON("api/connections/claude/complete", "POST", { flow_id: flow.flow_id, code: code.trim() });
      if (r && r.error) {
        toast("接続に失敗: " + (r.error.message || r.error));
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
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
          <div className="p-desc">Claude Code の OAuth 接続。サインインは新しいタブで開きます。</div>
          <div className="p-body">
            <Hint>
              タブが自動で開かない場合は{" "}
              <a href={flow.url} target="_blank" rel="noopener" className="flow-link">
                サインインリンク ↗
              </a>{" "}
              から。承認後にコードを貼り付けます。
            </Hint>
            <div className="flow">
              <input
                className="cinput"
                placeholder="コードを貼付"
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoFocus
              />
              <button disabled={busy} onClick={complete}>
                完了
              </button>
            </div>
          </div>
        </>
      ) : (
        <>
          <div className="p-desc">Claude Code の OAuth 接続。承認後にコードを貼り付けて完了します。</div>
          <div className="p-body">
            <button disabled={busy} onClick={start}>
              OAuth 接続
            </button>
          </div>
        </>
      )}
      {/* Behavior settings are a workspace-level file (independent of Claude auth),
          so they show whenever loaded — you can pre-set them before connecting. */}
      {claude && (
        <div className="p-settings">
          <div className="ps-title">設定</div>
          <SettingRow label="リモートコントロール">
            <OnOff
              value={claude.remoteControlAtStartup}
              onChange={(v) => updateClaude({ remoteControlAtStartup: v })}
            />
          </SettingRow>
          <SettingRow label="通知">
            <OnOff
              value={claude.agentPushNotifEnabled}
              onChange={(v) => updateClaude({ agentPushNotifEnabled: v })}
            />
          </SettingRow>
          <SettingRow label="RTK（トークン節約）">
            {claude.rtk_available ? (
              <OnOff value={claude.rtk_enabled} onChange={(v) => updateClaude({ rtk: v })} />
            ) : (
              <span className="muted">この workspace に rtk がありません</span>
            )}
          </SettingRow>
        </div>
      )}
    </ProviderCard>
  );
}

// Codex: ChatGPT subscription (device code) or API key, plus the RTK toggle
// (workspace-level; shown whenever settings load). codex has no command-rewrite
// hook so RTK there is instruction-based.
function CodexCard({
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
  const toast = useToast();
  const [mode, setMode] = useState("idle"); // idle | device | key
  const [dev, setDev] = useState<any>(null); // { user_code, url, flow_id, status }
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const alive = useRef(true);
  useEffect(() => () => {
    alive.current = false;
  }, []);

  const startDevice = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/codex/device/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast("Codex 認証開始に失敗: " + (res?.error?.message || "device code ログインが無効かもしれません"));
        return;
      }
      setMode("device");
      setDev({ user_code: res.user_code, url: res.url, flow_id: res.flow_id, status: "承認待ち…" });
      const deadline = Date.now() + 15 * 60 * 1000;
      const tick = async () => {
        if (!alive.current) return;
        if (Date.now() > deadline) {
          setDev((d: any) => ({ ...d, status: "期限切れ。やり直してください" }));
          return;
        }
        let p;
        try {
          p = await apiJSON("api/connections/codex/device/poll", "POST", { flow_id: res.flow_id });
        } catch {
          p = null;
        }
        if (p && p.connected) {
          setMode("idle");
          reload();
          return;
        }
        setTimeout(tick, 2500);
      };
      setTimeout(tick, 3000);
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
        toast("接続に失敗: " + (res.error.message || res.error));
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

  const rtkReady = agents && agents !== false;
  return (
    <ProviderCard
      id="codex"
      name="Codex"
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || (st.method === "apikey" ? "API キー" : "ChatGPT")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : mode === "device" && dev ? (
        <div className="p-body">
          <DeviceSteps code={dev.user_code} url={dev.url} status={dev.status} />
          <Hint>
            承認しても進まない場合は、ChatGPT の{" "}
            <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
              設定 &gt; セキュリティ
            </a>{" "}
            で「Codex に対してデバイスコード認証を有効にする」がオンか確認してください。
          </Hint>
        </div>
      ) : mode === "key" ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder="OpenAI API キー (sk-…)"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              autoFocus
            />
            <button disabled={busy || !key.trim()} onClick={saveKey}>
              接続
            </button>
            <button className="ghost" onClick={() => setMode("idle")}>
              戻る
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="p-desc">
            ChatGPT サブスク（推奨）か OpenAI API キーで接続。
          </div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startDevice}>
                <span className="p-opt-t">
                  ChatGPT サブスクで接続 <span className="p-rec">推奨</span>
                </span>
                <span className="p-opt-s">Plus / Pro の枠を使用。追加課金なし。</span>
              </button>
              <button type="button" className="p-opt" onClick={() => setMode("key")}>
                <span className="p-opt-t">API キーで接続</span>
                <span className="p-opt-s">OpenAI API の従量課金（sk-…）。</span>
              </button>
            </div>
            <Hint>
              ChatGPT サブスクで接続するには、先に ChatGPT の{" "}
              <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
                設定 &gt; セキュリティ
              </a>{" "}
              で「Codex に対してデバイスコード認証を有効にする」をオンにしてください。
            </Hint>
          </div>
        </>
      )}
      {/* RTK is a workspace-level flag (independent of Codex auth) — pre-settable. */}
      {rtkReady && (
        <div className="p-settings">
          <div className="ps-title">設定</div>
          <SettingRow label="RTK（トークン節約）">
            {agents.rtk_available ? (
              <OnOff value={agents.codex_rtk} onChange={(v) => updateAgents({ codex_rtk: v })} />
            ) : (
              <span className="muted">この workspace に rtk がありません</span>
            )}
          </SettingRow>
          <p className="ps-note">
            codex はコマンド書換フックを持たないため指示ベース（ベストエフォート）。AGENTS.md で rtk 利用を促すだけで、強制ではありません。
          </p>
        </div>
      )}
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
  ["custom", "カスタム…", ""],
];

function OpencodeCard({
  st,
  reload,
  agents,
  updateAgents,
  ocweb,
  updateOcweb,
}: {
  st: any;
  reload: () => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
  ocweb: any;
  updateOcweb: (patch: unknown) => void;
}) {
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
        toast("保存に失敗: " + (res.error.message || res.error));
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

  const rtkReady = agents && agents !== false;
  return (
    <ProviderCard
      id="opencode"
      name="opencode"
      status={<StatusPill on={envs.length > 0}>{envs.length > 0 ? `${envs.length} キー` : "未接続"}</StatusPill>}
    >
      <div className="p-desc">複数プロバイダの API キーを保存し、opencode 起動時に env として注入します。</div>
      <div className="p-body">
        {preset === "go" && (
          <Hint>
            <a href="https://opencode.ai/auth" target="_blank" rel="noopener" className="flow-link">
              opencode.ai/auth
            </a>{" "}
            でサインイン → 課金設定 → API キーを発行して貼り付け（同じキーで Zen も利用可）。
          </Hint>
        )}
        {envs.length > 0 && (
          <ul className="oc-keys">
            {envs.map((e: string) => (
              <li key={e}>
                <code>{e}</code>
                <button className="icon danger" title="削除" onClick={() => remove(e)}>
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
                {label}
              </option>
            ))}
          </select>
          {preset === "custom" && (
            <input
              className="cinput"
              placeholder="ENV 名 (例 GROQ_API_KEY)"
              value={customEnv}
              onChange={(e) => setCustomEnv(e.target.value)}
            />
          )}
          <input
            className="cinput"
            type="password"
            placeholder={envName ? envName + " の値" : "API キー"}
            value={key}
            onChange={(e) => setKey(e.target.value)}
          />
          <button disabled={busy || !envName || !key.trim()} onClick={add}>
            接続
          </button>
        </div>
      </div>
      {rtkReady && (
        <div className="p-settings">
          <div className="ps-title">設定</div>
          <SettingRow label="RTK（トークン節約）">
            {agents.rtk_available ? (
              <OnOff value={agents.opencode_rtk} onChange={(v) => updateAgents({ opencode_rtk: v })} />
            ) : (
              <span className="muted">この workspace に rtk がありません</span>
            )}
          </SettingRow>
          {ocweb && ocweb.available && (
            <SettingRow label="Web UI" sub="ブラウザ版 opencode を serve と同時に起動。全セッションを Web UI で扱えます。">
              {ocweb.enabled &&
                (ocweb.running ? (
                  <button className="ghost" onClick={() => window.open(ocwebURL(), "_blank", "noopener")}>
                    開く ↗
                  </button>
                ) : (
                  <span className="muted">起動中…</span>
                ))}
              <OnOff value={ocweb.enabled} onChange={(v) => updateOcweb({ enabled: v })} />
            </SettingRow>
          )}
        </div>
      )}
    </ProviderCard>
  );
}
