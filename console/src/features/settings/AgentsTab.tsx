import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import type { ReactNode } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { Sparkline } from "../../ui/Sparkline.tsx";
import { fmtTok } from "../../lib/fmttok.ts";
import { Choice, OnOff } from "./controls.tsx";
import {
  useSettings,
  setSetting,
  CLAUDE_MODELS,
  OUTPUT_LANGUAGES,
  ASSISTANT_AGENTS,
} from "../../lib/settings.ts";
import { useConnections } from "./useConnections.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { usePolling } from "./usePolling.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.tsx";

// AgentsTab is the per-agent home: for Claude / Codex / opencode it combines the
// CONNECTION (auth flow + status) and the BEHAVIOR settings (Remote Control / 通知 /
// RTK / opencode Web UI) in one card, so "set up Claude" is one place. Git-hosting
// providers live in their own GitTab. Connection auth goes through the Agent
// (secrets stored container-side); behavior settings via api/claude/settings +
// api/agents/rtk and apply to NEW sessions. Both need the workspace running.
export function AgentsTab() {
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
        toast("保存に失敗: " + (d.error.message || ""));
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
      <h4 className="ds-title">セッション</h4>
      <Row label="タイトル自動提案">
        <OnOff value={s.autoTitleSuggest} onChange={(v) => setSetting("autoTitleSuggest", v)} />
      </Row>
      <p className="muted ds-note">
        タイトル未設定のセッションで数回やり取りしたら、AIが短いタイトル案をチャット上部に表示します。
      </p>
      <Row label="アシスタントの回答言語">
        <Choice
          value={s.outputLanguage}
          options={OUTPUT_LANGUAGES}
          onChange={(v) => setSetting("outputLanguage", v)}
        />
      </Row>
      <p className="muted ds-note">
        アシスタント・チャットの回答言語です。「入力に合わせる」は、渡した文章や質問の言語に合わせて返します。
        日本語／English を選ぶと、他言語の文章でもその言語で回答します（翻訳アシスタントは対象外）。
      </p>
      <Row label="アシスタントのエージェント">
        <Choice
          value={s.assistantAgent}
          options={ASSISTANT_AGENTS}
          onChange={(v) => setSetting("assistantAgent", v)}
        />
      </Row>
      <p className="muted ds-note">
        アシスタント・チャットとタイトル案の生成を動かす CLI です。「自動」は接続済みのものを Claude → Codex →
        opencode の順で選びます。固定した CLI が未接続のときは自動選択に戻ります。反映はビルトインアシスタントの
        新しい会話から（カスタムアシスタントは各自のエージェント設定が優先）。
      </p>
    </section>
  );

  if (!running) {
    return (
      <>
        {sessionSettings}
        <EmptyState
          icon="debug-disconnect"
          title="設定はワークスペース内で実行されます"
          hint="接続とエージェント設定はコンテナ内の Agent / CLI を経由するため、ワークスペースの起動が必要です。"
        >
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? "起動中…" : "ワークスペースを起動"}
          </Button>
        </EmptyState>
      </>
    );
  }
  if (!conns)
    return (
      <>
        {sessionSettings}
        <p className="muted pad">読み込み中…</p>
      </>
    );

  return (
    <div className="conns">
      {sessionSettings}
      {gain && <RtkGainPanel gain={gain} />}
      <p className="muted ds-note">
        接続の変更は即時、挙動設定は各エージェントの新しいセッションから反映されます。
      </p>
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
      {agents === false && (
        <p className="ps-note">
          このワークスペースのイメージはエージェント設定 API（rtk）に未対応です。イメージを再ビルドして「作り直す」と有効になります。
        </p>
      )}
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
  const s = gain?.summary;
  const saved = s?.total_saved || 0;
  if (!s || saved <= 0) return null;
  const pct = Math.round(s.avg_savings_pct || 0);
  const series = (gain.daily || []).slice(-RTK_HIST_N).map((d: any) => d.saved_tokens);
  return (
    <section className="ds-group rtk-gain">
      <h4 className="ds-title">rtk 効果（トークン節約）</h4>
      <div className="rtk-gain-head">
        <Sparkline data={series} width={80} height={30} />
        <div className="rtk-gain-headline">
          <b>{fmtTok(saved)}</b>
          <span className="muted"> 累計節約</span>
        </div>
      </div>
      <div className="rtk-gain-meter">
        <div className="wu-row-head">
          <span className="muted">平均節約率</span>
          <span className="wu-pct">{pct}%</span>
        </div>
        <div className="wu-bar">
          <span className="wu-bar-fill" style={{ width: Math.min(100, pct) + "%" }} />
        </div>
      </div>
      <div className="ws-rtk-stats">
        <div className="ws-rtk-stat">
          <span className="muted">入力 → 出力</span>
          <b>
            {fmtTok(s.total_input)} → {fmtTok(s.total_output)}
          </b>
        </div>
        <div className="ws-rtk-stat">
          <span className="muted">実行コマンド</span>
          <b>{(s.total_commands || 0).toLocaleString()}</b>
        </div>
      </div>
      <p className="muted ds-note">
        rtk がコマンド出力を圧縮して節約したトークン量です（直近 {RTK_HIST_N} 日の推移）。
        計上範囲は各エージェントの rtk 設定（下記）により変わります。
      </p>
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
  return (
    <SettingRow label="RTK（トークン節約）">
      {available ? (
        <OnOff value={value} onChange={onChange} />
      ) : (
        <span className="muted">この workspace に rtk がありません</span>
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
  const toast = useToast();
  // 既定モデルは claude 起動時の初期モデル（クライアント側設定）。この card 内に置く。
  const s = useSettings();
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
      <div className="p-settings">
        <div className="ps-title">設定</div>
        {/* 既定モデルはクライアント側設定なので claude 認証・設定の読み込み状態に依らず表示。 */}
        <SettingRow label="既定モデル">
          <Choice
            value={s.defaultModel}
            options={CLAUDE_MODELS}
            onChange={(v) => setSetting("defaultModel", v)}
          />
        </SettingRow>
        <p className="ps-note">
          claude セッションを起動するとき（作成ダイアログ・リポジトリの起動）このモデルを初期選択にします。
          リポジトリで前回使ったモデルがあればそちらを優先します。
        </p>
        {/* Remote Control / 通知 / RTK are workspace-level files (independent of Claude
            auth) — pre-settable, but need the api/claude/settings endpoint loaded. */}
        {claude && (
          <>
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
        toast("Codex 認証開始に失敗: " + (res?.error?.message || "device code ログインが無効かもしれません"));
        return;
      }
      setMode("device");
      setDev({ user_code: res.user_code, url: res.url, flow_id: res.flow_id, status: "承認待ち…" });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setDev((d: any) => ({ ...d, status: "期限切れ。やり直してください" })),
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

  const settingsReady = (agents && agents !== false) || codex;
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
      {settingsReady && (
        <div className="p-settings">
          <div className="ps-title">設定</div>
          {codex && (
            <>
              <SettingRow label="利用制限時のモデル切替案内">
                <OnOff
                  value={codex.rate_limit_model_nudge}
                  onChange={(v) => updateCodex({ rate_limit_model_nudge: v })}
                />
              </SettingRow>
              <p className="ps-note">
                オフにすると、利用制限が近いときに軽量モデルへの切替を勧める Codex の案内を表示しません。
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
              <p className="ps-note">
                codex はコマンド書換フックを持たないため指示ベース（ベストエフォート）。AGENTS.md で rtk 利用を促すだけで、強制ではありません。
              </p>
            </>
          )}
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
}: {
  st: any;
  reload: () => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
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
          <RtkRow
            available={agents.rtk_available}
            value={agents.opencode_rtk}
            onChange={(v) => updateAgents({ opencode_rtk: v })}
          />
        </div>
      )}
    </ProviderCard>
  );
}
