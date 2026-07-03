import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, raw } from "../api.js";
import { useApp } from "../state.jsx";
import type { ReactNode } from "react";
import Icon from "../components/Icon.jsx";
import { useToast } from "../components/ToastProvider.jsx";

// Common props for a per-provider connection row: the provider's status (API shape,
// kept as any) and a reload callback that refetches after connect/disconnect.
interface RowProps {
  st: any;
  reload: () => void;
}

// CopyCode renders a one-time auth code that copies to the clipboard on click. The
// code stays visible (so it can be read), but clicking saves the manual select —
// used for the Codex / GitHub device-flow codes.
function CopyCode({ children }: { children: ReactNode }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(String(children));
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    } catch {
      /* clipboard blocked — the code stays selectable as a fallback */
    }
  };
  return (
    <button type="button" className="oauth-code" title="クリックでコピー" onClick={copy}>
      {children}
      <Icon name={copied ? "check" : "copy"} className="oauth-copy-ic" />
    </button>
  );
}

// DisconnectButton: the per-provider "切断" action shown when a connection is live.
function DisconnectButton({ onClick }: { onClick: () => void }) {
  return (
    <button className="ghost danger conn-disconnect" title="切断" onClick={onClick}>
      切断
    </button>
  );
}

// Colored 2-char badge per provider, matching the session kind badge colors so a
// provider reads the same color wherever it appears.
const BADGE_SHORT: Record<string, string> = {
  claude: "cc",
  codex: "cx",
  opencode: "oc",
  github: "gh",
  bitbucket: "bb",
};

// ProviderCard is the shared shell: a colored badge + name + status pill, then the
// provider's own description / connect / flow UI as children. Replaces the old flat
// .conn-row so every provider reads the same way (mock: console-connections-mock.html).
function ProviderCard({
  id,
  name,
  status,
  children,
}: {
  id: string;
  name: string;
  status: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="p-card">
      <div className="p-head">
        <span className={"p-badge pb-" + id}>{BADGE_SHORT[id]}</span>
        <span className="p-name">{name}</span>
        {status}
      </div>
      {children}
    </div>
  );
}

// Status pill: connected (green + dot + label) or a muted state (未接続 / a count).
function StatusPill({ on, children }: { on?: boolean; children: ReactNode }) {
  return (
    <span className={"p-status" + (on ? " on" : "")}>
      {on && <span className="p-dot" />}
      {children}
    </span>
  );
}

// Unified hint block (left-bordered, "i" marker) — replaces the ad-hoc .field-help /
// inline notes that each provider used differently.
function Hint({ children }: { children: ReactNode }) {
  return (
    <div className="p-hint">
      <span className="p-hint-i">i</span>
      <span>{children}</span>
    </div>
  );
}

// DeviceSteps renders a device-code flow (Codex / GitHub OAuth) as numbered steps —
// ①copy the code ②open the link ③wait for approval — instead of a single wrapping
// row, so the order is legible.
function DeviceSteps({ code, url, status }: { code?: string; url: string; status: string }) {
  let n = 0;
  return (
    <div className="p-steps">
      {code && (
        <div className="p-step">
          <span className="p-step-k">{++n}</span>
          <div className="p-step-c">
            <div className="p-step-lbl">コードをコピー</div>
            <CopyCode>{code}</CopyCode>
          </div>
        </div>
      )}
      <div className="p-step">
        <span className="p-step-k">{++n}</span>
        <div className="p-step-c">
          <div className="p-step-lbl">リンクを開いて貼り付け</div>
          <a href={url} target="_blank" rel="noopener" className="flow-link">
            {url} を開く ↗
          </a>
        </div>
      </div>
      <div className="p-step">
        <span className="p-step-k">{++n}</span>
        <div className="p-step-c">
          <div className="p-step-lbl">承認を待つ</div>
          <span className="p-waiting">
            <Icon name="loading" spin /> {status}
          </span>
        </div>
      </div>
    </div>
  );
}

// ConnectionsTab drives the per-user provider auth flows entirely in the WebUI:
// Claude (start → approve → paste code → complete), GitHub (OAuth device flow or
// PAT paste), Bitbucket (OAuth code grant or email+token). Secrets are stored in
// the container home by the Agent — the Console never holds them.
export default function ConnectionsTab() {
  const { bumpConn } = useApp();
  const [conns, setConns] = useState<any>(null);
  const reload = useCallback(() => {
    api("api/connections")
      .then(setConns)
      .catch(() => setConns({}));
    // Notify global listeners (onboarding card, REPOS launch filter) that
    // connection state may have changed so they refetch — this tab keeps its own
    // local copy and would otherwise leave them stale after a connect/disconnect.
    bumpConn();
  }, [bumpConn]);
  useEffect(reload, [reload]);

  if (!conns) return <p className="muted pad">読み込み中…</p>;
  return (
    <div className="conns">
      <div className="conn-cat">エージェント</div>
      <ClaudeRow st={conns.claude} reload={reload} />
      <CodexRow st={conns.codex} reload={reload} />
      <OpencodeRow st={conns.opencode} reload={reload} />
      <div className="conn-cat">git ホスティング</div>
      <GithubRow st={conns.github} reload={reload} />
      <BitbucketRow st={conns.bitbucket} reload={reload} />
    </div>
  );
}

// Codex auth: codex owns its credential file (~/.codex/auth.json), so the Console
// just drives `codex login` and reports `codex login status`. Two paths: paste an
// API key (codex login --with-api-key), or ChatGPT subscription via device-auth
// (show a one-time code + URL, then poll until approved). Mirrors the Claude row.
function CodexRow({ st, reload }: RowProps) {
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
            Codex CLI をこのワークスペースで使えるようにします。ChatGPT サブスク（推奨）か OpenAI API キーで接続。
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
          </div>
        </>
      )}
    </ProviderCard>
  );
}

// opencode auth: provider API keys kept in the encrypted store and injected as env
// vars (ANTHROPIC_API_KEY, …) when an opencode session launches — the same
// "settings-driven, stored, injected" model as Claude, but multi-provider.
// OpenCode Go is the default: the user issues an API key on the web
// (opencode.ai/auth) and pastes it; opencode reads it from OPENCODE_API_KEY. The
// same opencode.ai key/env also serves OpenCode Zen (the providers only differ by
// model routing: opencode-go/* vs opencode/*), so one entry covers both.
const OC_PRESETS = [
  ["go", "OpenCode Go", "OPENCODE_API_KEY"],
  ["anthropic", "Anthropic", "ANTHROPIC_API_KEY"],
  ["openai", "OpenAI", "OPENAI_API_KEY"],
  ["openrouter", "OpenRouter", "OPENROUTER_API_KEY"],
  ["google", "Google Gemini", "GEMINI_API_KEY"],
  ["custom", "カスタム…", ""],
];

function OpencodeRow({ st, reload }: RowProps) {
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
    </ProviderCard>
  );
}

function ClaudeRow({ st, reload }: RowProps) {
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
      // Like GitHub / Bitbucket OAuth: pop the sign-in page open in a new tab
      // automatically. Claude has no poll-back, so we still take the pasted code.
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
      const r = await apiJSON("api/connections/claude/complete", "POST", {
        flow_id: flow.flow_id,
        code: code.trim(),
      });
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
    </ProviderCard>
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
