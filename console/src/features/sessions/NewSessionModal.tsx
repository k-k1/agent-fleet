// NewSessionModal — create a session. shell is the default one-click kind;
// agent kinds add a model selector + repo source (existing dir / provider clone /
// URL clone) with branch forking and parallel working copies; ssm hands off to
// the SSO login modal after create. Port of the old components/NewSessionModal.
//
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { RepoPicker } from "../repos/RepoPicker.tsx";
import type { RepoSelection } from "../repos/RepoPicker.tsx";
import { DirPicker } from "../repos/DirPicker.tsx";
import type { RepoLite } from "../repos/DirPicker.tsx";
import { SsmLoginModal } from "./SsmLoginModal.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { readKindAvail, writeKindAvail } from "../../lib/kindavail.ts";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../../lib/reponame.ts";
import { agentOf, availableKinds, newSessionKinds } from "../../agents/registry.ts";
import { useSettings, CLAUDE_MODELS } from "../../lib/settings.ts";
import { hostColorBase } from "../../lib/termcolor.ts";
import type { SsmHost } from "../../types/session.ts";

// lastSeg: final path/name segment, used only to preview the placeholder title.
const lastSeg = (full: string) => (full.split("/").pop() || "").replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 40);

const SOURCE_HELP = {
  dir: "既存のフォルダを選んで起動します（clone しません）。リポジトリはブランチ付きで一覧、ホームのまま(~)でも起動できます。",
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
};

type Source = "dir" | "picker" | "url";

interface NewSessionModalProps {
  onClose: () => void;
  onCreated: (name: string, cloned: boolean, repo: string, kind: string) => void;
}

export function NewSessionModal({ onClose, onCreated }: NewSessionModalProps) {
  const toast = useToast();
  const settings = useSettings();
  // Optional user title → claude --name. The identity slug is server-allocated.
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState("shell");
  const [ssmHosts, setSsmHosts] = useState<SsmHost[] | null>(null);
  const [ssmHostId, setSsmHostId] = useState("");
  // After creating a kind=ssm session: the created name while the SSO handshake runs.
  const [ssmLogin, setSsmLogin] = useState<string | null>(null);
  const [ssmForce, setSsmForce] = useState(false);
  // Kind availability, seeded from the cache so buttons render instantly.
  const [avail, setAvail] = useState(readKindAvail);
  const [loaded, setLoaded] = useState(false);
  const [model, setModel] = useState(settings.defaultModel);
  const [source, setSource] = useState<Source>("dir");
  const [sourceTouched, setSourceTouched] = useState(false);
  const [sel, setSel] = useState<RepoSelection | null>(null);
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [newBranch, setNewBranch] = useState("");
  const [dir, setDir] = useState("");
  const [repos, setRepos] = useState<RepoLite[]>([]);
  const [reposLoaded, setReposLoaded] = useState(false);
  const [copyMode, setCopyMode] = useState<"reuse" | "new">("new");
  const [repoName, setRepoName] = useState("");
  const [repoNameEdited, setRepoNameEdited] = useState(false);
  const [busy, setBusy] = useState(false);

  // Existing working copies — to detect "already cloned" and offer a second copy.
  useEffect(() => {
    let alive = true;
    api("api/repos")
      .then((d) => {
        if (!alive) return;
        setRepos(d.repos || []);
        setReposLoaded(true);
      })
      .catch(() => alive && setReposLoaded(true));
    return () => {
      alive = false;
    };
  }, []);

  // Default the source once we know if any working copies exist (dir when some,
  // picker to clone otherwise). Stops once the user picks.
  useEffect(() => {
    if (!reposLoaded || sourceTouched) return;
    setSource(repos.length ? "dir" : "picker");
  }, [reposLoaded, repos, sourceTouched]);

  const chooseSource = (v: Source) => {
    setSourceTouched(true);
    setSource(v);
  };

  // Provider auth + SSM hosts gate the kind buttons; cached for instant render.
  useEffect(() => {
    let alive = true;
    Promise.all([api("api/connections").catch(() => ({})), api("api/ssm/hosts").catch(() => [])]).then(
      ([c, hosts]) => {
        if (!alive) return;
        const hs = Array.isArray(hosts) ? hosts : [];
        setSsmHosts(hs);
        const a = availableKinds({ conns: c, ssmHostCount: hs.length });
        setAvail(a);
        writeKindAvail(a);
        setLoaded(true);
      },
    );
    return () => {
      alive = false;
    };
  }, []);

  const agent = agentOf(kind);
  const hasModel = agent.caps.model;
  const isSSM = kind === "ssm";
  const isAgent = agent.caps.runsInDir;

  const kindAvail: Record<string, boolean> = { ...avail, shell: true };
  const ssmHost = (ssmHosts || []).find((h) => h.id === ssmHostId) || null;

  const titlePlaceholder =
    isAgent && source === "dir" && dir
      ? lastSeg(dir)
      : isAgent && sel
        ? lastSeg(sel.fullName)
        : isSSM && ssmHost
          ? lastSeg(ssmHost.alias)
          : kind;

  const cloneUrl = !isAgent ? "" : source === "picker" ? sel?.cloneUrl : source === "url" ? url.trim() : "";
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
  const cloning = isAgent && source !== "dir" && !!cloneUrl;
  const cloneNewBranch = cloning ? newBranch.trim() : "";
  const targetBranch = cloneNewBranch || cloneBranch;

  const derivedRepo = cloning ? deriveRepoName(cloneUrl) : "";
  const existing = (cloning && repos.find((r) => r.name === derivedRepo)) || null;
  const repoNames = new Set(repos.map((r) => r.name));
  const suggestedRepoName = derivedRepo
    ? uniqueRepoName(`${derivedRepo}-${sanitizeSeg(targetBranch)}`, repoNames)
    : "";

  // Default the mode when a copy is found: reuse when the wanted branch is
  // already checked out, else a separate copy (don't clobber).
  useEffect(() => {
    const e = repos.find((r) => r.name === derivedRepo) || null;
    if (e) setCopyMode(e.branch === cloneBranch ? "reuse" : "new");
  }, [derivedRepo, cloneBranch, repos]);

  // Auto-fill the separate-copy folder name until edited.
  useEffect(() => {
    if (!repoNameEdited) setRepoName(suggestedRepoName);
  }, [suggestedRepoName, repoNameEdited]);

  const newCopy = cloning && (!!cloneNewBranch || (!!existing && copyMode === "new"));
  const repoNameOk = !newCopy || (repoNameRe.test(repoName.trim()) && !repoNames.has(repoName.trim()));

  const sourceOk = !isAgent || source === "dir" || !!cloneUrl;
  const ssmOk = !isSSM || !!ssmHostId;
  const canSubmit = sourceOk && repoNameOk && ssmOk && !busy;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (ssmLogin || !canSubmit) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/sessions", "POST", {
        title: title.trim(),
        kind,
        model: hasModel ? model : "",
        dir: isAgent && source === "dir" ? dir.trim() : "",
        remote_url: cloning ? cloneUrl : "",
        branch: cloning ? cloneBranch : "",
        new_branch: cloning ? cloneNewBranch : "",
        repo_name: newCopy ? repoName.trim() : "",
        ssm_host_id: isSSM ? ssmHostId : "",
        ssm_force_login: isSSM ? ssmForce : false,
        color: isSSM ? hostColorBase(settings.ssmHostColors?.[ssmHostId], ssmHostId) : "",
      });
      if (res && res.error) {
        toast("作成に失敗: " + errText(res.error));
        return;
      }
      const slug = (res && res.name) || "";
      if (isSSM) {
        setSsmLogin(slug);
        return;
      }
      onCreated(slug, cloning, cloning ? (res && res.repo) || "" : "", kind);
    } finally {
      setBusy(false);
    }
  };

  if (ssmLogin) {
    return <SsmLoginModal name={ssmLogin} onReady={(n) => onCreated(n, false, "", kind)} onCancel={onClose} />;
  }

  return (
    <Modal title="新しいセッション" onClose={onClose} className="session-modal" as="form" onSubmit={submit} lockClose={busy}>
      <div className="ui-modal-body">
        {/* 種類 — shell 左 / 既定。registry 駆動で、利用可能な kind だけ並ぶ。 */}
        <div className="ui-field">
          <span className="ui-field-label">種類</span>
          <div className="ui-seg big">
            {newSessionKinds.map((k) => {
              if (k !== "shell" && !kindAvail[k]) return null;
              const a = agentOf(k);
              return (
                <button
                  key={k}
                  type="button"
                  className={"seg-btn kind-" + a.cssClass + (kind === k ? " active" : "")}
                  onClick={() => setKind(k)}
                >
                  <Icon name={a.icon} className="seg-ic" />
                  {a.label}
                  <span className="seg-sub">{a.launchHint}</span>
                </button>
              );
            })}
          </div>
          {loaded && !newSessionKinds.some((k) => k !== "shell" && kindAvail[k]) && (
            <span className="ui-field-hint">
              claude / codex / opencode / ssm は、
              <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("agents")}>
                設定
              </button>
              で認証・ホスト登録すると選べるようになります。
            </span>
          )}
        </div>

        {/* モデル（claude のみ） */}
        {hasModel && (
          <div className="ui-field">
            <span className="ui-field-label">モデル</span>
            <div className="ui-seg">
              {CLAUDE_MODELS.map(([v, label]) => (
                <button
                  key={v || "default"}
                  type="button"
                  className={"seg-btn" + (model === v ? " active" : "")}
                  onClick={() => setModel(v)}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* リポジトリ（claude / opencode / codex） */}
        {isAgent && (
          <>
            <div className="ui-field">
              <span className="ui-field-label">リポジトリ</span>
              <div className="ui-seg">
                {(
                  [
                    ["dir", "場所を選ぶ"],
                    ["picker", "接続から clone"],
                    ["url", "URL から clone"],
                  ] as const
                ).map(([v, label]) => (
                  <button
                    key={v}
                    type="button"
                    className={"seg-btn" + (source === v ? " active" : "")}
                    onClick={() => chooseSource(v)}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <span className="ui-field-hint">{SOURCE_HELP[source]}</span>

              {source === "dir" && <DirPicker value={dir} onChange={setDir} repos={repos} />}
              {source === "picker" && <RepoPicker onChange={setSel} />}
              {source === "url" && (
                <>
                  <label className="ui-field">
                    <span className="ui-field-label">clone URL</span>
                    <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://… / git@…" />
                  </label>
                  <label className="ui-field">
                    <span className="ui-field-label">ブランチ（任意）</span>
                    <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="既定ブランチ" />
                  </label>
                </>
              )}
            </div>

            {/* 新規ブランチ（任意）：基点ブランチから新ブランチを作成して切替 */}
            {cloning && (
              <div className="ui-field">
                <span className="ui-field-label">新規ブランチ（任意）</span>
                <input
                  value={newBranch}
                  onChange={(e) => setNewBranch(e.target.value)}
                  placeholder={`${cloneBranch || "既定ブランチ"} から作成`}
                />
                <span className="ui-field-hint">
                  指定すると <code>{cloneBranch || "既定ブランチ"}</code> を基点に新しいブランチを作成して切り替えます。空なら基点ブランチのまま。
                </span>
                {cloneNewBranch && (
                  <label className="ui-field">
                    <span className="ui-field-label">フォルダ名</span>
                    <input
                      value={repoName}
                      onChange={(e) => {
                        setRepoName(e.target.value);
                        setRepoNameEdited(true);
                      }}
                      placeholder={suggestedRepoName}
                    />
                    <span className="ui-field-hint">
                      {repoNameOk ? (
                        <>
                          作業コピーは <code>{repoName || suggestedRepoName}</code> に clone します。
                        </>
                      ) : (
                        "英数字始まりの一意な名前にしてください（既存の作業コピーと重複不可）。"
                      )}
                    </span>
                  </label>
                )}
              </div>
            )}

            {/* 既存の作業コピーがある場合：checkout 共用 or 並行 clone */}
            {cloning && existing && !cloneNewBranch && (
              <div className="ui-field">
                <span className="ui-field-label">作業コピー</span>
                <span className="ui-field-hint">
                  この repo の作業コピー「{existing.name}」(現在 <code>{existing.branch}</code>) が既にあります。
                </span>
                <div className="ui-seg">
                  <button
                    type="button"
                    className={"seg-btn" + (copyMode === "reuse" ? " active" : "")}
                    onClick={() => setCopyMode("reuse")}
                  >
                    既存で作業
                    <span className="seg-sub">{cloneBranch || "既定ブランチ"} に checkout</span>
                  </button>
                  <button
                    type="button"
                    className={"seg-btn" + (copyMode === "new" ? " active" : "")}
                    onClick={() => setCopyMode("new")}
                  >
                    別コピーを新規
                    <span className="seg-sub">並行作業用に別フォルダへ clone</span>
                  </button>
                </div>
                {copyMode === "new" && (
                  <label className="ui-field">
                    <span className="ui-field-label">フォルダ名</span>
                    <input
                      value={repoName}
                      onChange={(e) => {
                        setRepoName(e.target.value);
                        setRepoNameEdited(true);
                      }}
                      placeholder={suggestedRepoName}
                    />
                    {!repoNameOk && (
                      <span className="ui-field-hint">
                        英数字始まりの一意な名前にしてください（既存の作業コピーと重複不可）。
                      </span>
                    )}
                  </label>
                )}
              </div>
            )}
          </>
        )}

        {/* SSM ログイン先 */}
        {isSSM && (
          <div className="ui-field">
            <span className="ui-field-label">ログイン先ホスト</span>
            {ssmHosts === null ? (
              <p className="sm-muted">読み込み中…</p>
            ) : ssmHosts.length === 0 ? (
              <span className="ui-field-hint">
                登録済みのホストがありません。
                <button type="button" className="linklike" onClick={() => useSettingsUI.getState().openSettings("ssm")}>
                  設定 → SSM
                </button>
                で登録してください。
              </span>
            ) : (
              <>
                <select value={ssmHostId} onChange={(e) => setSsmHostId(e.target.value)}>
                  <option value="">— ホストを選択 —</option>
                  {ssmHosts.map((h) => (
                    <option key={h.id} value={h.id}>
                      {h.alias}
                      {h.accountId ? ` (acct ${h.accountId})` : ""}
                    </option>
                  ))}
                </select>
                <label className="ssm-check">
                  <input type="checkbox" checked={ssmForce} onChange={(e) => setSsmForce(e.target.checked)} />
                  強制的に再ログイン（キャッシュ済みでも aws sso logout → login）
                </label>
                <span className="ui-field-hint">
                  作成後、認証が必要ならモーダルに <code>aws sso login</code> の URL が出ます。別タブで承認すると
                  接続します（AWS の秘密情報は Agent Fleet に保存されません）。
                  <br />⚠ <b>自分で開始したこのログインのみ承認してください</b>（身に覚えのないコード/URL は入力しない）。
                </span>
              </>
            )}
          </div>
        )}

        {/* タイトル（任意） */}
        <label className="ui-field">
          <span className="ui-field-label">タイトル（任意）</span>
          <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={titlePlaceholder} />
          <span className="ui-field-hint">
            一覧と Claude on the web で識別する名前。空なら自動（<code>{titlePlaceholder}</code> など）。
          </span>
        </label>
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          キャンセル
        </Button>
        <Button variant="primary" type="submit" disabled={!canSubmit}>
          {busy ? (cloning ? "Cloning…" : "作成中…") : isSSM ? "接続" : "作成して開く"}
        </Button>
      </footer>
    </Modal>
  );
}
