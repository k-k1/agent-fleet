import { useEffect, useState } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { usePolling } from "./usePolling.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConnections } from "./useConnections.ts";
import { ProviderCard, StatusPill, DeviceSteps, DisconnectButton, IssueLink, Hint } from "./providerCard.tsx";
import { useT } from "../../lib/i18n/index.ts";

interface RowProps {
  st: any;
  reload: () => void;
  /** テナントがこのプロバイダの OAuth アプリを登録しているか（docs/71）。 */
  oauthAvailable: boolean;
}

// useGitOAuthAvailability — 「OAuth で接続」を出してよいか。
//
// ★ 押してから not_configured が返る形にはしない。設定を持っているのはテナント
// 管理者で、押した本人には直せないため、押す前に「テナント管理者に登録を頼む」と
// 言えないと詰む（docs/71 §71.4）。
//
// ★ /api/connections（Agent へプロキシ）ではなく CP 直の /api/git-oauth を見る。
// 答えは CP の DB にあり、ワークスペースが止まっている間もこの面は開かれる。
function useGitOAuthAvailability() {
  const [avail, setAvail] = useState<Record<string, { configured?: boolean }> | null>(null);
  useEffect(() => {
    api("api/git-oauth")
      .then((d) => {
        if (d && !d.error) setAvail(d);
      })
      .catch(() => {});
  }, []);
  return avail;
}

// GitTab: git-hosting CONNECTIONS (GitHub / Bitbucket) used for clone / fetch / push,
// plus the commit identity. Lives in the 接続 group next to the other external-account
// connections. Auth is agent-proxied (proxyAgentREST → 502 while stopped), so the tab
// needs a running workspace — same as the エージェント tab. Self-hosted internal repos
// (CP-native, no Agent) moved to their own ワークスペース › 内部リポジトリ tab
// (InternalReposTab), since they're workspace infra, not an external connection.
//
// TODO(gitconfig): per-provider commit identity (user.name / user.email) is planned
// here — each provider card will gain a settings group like the agent cards. It needs
// a new Agent endpoint (workspace/agent git identity), so it lands once that exists.
export function GitTab() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  const running = wsState === "running";
  const { conns, reload } = useConnections();
  const oauth = useGitOAuthAvailability();
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  return (
    <div className="conns">
      {!running ? (
        <EmptyState
          icon="debug-disconnect"
          title={tr("git.ws_required_title")}
          hint={tr("git.ws_required_hint")}
        >
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("ops.start_ws")}
          </Button>
        </EmptyState>
      ) : !conns ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : (
        <>
          <div className="conn-cat">{tr("git.cat_hosting")}</div>
          {/* 未取得（null）の間は出す側に倒す。取得できないだけで導線を消すと、
              「登録済みなのにボタンが無い」という直しようのない画面になる。 */}
          <GithubRow st={conns.github} reload={reload} oauthAvailable={oauth?.github?.configured !== false} />
          <BitbucketRow st={conns.bitbucket} reload={reload} oauthAvailable={oauth?.bitbucket?.configured !== false} />
          <GlobalIdentity />
        </>
      )}
    </div>
  );
}

// IdentityFields edits the commit user.name / user.email a provider uses. The Agent
// bakes it into each of that host's repo's local .git/config, so it applies to EVERY
// commit path (terminal / claude / Console), and each repo can still override it.
// Empty = use the connected account (auto-seeded).
function IdentityFields({ host, name0, email0 }: { host: string; name0?: string; email0?: string }) {
  const tr = useT();
  const toast = useToast();
  const [name, setName] = useState(name0 || "");
  const [email, setEmail] = useState(email0 || "");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setName(name0 || "");
    setEmail(email0 || "");
  }, [name0, email0]);
  const save = async () => {
    setBusy(true);
    try {
      const res = await apiJSON(`api/connections/git/${encodeURIComponent(host)}/identity`, "PUT", {
        name: name.trim(),
        email: email.trim(),
      });
      if (res && res.error) {
        toast(tr("common.save_failed_msg", { msg: String(res.error.message || res.error) }));
        return;
      }
      toast(tr("git.identity_saved"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="git-identity">
      <div className="gi-title">{tr("git.identity_title")}</div>
      <div className="gi-row">
        <input className="cinput" placeholder={tr("git.name_placeholder_ex")} value={name} onChange={(e) => setName(e.target.value)} />
        <input className="cinput" placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
        <button disabled={busy} onClick={save}>
          {tr("common.save")}
        </button>
      </div>
      <div className="field-help">{tr("git.identity_help")}</div>
    </div>
  );
}

// GlobalIdentity edits the ~/.gitconfig default identity — used for repos that match no
// connected provider (no remote / direct-dir sessions).
function GlobalIdentity() {
  const tr = useT();
  const toast = useToast();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api("api/git/identity")
      .then((d) => {
        if (d && !d.error) {
          setName(d.name || "");
          setEmail(d.email || "");
        }
      })
      .catch(() => {});
  }, []);
  const save = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/git/identity", "PUT", { name: name.trim(), email: email.trim() });
      if (res && res.error) {
        toast(tr("common.save_failed_msg", { msg: String(res.error.message || res.error) }));
        return;
      }
      toast(tr("git.global_identity_saved"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="conn-cat">{tr("git.global_identity_cat")}</div>
      <div className="git-identity solo">
        <div className="gi-row">
          <input className="cinput" placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <input className="cinput" placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button disabled={busy} onClick={save}>
            {tr("common.save")}
          </button>
        </div>
        <div className="field-help">{tr("git.global_identity_help")}</div>
      </div>
    </>
  );
}

function GithubRow({ st, reload, oauthAvailable }: RowProps) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [oauth, setOauth] = useState<any>(null); // { user_code, verification_uri, status }
  const [token, setToken] = useState("");

  const startOAuth = async () => {
    const res = await api("api/connections/git/github/oauth/start", { method: "POST" });
    if (!res || res.error) {
      if (res?.error?.code === "not_configured")
        toast(tr("git.github_oauth_unconfigured"), { kind: "warn" });
      else toast(tr("git.oauth_start_failed", { msg: res?.error?.message || "" }));
      return;
    }
    setMode("oauth");
    setOauth({ user_code: res.user_code, verification_uri: res.verification_uri, status: tr("git.oauth_waiting") });
    let iv = (res.interval || 5) * 1000;
    poll({
      deadlineMs: (res.expires_in || 900) * 1000,
      firstDelayMs: iv,
      onExpire: () => setOauth((o: any) => ({ ...o, status: tr("git.oauth_expired") })),
      step: async () => {
        let p;
        try {
          p = await apiJSON("api/connections/git/github/oauth/poll", "POST", { flow_id: res.flow_id });
        } catch {
          p = null;
        }
        if (p && p.connected) {
          setMode("idle");
          reload();
          return { stop: true };
        }
        if (p && p.error) {
          setOauth((o: any) => ({ ...o, status: tr("git.oauth_failed", { msg: p.error.message || p.error.code || "" }) }));
          return { stop: true };
        }
        if (p && p.interval) iv = p.interval * 1000;
        return { stop: false, nextMs: iv };
      },
    });
  };

  const saveToken = async () => {
    if (!token.trim()) return;
    const res = await apiJSON("api/connections/git/github.com", "PUT", { token: token.trim() });
    if (res && res.error) {
      toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
      return;
    }
    setToken("");
    setMode("idle");
    reload();
  };
  const disconnect = async () => {
    await raw("api/connections/git/github.com", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="github"
      name="GitHub"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <>
          <div className="p-who">
            <span className="p-em" title={st.email || st.username || ""}>
              {st.username || tr("conn.connected")}
            </span>
            {st.email && <span className="p-pl">{st.email}</span>}
            <DisconnectButton onClick={disconnect} />
          </div>
          <IdentityFields host="github.com" name0={st.commitName} email0={st.commitEmail} />
        </>
      ) : mode === "oauth" && oauth ? (
        <div className="p-body">
          <DeviceSteps code={oauth.user_code} url={oauth.verification_uri} status={oauth.status} />
        </div>
      ) : mode === "token" ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("git.github_token_ph")}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoFocus
            />
            <button onClick={saveToken}>{tr("conn.connect")}</button>
            <button className="ghost" onClick={() => setMode("idle")}>
              {tr("common.back")}
            </button>
          </div>
          <IssueLink url="https://github.com/settings/tokens" />
          <Hint>
            {tr("git.github_token_hint")} <code>repo</code>
            {tr("git.github_token_hint_read")} <code>public_repo</code>
            {tr("git.github_token_hint_pub")} <code>Contents: Read and write</code>
            {tr("git.github_token_hint_fg")}
          </Hint>
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("git.github_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              {oauthAvailable && (
                <button type="button" className="p-opt" onClick={startOAuth}>
                  <span className="p-opt-t">
                    {tr("git.connect_oauth")} <span className="p-rec">{tr("git.recommended")}</span>
                  </span>
                  <span className="p-opt-s">{tr("git.github_oauth_sub")}</span>
                </button>
              )}
              <button type="button" className="p-opt" onClick={() => setMode("token")}>
                <span className="p-opt-t">{tr("git.connect_token")}</span>
                <span className="p-opt-s">{tr("git.github_token_sub")}</span>
              </button>
            </div>
            {!oauthAvailable && <Hint>{tr("git.oauth_unregistered")}</Hint>}
          </div>
        </>
      )}
    </ProviderCard>
  );
}

function BitbucketRow({ st, reload, oauthAvailable }: RowProps) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [status, setStatus] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");

  const startOAuth = async () => {
    const res = await api("api/connections/git/bitbucket/oauth/start");
    if (!res || res.error || !res.authorize_url) {
      if (res?.error?.code === "not_configured")
        toast(tr("git.bitbucket_oauth_unconfigured"), { kind: "warn" });
      else toast(tr("git.oauth_start_failed", { msg: res?.error?.message || "" }));
      return;
    }
    window.open(res.authorize_url, "_blank", "noopener");
    setMode("oauth");
    setStatus(tr("git.bb_waiting"));
    poll({
      deadlineMs: 5 * 60 * 1000,
      firstDelayMs: 2500,
      onExpire: () => setStatus(tr("git.bb_timeout")),
      step: async () => {
        let d;
        try {
          d = await api("api/connections");
        } catch {
          d = null;
        }
        if (d && d.bitbucket && d.bitbucket.connected) {
          setMode("idle");
          reload();
          return { stop: true };
        }
        return { stop: false, nextMs: 2000 };
      },
    });
  };

  const saveToken = async () => {
    if (!token.trim()) return;
    const res = await apiJSON("api/connections/git/bitbucket.org", "PUT", {
      username: username.trim(),
      token: token.trim(),
    });
    if (res && res.error) {
      // Connect-time scope check (backend): map the actionable codes to guidance,
      // fall back to the raw message for anything else.
      const code = res.error.code;
      const msg =
        code === "bb_scopeless"
          ? tr("git.bb_err_scopeless")
          : code === "bb_no_repo_read"
            ? tr("git.bb_err_no_repo_read")
            : tr("conn.connect_failed", { msg: String(res.error.message || res.error) });
      toast(msg, { kind: "warn" });
      return;
    }
    // Connected, but the token can't push (no write scope) — surface it, don't block.
    if (res?.warn === "no_write") toast(tr("git.bb_warn_no_write"), { kind: "warn" });
    setToken("");
    setUsername("");
    setMode("idle");
    reload();
  };
  const disconnect = async () => {
    await raw("api/connections/git/bitbucket.org", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="bitbucket"
      name="Bitbucket"
      status={<StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>}
    >
      {st?.connected ? (
        <>
          <div className="p-who">
            <span className="p-em" title={st.email || st.username || ""}>
              {st.username || tr("conn.connected")}
            </span>
            {st.email && <span className="p-pl">{st.email}</span>}
            <DisconnectButton onClick={disconnect} />
          </div>
          <IdentityFields host="bitbucket.org" name0={st.commitName} email0={st.commitEmail} />
        </>
      ) : mode === "oauth" ? (
        <div className="p-body">
          <span className="p-waiting">
            <Icon name="loading" spin /> {status}
          </span>
        </div>
      ) : mode === "token" ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              placeholder={tr("git.bb_email_ph")}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
            <input
              className="cinput"
              type="password"
              placeholder={tr("git.bb_token_ph")}
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button onClick={saveToken}>{tr("conn.connect")}</button>
            <button className="ghost" onClick={() => setMode("idle")}>
              {tr("common.back")}
            </button>
          </div>
          <IssueLink url="https://id.atlassian.com/manage-profile/security/api-tokens" />
          <Hint>
            {tr("git.bb_token_hint")}
            <br />
            <code>read:account</code> <code>read:workspace:bitbucket</code> <code>read:repository:bitbucket</code>
            {tr("git.bb_token_hint_read")} <code>write:repository:bitbucket</code>
            {tr("git.bb_token_hint_write")}
          </Hint>
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("git.bitbucket_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              {oauthAvailable && (
                <button type="button" className="p-opt" onClick={startOAuth}>
                  <span className="p-opt-t">
                    {tr("git.connect_oauth")} <span className="p-rec">{tr("git.recommended")}</span>
                  </span>
                  <span className="p-opt-s">{tr("git.bb_oauth_sub")}</span>
                </button>
              )}
              <button type="button" className="p-opt" onClick={() => setMode("token")}>
                <span className="p-opt-t">{tr("git.connect_apptoken")}</span>
                <span className="p-opt-s">{tr("git.bb_token_sub")}</span>
              </button>
            </div>
            {!oauthAvailable && <Hint>{tr("git.oauth_unregistered")}</Hint>}
          </div>
        </>
      )}
    </ProviderCard>
  );
}
