import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, raw } from "../api.js";
import { useApp } from "../state.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.jsx";

// Common props for a per-provider connection row: the provider's status (API shape,
// kept as any) and a reload callback that refetches after connect/disconnect.
interface RowProps {
  st: any;
  reload: () => void;
}

// ConnectionsTab drives the per-user AGENT provider auth flows (Claude / Codex /
// opencode) entirely in the WebUI. Git-hosting providers (GitHub / Bitbucket) live
// in their own GitTab. Secrets are stored in the container home by the Agent — the
// Console never holds them.
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
      <ClaudeRow st={conns.claude} reload={reload} />
      <CodexRow st={conns.codex} reload={reload} />
      <OpencodeRow st={conns.opencode} reload={reload} />
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
