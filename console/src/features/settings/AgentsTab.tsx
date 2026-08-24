import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../ui/ToastProvider.tsx";
import type { ReactNode } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { Button } from "../../ui/Button.tsx";
import { ModelPicker } from "../../ui/ModelPicker.tsx";
import { Choice, OnOff, Row, Select } from "./controls.tsx";
import {
  agentLaunchDefault,
  useSettings,
  setSetting,
  setSettings,
  ASSISTANT_RECOMMENDED_MODEL,
  CLAUDE_MODELS,
} from "../../lib/settings.ts";
import { useEffortOptions, useModelOptions } from "../../lib/agentModels.ts";
import { modelMatchesHidden } from "../../lib/modelDeny.ts";
import { forgetHiddenRepoModels } from "../../lib/repoLast.ts";
import { agentOf, nonPlanModeLabel } from "../../agents/registry.ts";
import { useConnections } from "./useConnections.ts";
import { useSettingsUI } from "./store.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { usePolling } from "./usePolling.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, ReauthButton, IssueLink } from "./providerCard.tsx";
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
//      sit here lives in the 使用量 tab (features/usage) — monitoring is not a setting.
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
      {/* セッション間メッセージ（docs/58 / ADR 0041）。カードの中ではなくここに置くのは、
          af 自身の MCP が配られる 7 kind すべてに効く設定で、特定のエージェントの
          設定ではないから（claude カードに入れると claude 限定に見える）。 */}
      <Row label={tr("agents.peer_messaging")}>
        <OnOff value={s.peerMessaging} onChange={(v) => setSetting("peerMessaging", v)} />
      </Row>
      <p className="muted ds-note">{tr("agents.note_peer_messaging")}</p>
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
          <CursorCard running={running} st={conns?.cursor} reload={reload} />
          <CopilotCard running={running} st={conns?.copilot} agents={agents} updateAgents={updateAgents} />
          <KiroCard running={running} st={conns?.kiro} reload={reload} />
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

// ThinkingRow: 「思考を展開して表示」（kind スコープ・既定オフ）。ミラーの「思考」ブロックは
// 既定で畳んだまま出るので、思考を常に読みたい人だけがここで開いた状態を既定にできる。
// 思考を出す backend（codex / opencode）のカードにだけ置き、kind ごとに独立して効く。
function ThinkingRow({ kind }: { kind: string }) {
  const s = useSettings();
  const tr = useT();
  return (
    <>
      <SettingRow label={tr("agents.expand_thinking")}>
        <OnOff
          value={s.expandThinking[kind] === true}
          onChange={(v) => setSettings({ expandThinking: { ...s.expandThinking, [kind]: v } })}
        />
      </SettingRow>
      <p className="ps-note">{tr("agents.expand_thinking_note")}</p>
    </>
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
function LaunchDefaults({ kind }: { kind: "claude" | "codex" | "cursor" | "kiro" | "agy" | "opencode" | "copilot" }) {
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
        {kind === "claude" ? (
          <ModelPicker kind={kind} model={row.model} onChange={(model) => update({ model, effort: "" })} />
        ) : models.length > 8 ? (
          <Select value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        ) : (
          <Choice value={row.model} options={models} onChange={(model) => update({ model, effort: "" })} />
        )}
      </SettingRow>
      {kind === "claude" && <ClaudeCustomModelsRow />}
      {/* agy は effort 相当がモデル名に織り込まれている（(Medium) 等）ため行ごと出さない。 */}
      {desc.caps.effort && (
        <SettingRow label={tr("agents.default_effort")}>
          <Choice value={row.effort} options={efforts} onChange={(effort) => update({ effort })} />
        </SettingRow>
      )}
      <HiddenModelsRow kind={kind} />
      {/* planMode（チャットの plan トグル）が無くても tuiStartMode（plan 起動）対応なら
          既定の開始モードを設定できる（cursor/copilot/kiro — 起動 UI のゲートと同型）。 */}
      {(desc.caps.planMode || desc.caps.tuiStartMode) && (
        <SettingRow label={tr("agents.start_mode")}>
          <Choice
            value={row.startMode}
            options={[["normal", nonPlanModeLabel(kind, row.skipPermissions) || tr("agents.mode_normal")], ["plan", "Plan"]]}
            onChange={(startMode) => update({ startMode: startMode === "plan" ? "plan" : "normal" })}
          />
        </SettingRow>
      )}
      {/* 権限確認をスキップするか（docs/76）。承認待ちを Console から答えられる kind
          （claude / cursor / copilot / kiro / agy）だけに出す — 答えられない kind で
          オフにできると、固まったセッションを作れてしまう。 */}
      {desc.caps.permissionChoice && (
        <SettingRow label={tr("agents.skip_permissions")} sub={tr("agents.skip_permissions_sub")}>
          <OnOff value={row.skipPermissions} onChange={(skipPermissions) => update({ skipPermissions })} />
        </SettingRow>
      )}
      {desc.caps.permissionChoice && !row.skipPermissions && (
        <p className="ps-note">{tr("agents.skip_permissions_off_note")}</p>
      )}
      <p className="ps-note">{tr("agents.note_launch_defaults")}</p>
    </>
  );
}

// Claude Code OAuth exposes no account-aware model catalog. These user-owned full ids are
// therefore the durable catalog shared by launch pickers and MCP list_models.
function ClaudeCustomModelsRow() {
  const s = useSettings();
  const tr = useT();
  const [value, setValue] = useState("");
  const id = value.trim();
  const duplicate = s.claudeCustomModels.some((m) => m.toLowerCase() === id.toLowerCase());
  const valid = /^claude-[a-z0-9][a-z0-9._\-[\]]*$/i.test(id) && !duplicate;
  const add = () => {
    if (!valid) return;
    setSettings({ claudeCustomModels: [...s.claudeCustomModels, id] });
    setValue("");
  };
  return (
    <SettingRow label={tr("agents.claude_custom_models")} sub={tr("agents.claude_custom_models_sub")}>
      <div className="hidden-models">
        {s.claudeCustomModels.length > 0 && (
          <div className="hm-chips">
            {s.claudeCustomModels.map((model) => (
              <span key={model} className="hm-chip">
                {model}
                <button
                  type="button"
                  className="hm-chip-x"
                  aria-label={tr("agents.claude_custom_models_remove", { model })}
                  onClick={() => setSettings({ claudeCustomModels: s.claudeCustomModels.filter((m) => m !== model) })}
                >×</button>
              </span>
            ))}
          </div>
        )}
        <div className="ui-field-row">
          <input
            className="ds-select"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }}
            placeholder="claude-opus-4-8"
            aria-label={tr("agents.claude_custom_models_input")}
            spellCheck={false}
          />
          <Button small icon="add" disabled={!valid} onClick={add}>{tr("agents.claude_custom_models_add")}</Button>
        </div>
      </div>
    </SettingRow>
  );
}

// HiddenModelsRow:「使わないモデル」— kind ごとの除外リスト（settings.hiddenModels）。
// 動機は課金事故の予防で、Claude の Team プランでは Fable が API クレジット扱いになる。
// 除外すると（Agent が同じ ui-prefs を読むので）Console のピッカーからも MCP の
// list_models からも消え、除外モデルを指定した起動は Agent 側で断られる。
//
// 編集 UI は「現在の除外＝チップ（×で解除）」＋「追加＝残っている候補の select」。
// 追加側の候補が既に絞り込み済みなので、生カタログを別途持たなくても往復できる。
function HiddenModelsRow({ kind }: { kind: string }) {
  const tr = useT();
  const s = useSettings();
  const visible = (useModelOptions(kind) || []).filter(([id]) => id); // 「既定」は id ではない
  const hidden = s.hiddenModels?.[kind] || [];
  // claude は固定4ティアで「既定」の選択肢が無い＝全部隠すと起動できるモデルが消える。
  // 最後の1つは隠させない（Agent 側にも同じフェイルセーフがあるが、行き止まりの状態を
  // 作らせない方が親切）。
  const canAdd = visible.length > (kind === "claude" ? 1 : 0);

  const apply = (next: string[]) => {
    const patch: Parameters<typeof setSettings>[0] = {
      hiddenModels: { ...s.hiddenModels, [kind]: next },
    };
    const isHidden = (m: string) => !!m && next.some((h) => modelMatchesHidden(m, h));
    // 保存済みの選択値を掃く。放置すると「設定画面には除外と出ているのに起動導線は
    // 除外モデルを既定に持っている」状態になり、起動のたびに Agent 側ガードで弾かれる。
    const row = agentLaunchDefault(s, kind);
    if (isHidden(row.model)) {
      const fallback = kind === "claude" ? CLAUDE_MODELS.find(([id]) => !isHidden(id))?.[0] || "" : "";
      patch.agentLaunchDefaults = { ...s.agentLaunchDefaults, [kind]: { ...row, model: fallback, effort: "" } };
      if (kind === "claude") patch.defaultModel = fallback;
    }
    if (isHidden(s.assistantModels?.[kind] || "")) {
      patch.assistantModels = { ...s.assistantModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (isHidden(s.assistantUtilityModels?.[kind] || "")) {
      patch.assistantUtilityModels = { ...s.assistantUtilityModels, [kind]: ASSISTANT_RECOMMENDED_MODEL };
    }
    if (kind === "claude" && isHidden(s.assistantAutoTurnModel)) patch.assistantAutoTurnModel = "";
    setSettings(patch);
    forgetHiddenRepoModels(kind, next); // リポジトリごとの「前回使ったモデル」も掃く
  };

  return (
    <SettingRow label={tr("agents.hidden_models")} sub={tr("agents.hidden_models_sub")}>
      <div className="hidden-models">
        {hidden.length > 0 && (
          <div className="hm-chips">
            {hidden.map((id) => (
              <span key={id} className="hm-chip">
                {id}
                <button
                  type="button"
                  className="hm-chip-x"
                  aria-label={tr("agents.hidden_models_remove", { model: id })}
                  onClick={() => apply(hidden.filter((h) => h !== id))}
                >
                  ×
                </button>
              </span>
            ))}
          </div>
        )}
        <select
          className="ds-select"
          value=""
          disabled={!canAdd}
          aria-label={tr("agents.hidden_models_add")}
          onChange={(e) => e.target.value && apply([...hidden, e.target.value])}
        >
          <option value="">{tr("agents.hidden_models_add")}</option>
          {visible.map(([id, label]) => (
            <option key={id} value={id}>
              {label}
            </option>
          ))}
        </select>
      </div>
    </SettingRow>
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
  const s = useSettings();
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
  // 再認証。claude は自分の .credentials.json を所有していて「更新だけ」のコマンドを
  // 持たないので、一度サインアウトしてから同じ OAuth フローを開き直す（＝これまで
  // 利用者が手で踏んでいた 切断→接続 を 1 アクションにしたもの）。サーバ側でトークンが
  // 失効しても `claude auth status` は手元の資格情報を見て loggedIn を返すため、カードは
  // 接続済みのまま — この導線が無いと、認証切れは「切断してみる」以外に直しようがない。
  const reauth = async () => {
    await raw("api/connections/claude", { method: "DELETE" });
    reload(); // 状態ピルを 未接続 へ戻す（フロー表示自体は下の分岐が先に効く）
    await start();
  };

  return (
    <ProviderCard
      id="claude"
      name={kindDisplayName("claude")}
      status={
        running ? (
          /* 期限切れは「接続済み」ではない: 資格情報は手元にあるので `claude auth status`
             は loggedIn を返すが、それでターンは始まらない（docs/47 §4-8）。緑のピルの
             ままにすると、この画面がまさに嘘をつく場所になる。 */
          <StatusPill on={st?.connected && !st?.expired}>
            {!st?.connected
              ? tr("conn.disconnected")
              : st?.expired
                ? tr("conn.expired")
                : tr("conn.connected")}
          </StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : /* フローを接続状態より先に見る: 再認証はサインアウト→フロー開始の順で走り、
            api/connections の再取得はそれより遅れて届く。接続済みを先に見ていると、
            開いたばかりのコード貼り付け欄がその一瞬だけ隠れてしまう。 */
      flow ? (
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
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || tr("conn.connected")}>
            {st.email || tr("conn.connected")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          {/* 期限（docs/47 §4-8）。CLI 側の予告は残り1日以下・15秒で消える起動ヒント
              だけで、切れた後は何も出ない。ここは消えない場所なので、切れる前から
              静かに出しておく。日時は tooltip（行を伸ばさない）。 */}
          {(st.expired || st.days_left !== undefined) && (
            <span className="p-exp" title={st.expires_at ? new Date(st.expires_at).toLocaleString() : undefined}>
              {st.expired
                ? tr("conn.expired")
                : st.days_left
                  ? tr("conn.expires_in", { days: st.days_left })
                  : /* 残り 1 日未満、または更新期限は過ぎたが最後のアクセストークンで
                       まだ動いている状態（数時間で止まる）。日数では言えない。 */
                    tr("conn.expires_soon")}
            </span>
          )}
          <ReauthButton onClick={() => void reauth()} />
          <DisconnectButton onClick={disconnect} />
        </div>
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
        <SettingRow label={tr("agents.claude_abort_resume")}>
          <OnOff
            value={s.claudeAbortAutoResume}
            onChange={(v) => setSetting("claudeAbortAutoResume", v)}
          />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_claude_abort_resume")}</p>
        <SettingRow label={tr("agents.claude_rate_limit_resume")}>
          <OnOff value={s.rateLimitAutoResume} onChange={(v) => setSetting("rateLimitAutoResume", v)} />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_claude_rate_limit_resume")}</p>
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
          <span className="p-em" title={st.email || tr("conn.connected")}>
            {st.email || tr("conn.connected")}
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
          <IssueLink url="https://platform.openai.com/api-keys" />
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
        <ThinkingRow kind="codex" />
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

// Cursor: dedicated login flow (docs/40). `NO_OPEN_BROWSER=1 cursor-agent login`
// prints an authorize URL and self-polls until the user approves in a browser, then
// writes ~/.config/cursor/auth.json — so the UI shows the URL and polls
// api/connections/cursor/poll (no pasted code, unlike Claude/Codex). v1 is
// login-only; a manual CURSOR_API_KEY registration path is deferred to Track D
// (cursor has no key-persistence command and injecting it into the TUI pane would
// leak it into `ps`). No RTK toggle yet — cursor's rtk hook seam is Track D.
function CursorCard({ running, st, reload }: { running: boolean; st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [flow, setFlow] = useState<any>(null); // { url, flow_id, status } while a login is in flight
  const [busy, setBusy] = useState(false);
  const unsupported = st?.supported === false;

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/cursor/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.cursor_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      setFlow({ url: res.url, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 10 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setFlow((f: any) => (f ? { ...f, status: tr("git.oauth_expired") } : f)),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/cursor/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setFlow(null);
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
  const disconnect = async () => {
    await raw("api/connections/cursor", { method: "DELETE" });
    setFlow(null);
    reload();
  };

  return (
    <ProviderCard
      id="cursor"
      name={kindDisplayName("cursor")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : unsupported ? (
        <div className="p-desc">{tr("agents.cursor_unsupported", { reason: st?.reason || "" })}</div>
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || "Cursor"}
          </span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : flow ? (
        <div className="p-body">
          {/* No pasted code — cursor approves entirely in the browser, then self-polls. */}
          <DeviceSteps url={flow.url} status={flow.status} />
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.cursor_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startLogin}>
                <span className="p-opt-t">{tr("agents.cursor_connect")}</span>
                <span className="p-opt-s">{tr("agents.cursor_connect_note")}</span>
              </button>
            </div>
            <Hint>
              {tr("agents.cursor_hint_1")}
              <a href="https://cursor.com/dashboard" target="_blank" rel="noopener" className="flow-link">
                {tr("agents.cursor_dashboard")}
              </a>
              {tr("agents.cursor_hint_2")}
            </Hint>
          </div>
        </>
      )}
      <CardSettings>
        <LaunchDefaults kind="cursor" />
      </CardSettings>
    </ProviderCard>
  );
}

// Kiro: on-demand install + device-flow login (docs/43 Track C). Kiro's ~855MB
// bundle is NOT baked on the lean image (decision §4-2), so a fresh workspace reports
// supported=false; the card offers an "install" button that lands the CLI in the
// user's ~/.local (POST /connections/kiro/install runs in the background, we poll
// GET for progress). Once installed, `kiro-cli login --license free --use-device-flow`
// prints a verification URL (+ short confirmation code) and self-polls AWS SSO until
// the user approves in a browser — so the UI shows both and polls
// api/connections/kiro/poll (no pasted code, like Codex/Cursor). v1 is login-only
// (Builder ID / free); the API-key path (KIRO_API_KEY, Pro+) is deferred to Track D.
// No RTK toggle yet — kiro's rtk hook seam is Track D.
function KiroCard({ running, st, reload }: { running: boolean; st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [flow, setFlow] = useState<any>(null); // { url, user_code, flow_id, status } while a login is in flight
  const [installing, setInstalling] = useState<null | "installing" | "error">(null);
  const [busy, setBusy] = useState(false);
  // { installed, version, pin, updateAvailable } — the version facts behind the update
  // affordance below. Kiro is the one CLI whose copy lives in the home volume with no
  // self-updater and no boot-install, so a versions.json pin bump only reaches it when
  // something re-installs. The launch guard does that implicitly at the next launch;
  // this makes it EXPLICIT (you see that an update exists and press the button when a
  // multi-minute download suits you, instead of being surprised mid-launch).
  const [inst, setInst] = useState<any>(null);
  const unsupported = st?.supported === false; // CLI not installed yet (on-demand)

  const loadInstall = useCallback(async () => {
    if (!running) return;
    try {
      setInst(await api("api/connections/kiro/install"));
    } catch {
      /* stopped workspace / transient 502 — the card just shows no update notice */
    }
  }, [running]);
  useEffect(() => {
    void loadInstall();
  }, [loadInstall, st?.supported]);

  const install = async () => {
    setBusy(true);
    setInstalling("installing");
    try {
      const res = await api("api/connections/kiro/install", { method: "POST" });
      if (!res || res.error) {
        setInstalling("error");
        toast(tr("agents.kiro_install_failed", { msg: res?.error?.message || "" }));
        return;
      }
      if (res.state === "done") {
        setInstalling(null);
        void loadInstall();
        reload();
        return;
      }
      // Poll the background install until it finishes; the ~855MB download is slow.
      poll({
        deadlineMs: 20 * 60 * 1000,
        firstDelayMs: 4000,
        onExpire: () => setInstalling("error"),
        step: async () => {
          let p;
          try {
            p = await api("api/connections/kiro/install");
          } catch {
            p = null;
          }
          if (p && p.state === "done") {
            setInstalling(null);
            void loadInstall(); // refresh version / updateAvailable after an upgrade
            reload();
            return { stop: true };
          }
          if (p && p.state === "error") {
            setInstalling("error");
            toast(tr("agents.kiro_install_failed", { msg: p.error || "" }));
            return { stop: true };
          }
          return { stop: false, nextMs: 4000 };
        },
      });
    } finally {
      setBusy(false);
    }
  };

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/kiro/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.kiro_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      setFlow({ url: res.url, user_code: res.user_code, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setFlow((f: any) => (f ? { ...f, status: tr("git.oauth_expired") } : f)),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/kiro/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setFlow(null);
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
  const disconnect = async () => {
    await raw("api/connections/kiro", { method: "DELETE" });
    setFlow(null);
    reload();
  };

  return (
    <ProviderCard
      id="kiro"
      name={kindDisplayName("kiro")}
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
            {st.email || "Kiro"}
          </span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : unsupported ? (
        // Not installed yet — offer the on-demand install (~855MB into the home volume).
        <>
          <div className="p-desc">{tr("agents.kiro_install_desc")}</div>
          <div className="p-body">
            {installing === "installing" ? (
              <p className="ps-note ps-note-warn">{tr("agents.kiro_installing")}</p>
            ) : (
              <>
                <div className="p-opts">
                  <button type="button" className="p-opt" disabled={busy} onClick={install}>
                    <span className="p-opt-t">{tr("agents.kiro_install")}</span>
                    <span className="p-opt-s">{tr("agents.kiro_install_note")}</span>
                  </button>
                </div>
                {installing === "error" && <p className="ps-note ps-note-warn">{tr("agents.kiro_install_error")}</p>}
              </>
            )}
          </div>
        </>
      ) : flow ? (
        <div className="p-body">
          {/* device flow: URL + a short code to confirm in the browser; kiro self-polls. */}
          <DeviceSteps code={flow.user_code} url={flow.url} status={flow.status} />
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.kiro_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startLogin}>
                <span className="p-opt-t">{tr("agents.kiro_connect")}</span>
                <span className="p-opt-s">{tr("agents.kiro_connect_note")}</span>
              </button>
            </div>
            <Hint>{tr("agents.kiro_hint")}</Hint>
          </div>
        </>
      )}
      {/* Update affordance. Rendered outside the connection branches above because it
          applies whether or not you are signed in — it is about the BINARY, not the
          auth. Shown only when the agent positively reports a version mismatch against
          the versions.json pin (an unreadable version or a missing pin says nothing, so
          the user is never nagged into a 554MB download on a guess). */}
      {running && !unsupported && inst?.updateAvailable && (
        <div className="p-body">
          {installing === "installing" ? (
            <p className="ps-note ps-note-warn">{tr("agents.kiro_updating")}</p>
          ) : (
            <>
              <p className="ps-note ps-note-warn">
                {tr("agents.kiro_update_avail", { cur: inst.version || "?", pin: inst.pin || "?" })}
              </p>
              <div className="p-opts">
                <button type="button" className="p-opt" disabled={busy} onClick={install}>
                  <span className="p-opt-t">{tr("agents.kiro_update")}</span>
                  <span className="p-opt-s">{tr("agents.kiro_update_note")}</span>
                </button>
              </div>
              {installing === "error" && <p className="ps-note ps-note-warn">{tr("agents.kiro_install_error")}</p>}
            </>
          )}
        </div>
      )}
      <CardSettings>
        <LaunchDefaults kind="kiro" />
      </CardSettings>
    </ProviderCard>
  );
}

// opencode: two independent auth paths that coexist (docs/54) —
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

// 利用枠の導線（docs/54 §54.7）。opencode.ai の利用枠ページはブラウザセッション前提で、
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

  const usage = s.opencodeCatalog; // off | free | go | zen（課金経路の選択・docs/54）
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
