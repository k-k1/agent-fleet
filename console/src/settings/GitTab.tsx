import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, raw } from "../api.js";
import { useApp } from "../state.jsx";
import Icon from "../components/Icon.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import EmptyState from "../components/EmptyState.jsx";
import { ProviderCard, StatusPill, DeviceSteps, DisconnectButton } from "./providerCard.jsx";

interface RowProps {
  st: any;
  reload: () => void;
}

// GitTab: git-hosting connections (GitHub / Bitbucket) used for clone / fetch / push.
// Split out of the old 接続 tab so it sits on its own domain, next to SSM / MCP — the
// エージェント tab owns the agent providers. Auth flows and APIs are unchanged.
//
// TODO(gitconfig): per-provider commit identity (user.name / user.email) is planned
// here — each provider card will gain a settings group like the agent cards. It needs
// a new Agent endpoint (workspace/agent git identity), so it lands once that exists.
export default function GitTab() {
  const { bumpConn, wsState, startWs } = useApp();
  // git-hosting auth is agent-proxied (proxyAgentREST → 502 while stopped), so the
  // tab needs a running workspace — same as the agent tab (SSM/MCP are CP-stored and
  // don't).
  const running = wsState === "running";
  const [conns, setConns] = useState<any>(null);
  const reload = useCallback(() => {
    api("api/connections")
      .then(setConns)
      .catch(() => setConns({}));
    bumpConn();
  }, [bumpConn]);
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  if (!running) {
    return (
      <EmptyState
        as="div"
        icon="debug-disconnect"
        message="Git 接続はワークスペース内で実行されます"
        hint="認証はコンテナ内の Agent を経由するため、ワークスペースの起動が必要です。"
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
      <div className="conn-cat">git ホスティング</div>
      <GithubRow st={conns.github} reload={reload} />
      <BitbucketRow st={conns.bitbucket} reload={reload} />
    </div>
  );
}

function GithubRow({ st, reload }: RowProps) {
  const toast = useToast();
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [oauth, setOauth] = useState<any>(null); // { user_code, verification_uri, status }
  const [token, setToken] = useState("");
  const alive = useRef(true);
  useEffect(() => () => {
    alive.current = false;
  }, []);

  const startOAuth = async () => {
    const res = await api("api/connections/git/github/oauth/start", { method: "POST" });
    if (!res || res.error) {
      if (res?.error?.code === "not_configured")
        toast("GitHub OAuth は未設定です（client_id）。「token」から貼付を使ってください。", { kind: "warn" });
      else toast("OAuth 開始に失敗: " + (res?.error?.message || ""));
      return;
    }
    setMode("oauth");
    setOauth({ user_code: res.user_code, verification_uri: res.verification_uri, status: "承認待ち…" });
    const deadline = Date.now() + (res.expires_in || 900) * 1000;
    let iv = (res.interval || 5) * 1000;
    const tick = async () => {
      if (!alive.current) return;
      if (Date.now() > deadline) {
        setOauth((o: any) => ({ ...o, status: "期限切れ。やり直してください" }));
        return;
      }
      let p;
      try {
        p = await apiJSON("api/connections/git/github/oauth/poll", "POST", { flow_id: res.flow_id });
      } catch {
        p = null;
      }
      if (p && p.connected) {
        setMode("idle");
        reload();
        return;
      }
      if (p && p.error) {
        setOauth((o: any) => ({ ...o, status: "失敗: " + (p.error.message || p.error.code || "") }));
        return;
      }
      if (p && p.interval) iv = p.interval * 1000;
      setTimeout(tick, iv);
    };
    setTimeout(tick, iv);
  };

  const saveToken = async () => {
    if (!token.trim()) return;
    const res = await apiJSON("api/connections/git/github.com", "PUT", { token: token.trim() });
    if (res && res.error) {
      toast("接続に失敗: " + (res.error.message || res.error));
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || st.username || ""}>
            {st.username || "connected"}
          </span>
          {st.email && <span className="p-pl">{st.email}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
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
              placeholder="Personal Access Token"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoFocus
            />
            <button onClick={saveToken}>接続</button>
            <button className="ghost" onClick={() => setMode("idle")}>
              戻る
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="p-desc">OAuth（デバイスフロー）または Personal Access Token で接続。</div>
          <div className="p-body">
            <div className="flow">
              <button onClick={startOAuth}>OAuth 接続</button>
              <button className="ghost" onClick={() => setMode("token")}>
                token
              </button>
            </div>
          </div>
        </>
      )}
    </ProviderCard>
  );
}

function BitbucketRow({ st, reload }: RowProps) {
  const toast = useToast();
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [status, setStatus] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const alive = useRef(true);
  useEffect(() => () => {
    alive.current = false;
  }, []);

  const startOAuth = async () => {
    const res = await api("api/connections/git/bitbucket/oauth/start");
    if (!res || res.error || !res.authorize_url) {
      if (res?.error?.code === "not_configured")
        toast("Bitbucket OAuth は未設定です（key/secret）。「token」から貼付を使ってください。", { kind: "warn" });
      else toast("OAuth 開始に失敗: " + (res?.error?.message || ""));
      return;
    }
    window.open(res.authorize_url, "_blank", "noopener");
    setMode("oauth");
    setStatus("別タブで承認してください…");
    const deadline = Date.now() + 5 * 60 * 1000;
    const tick = async () => {
      if (!alive.current) return;
      if (Date.now() > deadline) {
        setStatus("タイムアウト。やり直してください");
        return;
      }
      let d;
      try {
        d = await api("api/connections");
      } catch {
        d = null;
      }
      if (d && d.bitbucket && d.bitbucket.connected) {
        setMode("idle");
        reload();
        return;
      }
      setTimeout(tick, 2000);
    };
    setTimeout(tick, 2500);
  };

  const saveToken = async () => {
    if (!token.trim()) return;
    const res = await apiJSON("api/connections/git/bitbucket.org", "PUT", {
      username: username.trim(),
      token: token.trim(),
    });
    if (res && res.error) {
      toast("接続に失敗: " + (res.error.message || res.error));
      return;
    }
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
      status={<StatusPill on={st?.connected}>{st?.connected ? "接続済み" : "未接続"}</StatusPill>}
    >
      {st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || st.username || ""}>
            {st.username || "connected"}
          </span>
          {st.email && <span className="p-pl">{st.email}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
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
              placeholder="Atlassian email"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
            <input
              className="cinput"
              type="password"
              placeholder="API token"
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <button onClick={saveToken}>接続</button>
            <button className="ghost" onClick={() => setMode("idle")}>
              戻る
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="p-desc">OAuth（コードグラント）または メール＋アプリトークンで接続。</div>
          <div className="p-body">
            <div className="flow">
              <button onClick={startOAuth}>OAuth 接続</button>
              <button className="ghost" onClick={() => setMode("token")}>
                token
              </button>
            </div>
          </div>
        </>
      )}
    </ProviderCard>
  );
}
