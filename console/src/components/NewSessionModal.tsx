import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../api.js";
import { useApp } from "../state.jsx";
import RepoPicker from "./RepoPicker.jsx";
import DirPicker from "./DirPicker.jsx";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { useToast } from "./ToastProvider.jsx";
import SsmLoginModal from "./SsmLoginModal.jsx";
import { readKindAvail, writeKindAvail } from "../lib/kindavail.js";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../lib/reponame.js";
import { agentOf, availableKinds, newSessionKinds } from "../agents/registry.ts";
import { useSettings } from "../lib/settings.js";
import { hostColorBase } from "../lib/termcolor.js";
import type { FormEvent } from "react";
import type { SsmHost } from "../types/session.ts";
import type { RepoSelection } from "./RepoPicker.jsx";

// NewSessionModal: a clear, roomy dialog for creating a session.
// shell is the left / default kind — a one-click shell needs no repo, no dir, and
// an auto-filled name. claude additionally offers a model and a repo source
// (provider picker / clone URL / none).
// lastSeg is the final path/name segment, used only to preview a placeholder title.
const lastSeg = (full: string) =>
  (full.split("/").pop() || "").replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 40);


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

export default function NewSessionModal({ onClose, onCreated }: NewSessionModalProps) {
  const { openSettings } = useApp();
  const toast = useToast();
  const settings = useSettings();
  // Optional user title → claude --name (web identification). The session's identity
  // is a unique slug the server auto-allocates; the client never picks a name.
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState("shell"); // shell is the default (left) kind
  const [ssmHosts, setSsmHosts] = useState<SsmHost[] | null>(null); // registered SSM host bookmarks
  const [ssmHostId, setSsmHostId] = useState("");
  // After creating a kind=ssm session, hand off to the shared SsmLoginModal (below):
  // this holds the created session name while the SSO handshake runs. null = not yet.
  const [ssmLogin, setSsmLogin] = useState<string | null>(null); // session name (string)
  const [ssmForce, setSsmForce] = useState(false); // 強制再ログイン
  // Kind availability, seeded from the last-known cache so the buttons render instantly
  // (no shell-only flash); refreshed from the server below. loaded gates the "nothing
  // set up" hint so it doesn't flash before the real status arrives.
  const [avail, setAvail] = useState(readKindAvail); // { claude, codex, opencode, ssm }
  const [loaded, setLoaded] = useState(false);
  const [model, setModel] = useState(""); // "" = claude default
  const [source, setSource] = useState<Source>("dir");
  const [sourceTouched, setSourceTouched] = useState(false); // user picked a source → stop auto-defaulting
  const [sel, setSel] = useState<RepoSelection | null>(null); // picker: { cloneUrl, fullName, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [dir, setDir] = useState(""); // source=dir: home-relative launch dir ("" = home)
  const [repos, setRepos] = useState<{ name: string; path?: string; branch?: string }[]>([]); // existing working copies
  const [reposLoaded, setReposLoaded] = useState(false);
  const [copyMode, setCopyMode] = useState<"reuse" | "new">("new"); // when a copy exists
  const [repoName, setRepoName] = useState(""); // target folder for a separate copy
  const [repoNameEdited, setRepoNameEdited] = useState(false);
  const [busy, setBusy] = useState(false);

  // Existing working copies, to detect "this repo is already cloned" and offer a
  // separate working copy for a different branch.
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

  // Default the repo source once we know whether any working copies exist: browse
  // existing folders ("場所を選ぶ", which lists repos) when there are cloned repos —
  // the common case is reusing what's here; else point at the provider picker to
  // clone. Stops once the user picks a source.
  useEffect(() => {
    if (!reposLoaded || sourceTouched) return;
    setSource(repos.length ? "dir" : "picker");
  }, [reposLoaded, repos, sourceTouched]);

  const chooseSource = (v: Source) => {
    setSourceTouched(true);
    setSource(v);
  };

  // Provider auth status + SSM hosts, fetched up front to gate the kind options: a kind
  // is offered only when it's ready (claude/codex/opencode authenticated, ssm has a
  // host). shell is always available. Result is cached (localStorage) so the next open
  // renders the buttons immediately; this fetch reconciles.
  useEffect(() => {
    let alive = true;
    Promise.all([
      api("api/connections").catch(() => ({})),
      api("api/ssm/hosts").catch(() => []),
    ]).then(([c, hosts]) => {
      if (!alive) return;
      const hs = Array.isArray(hosts) ? hosts : [];
      setSsmHosts(hs);
      // Availability per kind lives on the agent descriptors (src/agents/registry).
      const a = availableKinds({ conns: c, ssmHostCount: hs.length });
      setAvail(a);
      writeKindAvail(a);
      setLoaded(true);
    });
    return () => {
      alive = false;
    };
  }, []);

  const agent = agentOf(kind);
  const hasModel = agent.caps.model; // claude offers a model selector
  const isSSM = kind === "ssm"; // ssm has a bespoke SSO login handoff
  const isAgent = agent.caps.runsInDir; // claude/opencode/codex run in a working dir

  // shell always; the rest from the (cached, then refreshed) availability.
  const kindAvail: Record<string, boolean> = { ...avail, shell: true };

  const ssmHost = (ssmHosts || []).find((h) => h.id === ssmHostId) || null;

  // Placeholder preview of the auto title used when the user leaves the field blank:
  // the picked dir's last segment (repo/folder) for source=dir, the repo name for a
  // clone, the host alias for SSM, else the kind word.
  const titlePlaceholder =
    isAgent && source === "dir" && dir
      ? lastSeg(dir)
      : isAgent && sel
        ? lastSeg(sel.fullName)
        : isSSM && ssmHost
          ? lastSeg(ssmHost.alias)
          : kind;

  const onPick = (s: RepoSelection | null) => setSel(s);

  const cloneUrl = !isAgent ? "" : source === "picker" ? sel?.cloneUrl : source === "url" ? url.trim() : "";
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
  const cloning = isAgent && source !== "dir" && !!cloneUrl;

  // Detect an existing working copy at the default (derived) folder name.
  const derivedRepo = cloning ? deriveRepoName(cloneUrl) : "";
  const existing = (cloning && repos.find((r) => r.name === derivedRepo)) || null;
  const repoNames = new Set(repos.map((r) => r.name));
  const suggestedRepoName = derivedRepo
    ? uniqueRepoName(`${derivedRepo}-${sanitizeSeg(cloneBranch)}`, repoNames)
    : "";

  // Default the mode when a copy is found: reuse it when the wanted branch is
  // already checked out, otherwise default to a separate copy (don't clobber).
  useEffect(() => {
    const e = repos.find((r) => r.name === derivedRepo) || null;
    if (e) setCopyMode(e.branch === cloneBranch ? "reuse" : "new");
  }, [derivedRepo, cloneBranch, repos]);

  // Auto-fill the separate-copy folder name until the user edits it.
  useEffect(() => {
    if (!repoNameEdited) setRepoName(suggestedRepoName);
  }, [suggestedRepoName, repoNameEdited]);

  // A separate copy is used when cloning a repo that already exists and the user
  // chose "new"; that target folder name is sent as repo_name (else the server
  // derives/reuses the legacy name).
  const newCopy = cloning && !!existing && copyMode === "new";
  const repoNameOk = !newCopy || (repoNameRe.test(repoName.trim()) && !repoNames.has(repoName.trim()));

  // shell never needs a repo; an agent's repo is optional. The "場所を選ぶ" flow is
  // always valid (empty dir = home); clone flows need a URL.
  const sourceOk = !isAgent || source === "dir" || !!cloneUrl;
  const ssmOk = !isSSM || !!ssmHostId;
  const canSubmit = sourceOk && repoNameOk && ssmOk && !busy;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (ssmLogin || !canSubmit) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/sessions", "POST", {
        title: title.trim(), // optional; server auto-allocates the identity slug
        kind,
        model: hasModel ? model : "",
        // Working dir: the picked home-relative folder for source=dir (empty = home;
        // server resolves it against home). Clone flows leave it empty and derive the
        // dir server-side from remote_url/repo_name.
        dir: isAgent && source === "dir" ? dir.trim() : "",
        remote_url: cloning ? cloneUrl : "",
        branch: cloning ? cloneBranch : "",
        repo_name: newCopy ? repoName.trim() : "",
        ssm_host_id: isSSM ? ssmHostId : "",
        ssm_force_login: isSSM ? ssmForce : false,
        // SSM: the terminal background hue for this host (per-user setting, auto-derived
        // when unset). Stored on the session so its terminal is tinted per host.
        color: isSSM ? hostColorBase(settings.ssmHostColors?.[ssmHostId], ssmHostId) : "",
      });
      if (res && res.error) {
        toast("作成に失敗: " + errText(res.error));
        return;
      }
      // The server assigns the identity slug; use the returned session's name (the
      // client no longer knows it) for every downstream reference.
      const slug = (res && res.name) || "";
      // SSM: the session (tmux) is launched; hand off to SsmLoginModal to drive the SSO
      // handshake and attach the terminal only once ready (see early return below).
      if (isSSM) {
        setSsmLogin(slug);
        return;
      }
      // Pass the cloned repo dir basename (server echoes it as `repo`) so the
      // caller can refresh + reveal it in the Files tree once the clone lands. kind
      // lets the caller open a claude session as chat (vs terminal for other kinds).
      onCreated(slug, cloning, cloning ? (res && res.repo) || "" : "", kind);
    } finally {
      setBusy(false);
    }
  };

  // After an ssm session is created, the SSO handshake is driven by the shared modal;
  // it attaches the terminal (onCreated) on ready, or closes on cancel.
  if (ssmLogin) {
    return (
      <SsmLoginModal name={ssmLogin} onReady={(n) => onCreated(n, false, "", kind)} onCancel={onClose} />
    );
  }

  return (
    <Modal title="新しいセッション" onClose={onClose} className="session-modal" as="form" onSubmit={submit} lockClose={busy}>
        <div className="modal-body">
          {/* 種類 — shell 左 / 既定 */}
          <div className="field">
            <div className="field-label">種類</div>
            <div className="seg big">
              {/* Data-driven from the agent registry: shell is always shown; the rest
                  appear once available. Adding an agent descriptor surfaces it here. */}
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
              <div className="field-help">
                claude / codex / opencode / ssm は、
                <button type="button" className="linklike" onClick={() => { onClose(); openSettings(); }}>
                  設定
                </button>
                で認証・ホスト登録すると選べるようになります。
              </div>
            )}
          </div>

          {/* モデル（claude のみ） */}
          {hasModel && (
            <div className="field">
              <div className="field-label">モデル</div>
              <div className="seg">
                {[
                  ["", "既定"],
                  ["opus", "Opus"],
                  ["sonnet", "Sonnet"],
                  ["haiku", "Haiku"],
                ].map(([v, label]) => (
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

          {/* リポジトリ（claude / opencode） */}
          {isAgent && (
            <>
              <div className="field">
                <div className="field-label">リポジトリ</div>
                <div className="seg">
                  {([
                    ["dir", "場所を選ぶ"],
                    ["picker", "接続から clone"],
                    ["url", "URL から clone"],
                  ] as const).map(([v, label]) => (
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
                <div className="field-help">{SOURCE_HELP[source]}</div>

                {source === "dir" && <DirPicker value={dir} onChange={setDir} repos={repos} />}
                {source === "picker" && <RepoPicker onChange={onPick} />}
                {source === "url" && (
                  <div className="stack">
                    <label className="pick-field">
                      <span>clone URL</span>
                      <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://… / git@…" />
                    </label>
                    <label className="pick-field">
                      <span>ブランチ（任意）</span>
                      <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="既定ブランチ" />
                    </label>
                  </div>
                )}
              </div>

              {/* 既存の作業コピーがある場合：checkout で共用するか、別フォルダへ並行 clone するか */}
              {cloning && existing && (
                <div className="field">
                  <div className="field-label">作業コピー</div>
                  <div className="field-help">
                    この repo の作業コピー「{existing.name}」(現在 <code>{existing.branch}</code>) が既にあります。
                  </div>
                  <div className="seg">
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
                    <label className="pick-field">
                      <span>フォルダ名</span>
                      <input
                        value={repoName}
                        onChange={(e) => {
                          setRepoName(e.target.value);
                          setRepoNameEdited(true);
                        }}
                        placeholder={suggestedRepoName}
                      />
                      {!repoNameOk && (
                        <span className="field-help">
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
            <div className="field">
              <div className="field-label">ログイン先ホスト</div>
              {ssmHosts === null ? (
                <p className="muted">読み込み中…</p>
              ) : ssmHosts.length === 0 ? (
                <div className="field-help">
                  登録済みのホストがありません。
                  <button
                    type="button"
                    className="linklike"
                    onClick={() => {
                      onClose();
                      openSettings();
                    }}
                  >
                    設定 → SSM
                  </button>
                  で登録してください。
                </div>
              ) : (
                <>
                  <select
                    className="cinput"
                    value={ssmHostId}
                    onChange={(e) => setSsmHostId(e.target.value)}
                  >
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
                  <div className="field-help">
                    作成後、認証が必要ならモーダルに <code>aws sso login</code> の URL が出ます。別タブで承認すると
                    接続します（AWS の秘密情報は Agent Fleet に保存されません）。
                    <br />
                    ⚠ <b>自分で開始したこのログインのみ承認してください</b>（身に覚えのないコード/URL は入力しない）。
                  </div>
                </>
              )}
            </div>
          )}

          {/* タイトル（任意） */}
          <div className="field">
            <div className="field-label">タイトル（任意）</div>
            <input
              className="name-input"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={titlePlaceholder}
            />
            <div className="field-help">
              一覧と Claude on the web で識別する名前。空なら自動（
              <code>{titlePlaceholder}</code> など）。
            </div>
          </div>
        </div>

        <footer className="modal-foot">
          <button type="button" className="ghost" onClick={onClose} disabled={busy}>
            キャンセル
          </button>
          <button type="submit" className="primary" disabled={!canSubmit}>
            {busy ? (cloning ? "Cloning…" : "作成中…") : isSSM ? "接続" : "作成して開く"}
          </button>
        </footer>
    </Modal>
  );
}
