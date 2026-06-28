import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, raw } from "../api.js";

// ConnectionsTab drives the per-user provider auth flows entirely in the WebUI:
// Claude (start → approve → paste code → complete), GitHub (OAuth device flow or
// PAT paste), Bitbucket (OAuth code grant or email+token). Secrets are stored in
// the container home by the Agent — the Console never holds them.
export default function ConnectionsTab() {
  const [conns, setConns] = useState(null);
  const reload = useCallback(() => {
    api("api/connections")
      .then(setConns)
      .catch(() => setConns({}));
  }, []);
  useEffect(reload, [reload]);

  if (!conns) return <p className="muted pad">読み込み中…</p>;
  return (
    <div className="conns">
      <ClaudeRow st={conns.claude} reload={reload} />
      <GithubRow st={conns.github} reload={reload} />
      <BitbucketRow st={conns.bitbucket} reload={reload} />
      <OpencodeRow st={conns.opencode} reload={reload} />
    </div>
  );
}

// opencode auth: provider API keys kept in the encrypted store and injected as env
// vars (ANTHROPIC_API_KEY, …) when an opencode session launches — the same
// "settings-driven, stored, injected" model as Claude, but multi-provider.
const OC_PRESETS = [
  ["anthropic", "Anthropic", "ANTHROPIC_API_KEY"],
  ["openai", "OpenAI", "OPENAI_API_KEY"],
  ["openrouter", "OpenRouter", "OPENROUTER_API_KEY"],
  ["google", "Google Gemini", "GEMINI_API_KEY"],
  ["custom", "カスタム…", ""],
];

function OpencodeRow({ st, reload }) {
  const [preset, setPreset] = useState("anthropic");
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
        alert("保存に失敗: " + (res.error.message || res.error));
        return;
      }
      setKey("");
      setCustomEnv("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (env) => {
    await raw(`api/connections/opencode/${encodeURIComponent(env)}`, { method: "DELETE" });
    reload();
  };

  return (
    <div className="conn-row conn-block">
      <div className="conn-head">
        <Dot on={envs.length > 0} />
        <span className="cname">opencode</span>
        <span className="muted">プロバイダ API キー（起動時に env 注入）</span>
      </div>
      {envs.length > 0 && (
        <ul className="oc-keys">
          {envs.map((e) => (
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
  );
}

function Dot({ on }) {
  return <span className={"cdot " + (on ? "ok" : "off")}>●</span>;
}

function ClaudeRow({ st, reload }) {
  const [flow, setFlow] = useState(null); // { url, flow_id }
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  const start = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/claude/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        alert("Claude 認証開始に失敗: " + (res?.error?.message || ""));
        return;
      }
      setFlow({ url: res.url, flow_id: res.flow_id });
    } finally {
      setBusy(false);
    }
  };
  const complete = async () => {
    if (!code.trim()) return;
    setBusy(true);
    try {
      const r = await apiJSON("api/connections/claude/complete", "POST", {
        flow_id: flow.flow_id,
        code: code.trim(),
      });
      if (r && r.error) {
        alert("接続に失敗: " + (r.error.message || r.error));
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
    <div className="conn-row">
      <Dot on={st?.connected} />
      <span className="cname">Claude</span>
      {st?.connected ? (
        <>
          <span className="cwho">connected</span>
          <button className="icon danger" title="切断" onClick={disconnect}>
            ✕
          </button>
        </>
      ) : flow ? (
        <div className="flow">
          <a href={flow.url} target="_blank" rel="noopener" className="flow-link">
            ① ブラウザでサインイン
          </a>
          <input
            className="cinput"
            placeholder="② コードを貼付"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoFocus
          />
          <button disabled={busy} onClick={complete}>
            完了
          </button>
        </div>
      ) : (
        <button disabled={busy} onClick={start}>
          接続
        </button>
      )}
    </div>
  );
}

function GithubRow({ st, reload }) {
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [oauth, setOauth] = useState(null); // { user_code, verification_uri, status }
  const [token, setToken] = useState("");
  const alive = useRef(true);
  useEffect(() => () => (alive.current = false), []);

  const startOAuth = async () => {
    const res = await api("api/connections/git/github/oauth/start", { method: "POST" });
    if (!res || res.error) {
      if (res?.error?.code === "not_configured")
        alert("GitHub OAuth は未設定です（client_id）。「token」から貼付を使ってください。");
      else alert("OAuth 開始に失敗: " + (res?.error?.message || ""));
      return;
    }
    setMode("oauth");
    setOauth({ user_code: res.user_code, verification_uri: res.verification_uri, status: "承認待ち…" });
    const deadline = Date.now() + (res.expires_in || 900) * 1000;
    let iv = (res.interval || 5) * 1000;
    const tick = async () => {
      if (!alive.current) return;
      if (Date.now() > deadline) {
        setOauth((o) => ({ ...o, status: "期限切れ。やり直してください" }));
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
        setOauth((o) => ({ ...o, status: "失敗: " + (p.error.message || p.error.code || "") }));
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
      alert("接続に失敗: " + (res.error.message || res.error));
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
    <div className="conn-row">
      <Dot on={st?.connected} />
      <span className="cname">GitHub</span>
      {st?.connected ? (
        <>
          <span className="cwho" title={st.username || ""}>{st.username || "connected"}</span>
          <button className="icon danger" title="切断" onClick={disconnect}>
            ✕
          </button>
        </>
      ) : mode === "oauth" && oauth ? (
        <div className="flow">
          <span className="oauth-code">{oauth.user_code}</span>
          <a href={oauth.verification_uri} target="_blank" rel="noopener" className="flow-link">
            → {oauth.verification_uri} で入力
          </a>
          <span className="muted">{oauth.status}</span>
        </div>
      ) : mode === "token" ? (
        <div className="flow">
          <input
            className="cinput"
            type="password"
            placeholder="Personal Access Token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
          <button onClick={saveToken}>接続</button>
        </div>
      ) : (
        <>
          <button onClick={startOAuth}>OAuth 接続</button>
          <button className="ghost" onClick={() => setMode("token")}>
            token
          </button>
        </>
      )}
    </div>
  );
}

function BitbucketRow({ st, reload }) {
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [status, setStatus] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const alive = useRef(true);
  useEffect(() => () => (alive.current = false), []);

  const startOAuth = async () => {
    const res = await api("api/connections/git/bitbucket/oauth/start");
    if (!res || res.error || !res.authorize_url) {
      if (res?.error?.code === "not_configured")
        alert("Bitbucket OAuth は未設定です（key/secret）。「token」から貼付を使ってください。");
      else alert("OAuth 開始に失敗: " + (res?.error?.message || ""));
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
      alert("接続に失敗: " + (res.error.message || res.error));
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
    <div className="conn-row">
      <Dot on={st?.connected} />
      <span className="cname">Bitbucket</span>
      {st?.connected ? (
        <>
          <span className="cwho" title={st.username || ""}>{st.username || "connected"}</span>
          <button className="icon danger" title="切断" onClick={disconnect}>
            ✕
          </button>
        </>
      ) : mode === "oauth" ? (
        <span className="muted">{status}</span>
      ) : mode === "token" ? (
        <div className="flow">
          <input className="cinput" placeholder="Atlassian email" value={username} onChange={(e) => setUsername(e.target.value)} />
          <input
            className="cinput"
            type="password"
            placeholder="API token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
          <button onClick={saveToken}>接続</button>
        </div>
      ) : (
        <>
          <button onClick={startOAuth}>OAuth 接続</button>
          <button className="ghost" onClick={() => setMode("token")}>
            token
          </button>
        </>
      )}
    </div>
  );
}
