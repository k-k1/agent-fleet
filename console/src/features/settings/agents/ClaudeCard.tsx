import { useState } from "react";
import { api, apiJSON, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { OnOff } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton, ReauthButton } from "../parts/providerCard.tsx";
import { SettingRow, CardSettings, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

// Claude: OAuth connect (start → approve in a new tab → paste code → complete), plus
// its behavior settings (Remote Control / 通知 / RTK) once connected.
export function ClaudeCard({
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
             は loggedIn を返すが、それでターンは始まらない（docs/log/47 §4-8）。緑のピルの
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
          {/* 期限（docs/log/47 §4-8）。CLI 側の予告は残り1日以下・15秒で消える起動ヒント
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
