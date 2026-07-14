import { useEffect, useState } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { usePolling } from "./usePolling.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { InternalRepoBrowser } from "./InternalRepoBrowser.tsx";
import { useConnections } from "./useConnections.ts";
import { ProviderCard, StatusPill, DeviceSteps, DisconnectButton } from "./providerCard.tsx";

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
export function GitTab() {
  const wsState = useWorkspaceStore((s) => s.state);
  const startWs = useWorkspaceStore((s) => s.start);
  // git-hosting auth is agent-proxied (proxyAgentREST → 502 while stopped), so the
  // tab needs a running workspace — same as the agent tab (SSM/MCP are CP-stored and
  // don't).
  const running = wsState === "running";
  const { conns, reload } = useConnections();
  useEffect(() => {
    if (running) reload();
  }, [running, reload]);

  // Internal repos are CP-native (no Agent), so they render regardless of the
  // workspace state; the external git-hosting cards still require a running Agent.
  return (
    <div className="conns">
      <InternalRepos />
      {!running ? (
        <EmptyState
          icon="debug-disconnect"
          title="外部 Git 接続はワークスペース内で実行されます"
          hint="外部プロバイダの認証はコンテナ内の Agent を経由するため、ワークスペースの起動が必要です。"
        >
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? "起動中…" : "ワークスペースを起動"}
          </Button>
        </EmptyState>
      ) : !conns ? (
        <p className="muted pad">読み込み中…</p>
      ) : (
        <>
          <div className="conn-cat">git ホスティング</div>
          <GithubRow st={conns.github} reload={reload} />
          <BitbucketRow st={conns.bitbucket} reload={reload} />
          <GlobalIdentity />
        </>
      )}
    </div>
  );
}

// An internal repo from GET /api/internal-git/repos.
interface InternalRepo {
  name: string;
  clone_url: string;
  default_branch?: string;
  created_at?: string;
}

// InternalRepos manages the tenant's self-hosted git repositories (docs/reference/
// internal-git-provider). Unlike the OAuth provider cards, this is CP-native: list /
// create / delete talk to the CP directly (api/internal-git/*), need no external
// account, and work while the workspace is stopped. Clone URLs authenticate via the
// CP-injected token, so no connect step is required.
function InternalRepos() {
  const toast = useToast();
  const [repos, setRepos] = useState<InternalRepo[] | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [browsing, setBrowsing] = useState<string | null>(null);

  const load = () =>
    api("api/internal-git/repos")
      .then((d) => setRepos(d && !d.error ? d.repos || [] : []))
      .catch(() => setRepos([]));
  useEffect(() => {
    load();
  }, []);

  const create = async () => {
    const n = name.trim();
    if (!n) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/internal-git/repos", "POST", { name: n });
      if (res && res.error) {
        toast("作成に失敗: " + (res.error.message || res.error.code || ""));
        return;
      }
      toast(`内部リポジトリ「${res.name}」を作成しました`);
      setName("");
      load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (rn: string) => {
    if (!confirm(`内部リポジトリ「${rn}」を削除します。取り消せません。よろしいですか？`)) return;
    const res = await raw(`api/internal-git/repos/${encodeURIComponent(rn)}`, { method: "DELETE" });
    if (!res.ok) {
      toast("削除に失敗しました");
      return;
    }
    toast(`「${rn}」を削除しました`);
    load();
  };

  const rename = async (oldName: string, newName: string) => {
    const res = await apiJSON(`api/internal-git/repos/${encodeURIComponent(oldName)}/rename`, "POST", {
      new_name: newName,
    });
    if (res && res.error) {
      toast("リネームに失敗: " + (res.error.message || res.error.code || ""));
      return false;
    }
    toast(`「${oldName}」→「${res.name}」にリネームしました`);
    load();
    return true;
  };

  const copyUrl = (url: string) => {
    navigator.clipboard?.writeText(url).then(
      () => toast("クローンURLをコピーしました"),
      () => {},
    );
  };

  const count = repos?.length ?? 0;
  return (
    <>
      <div className="conn-cat">内部リポジトリ（フリート内）</div>
      <ProviderCard
        id="internal"
        name="内部 Git"
        status={<StatusPill on>{count ? `${count} 個` : "利用可"}</StatusPill>}
      >
        <div className="p-desc">
          外部アカウント不要。テナント内でリポジトリを共有できます（クローン / push 可）。認証は自動注入されるトークンで透過。
        </div>
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              placeholder="リポジトリ名（例: my-repo）"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && create()}
            />
            <button disabled={busy || !name.trim()} onClick={create}>
              作成
            </button>
          </div>
          {repos === null ? (
            <p className="muted pad">読み込み中…</p>
          ) : repos.length === 0 ? (
            <p className="muted pad">リポジトリはまだありません。上で作成してください。</p>
          ) : (
            <ul className="internal-repo-list">
              {repos.map((r) => (
                <InternalRepoRow
                  key={r.name}
                  repo={r}
                  onCopy={() => copyUrl(r.clone_url)}
                  onBrowse={() => setBrowsing(r.name)}
                  onRename={rename}
                  onRemove={() => remove(r.name)}
                />
              ))}
            </ul>
          )}
        </div>
      </ProviderCard>
      {browsing && <InternalRepoBrowser name={browsing} onClose={() => setBrowsing(null)} />}
    </>
  );
}

// IdentityFields edits the commit user.name / user.email a provider uses. The Agent
// bakes it into each of that host's repo's local .git/config, so it applies to EVERY
// commit path (terminal / claude / Console), and each repo can still override it.
// Empty = use the connected account (auto-seeded).
function IdentityFields({ host, name0, email0 }: { host: string; name0?: string; email0?: string }) {
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
        toast("保存に失敗: " + (res.error.message || res.error));
        return;
      }
      toast("コミット identity を保存しました");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="git-identity">
      <div className="gi-title">コミット identity（このプロバイダの既定）</div>
      <div className="gi-row">
        <input className="cinput" placeholder="name（例: 山田太郎）" value={name} onChange={(e) => setName(e.target.value)} />
        <input className="cinput" placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
        <button disabled={busy} onClick={save}>
          保存
        </button>
      </div>
      <div className="field-help">
        空欄なら接続アカウントを使用。端末 / claude のコミットにも適用され、リポジトリごとに上書きできます。
      </div>
    </div>
  );
}

// GlobalIdentity edits the ~/.gitconfig default identity — used for repos that match no
// connected provider (no remote / direct-dir sessions).
function GlobalIdentity() {
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
        toast("保存に失敗: " + (res.error.message || res.error));
        return;
      }
      toast("既定 identity を保存しました");
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="conn-cat">既定のコミット identity（すべての git）</div>
      <div className="git-identity solo">
        <div className="gi-row">
          <input className="cinput" placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <input className="cinput" placeholder="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          <button disabled={busy} onClick={save}>
            保存
          </button>
        </div>
        <div className="field-help">
          どのプロバイダにも紐づかないリポジトリ（remote 無し等）で使う ~/.gitconfig の既定値。
          解決順は「リポ上書き ＞ プロバイダ ＞ この既定」。
        </div>
      </div>
    </>
  );
}

// InternalRepoRow is one repo in the internal list: name (editable via リネーム),
// its clone URL (click to copy), and 削除. Rename edit-state is per-row.
function InternalRepoRow({
  repo,
  onCopy,
  onBrowse,
  onRename,
  onRemove,
}: {
  repo: InternalRepo;
  onCopy: () => void;
  onBrowse: () => void;
  onRename: (oldName: string, newName: string) => Promise<boolean>;
  onRemove: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(repo.name);
  const [busy, setBusy] = useState(false);

  const commit = async () => {
    const n = draft.trim();
    if (!n || n === repo.name) {
      setEditing(false);
      setDraft(repo.name);
      return;
    }
    setBusy(true);
    const ok = await onRename(repo.name, n);
    setBusy(false);
    if (ok) setEditing(false);
  };

  if (editing) {
    return (
      <li className="internal-repo">
        <input
          className="cinput ir-rename"
          value={draft}
          autoFocus
          disabled={busy}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") commit();
            if (e.key === "Escape") {
              // Don't let the Esc bubble to the settings modal's document-level
              // close handler — it should only cancel the rename.
              e.stopPropagation();
              setEditing(false);
              setDraft(repo.name);
            }
          }}
        />
        <button type="button" disabled={busy} onClick={commit}>
          保存
        </button>
        <button
          type="button"
          className="ghost"
          disabled={busy}
          onClick={() => {
            setEditing(false);
            setDraft(repo.name);
          }}
        >
          取消
        </button>
      </li>
    );
  }
  return (
    <li className="internal-repo">
      <span className="ir-name" title={repo.name}>
        {repo.name}
      </span>
      <button type="button" className="ir-url" title="クローンURLをコピー" onClick={onCopy}>
        <code>{repo.clone_url}</code>
      </button>
      <button type="button" className="ghost" title="参照（クローン不要）" onClick={onBrowse}>
        参照
      </button>
      <button type="button" className="ghost" title="リネーム" onClick={() => setEditing(true)}>
        リネーム
      </button>
      <button type="button" className="ghost danger conn-disconnect" title="削除" onClick={onRemove}>
        削除
      </button>
    </li>
  );
}

function GithubRow({ st, reload }: RowProps) {
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [oauth, setOauth] = useState<any>(null); // { user_code, verification_uri, status }
  const [token, setToken] = useState("");

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
    let iv = (res.interval || 5) * 1000;
    poll({
      deadlineMs: (res.expires_in || 900) * 1000,
      firstDelayMs: iv,
      onExpire: () => setOauth((o: any) => ({ ...o, status: "期限切れ。やり直してください" })),
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
          setOauth((o: any) => ({ ...o, status: "失敗: " + (p.error.message || p.error.code || "") }));
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
        <>
          <div className="p-who">
            <span className="p-em" title={st.email || st.username || ""}>
              {st.username || "connected"}
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
          <div className="p-desc">OAuth（デバイスフロー）か Personal Access Token で接続。</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" onClick={startOAuth}>
                <span className="p-opt-t">
                  OAuth で接続 <span className="p-rec">推奨</span>
                </span>
                <span className="p-opt-s">ブラウザで承認するデバイスフロー。</span>
              </button>
              <button type="button" className="p-opt" onClick={() => setMode("token")}>
                <span className="p-opt-t">アクセストークンで接続</span>
                <span className="p-opt-s">Personal Access Token を貼り付け。</span>
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
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | oauth | token
  const [status, setStatus] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");

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
    poll({
      deadlineMs: 5 * 60 * 1000,
      firstDelayMs: 2500,
      onExpire: () => setStatus("タイムアウト。やり直してください"),
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
        <>
          <div className="p-who">
            <span className="p-em" title={st.email || st.username || ""}>
              {st.username || "connected"}
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
          <div className="p-desc">OAuth（コードグラント）か メール＋アプリトークンで接続。</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" onClick={startOAuth}>
                <span className="p-opt-t">
                  OAuth で接続 <span className="p-rec">推奨</span>
                </span>
                <span className="p-opt-s">別タブで承認するコードグラント。</span>
              </button>
              <button type="button" className="p-opt" onClick={() => setMode("token")}>
                <span className="p-opt-t">アプリトークンで接続</span>
                <span className="p-opt-s">Atlassian メール＋API トークン。</span>
              </button>
            </div>
          </div>
        </>
      )}
    </ProviderCard>
  );
}
