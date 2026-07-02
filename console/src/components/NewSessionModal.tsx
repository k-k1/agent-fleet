import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../api.js";
import { useApp } from "../state.jsx";
import RepoPicker from "./RepoPicker.jsx";
import Modal from "./Modal.jsx";
import { useToast } from "./ToastProvider.jsx";
import SsmLoginModal from "./SsmLoginModal.jsx";
import { readKindAvail, writeKindAvail } from "../lib/kindavail.js";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../lib/reponame.js";
import { agentOf, availableKinds, newSessionKinds } from "../agents/registry.ts";
import type { FormEvent } from "react";
import type { Session, SsmHost } from "../types/session.ts";
import type { RepoSelection } from "./RepoPicker.jsx";

// NewSessionModal: a clear, roomy dialog for creating a session.
// shell is the left / default kind — a one-click shell needs no repo, no dir, and
// an auto-filled name. claude additionally offers a model and a repo source
// (provider picker / clone URL / none).
const lastSeg = (full: string) =>
  (full.split("/").pop() || "").replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 40);

// uniqueName returns base, or base-2 / base-3 … when already taken.
const uniqueName = (base: string, taken: Set<string>) => {
  if (!taken.has(base)) return base;
  for (let i = 2; i < 1000; i++) {
    const n = `${base}-${i}`;
    if (!taken.has(n)) return n;
  }
  return base;
};


const SOURCE_HELP = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
  none: "リポジトリを clone せず、ホーム(~) でそのまま起動します。",
};

interface NewSessionModalProps {
  onClose: () => void;
  onCreated: (name: string, cloned: boolean, repo: string) => void;
}

export default function NewSessionModal({ onClose, onCreated }: NewSessionModalProps) {
  const { openSettings } = useApp();
  const toast = useToast();
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);
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
  const [source, setSource] = useState<"picker" | "url" | "none">("picker");
  const [sel, setSel] = useState<RepoSelection | null>(null); // picker: { cloneUrl, fullName, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [dir, setDir] = useState("");
  const [taken, setTaken] = useState<Set<string>>(new Set());
  const [repos, setRepos] = useState<{ name: string; branch?: string }[]>([]); // existing working copies
  const [copyMode, setCopyMode] = useState<"reuse" | "new">("new"); // when a copy exists
  const [repoName, setRepoName] = useState(""); // target folder for a separate copy
  const [repoNameEdited, setRepoNameEdited] = useState(false);
  const [busy, setBusy] = useState(false);

  // Existing session names, for auto-naming uniqueness.
  useEffect(() => {
    let alive = true;
    api("api/sessions")
      .then((d) => alive && setTaken(new Set((d.sessions || []).map((s: Session) => s.name))))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  // Existing working copies, to detect "this repo is already cloned" and offer a
  // separate working copy for a different branch.
  useEffect(() => {
    let alive = true;
    api("api/repos")
      .then((d) => alive && setRepos(d.repos || []))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

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

  // Auto-fill the name (until the user types their own). An agent with a chosen repo
  // names after the repo; an SSM session after its host alias; else the kind word.
  const base = isAgent && sel ? lastSeg(sel.fullName) : isSSM && ssmHost ? lastSeg(ssmHost.alias) : kind;
  useEffect(() => {
    if (!nameEdited) setName(uniqueName(base, taken));
  }, [base, taken, nameEdited]);

  const onPick = (s: RepoSelection | null) => setSel(s);

  const cloneUrl = !isAgent ? "" : source === "picker" ? sel?.cloneUrl : source === "url" ? url.trim() : "";
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
  const cloning = isAgent && source !== "none" && !!cloneUrl;

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

  // shell never needs a repo; an agent's repo is optional (none is allowed).
  const sourceOk = !isAgent || source === "none" || !!cloneUrl;
  const ssmOk = !isSSM || !!ssmHostId;
  const canSubmit = !!name.trim() && sourceOk && repoNameOk && ssmOk && !busy;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (ssmLogin || !canSubmit) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/sessions", "POST", {
        name: name.trim(),
        kind,
        model: hasModel ? model : "",
        dir: isAgent && source === "none" ? dir.trim() : "",
        remote_url: cloning ? cloneUrl : "",
        branch: cloning ? cloneBranch : "",
        repo_name: newCopy ? repoName.trim() : "",
        ssm_host_id: isSSM ? ssmHostId : "",
        ssm_force_login: isSSM ? ssmForce : false,
      });
      if (res && res.error) {
        toast("作成に失敗: " + errText(res.error));
        return;
      }
      // SSM: the session (tmux) is launched; hand off to SsmLoginModal to drive the SSO
      // handshake and attach the terminal only once ready (see early return below).
      if (isSSM) {
        setSsmLogin(name.trim());
        return;
      }
      // Pass the cloned repo dir basename (server echoes it as `repo`) so the
      // caller can refresh + reveal it in the Files tree once the clone lands.
      onCreated(name.trim(), cloning, cloning ? (res && res.repo) || "" : "");
    } finally {
      setBusy(false);
    }
  };

  // After an ssm session is created, the SSO handshake is driven by the shared modal;
  // it attaches the terminal (onCreated) on ready, or closes on cancel.
  if (ssmLogin) {
    return (
      <SsmLoginModal name={ssmLogin} onReady={(n) => onCreated(n, false, "")} onCancel={onClose} />
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
                    className={"seg-btn" + (kind === k ? " active" : "")}
                    onClick={() => setKind(k)}
                  >
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
                    ["picker", "接続から選ぶ"],
                    ["url", "URL 手入力"],
                    ["none", "リポなし"],
                  ] as const).map(([v, label]) => (
                    <button
                      key={v}
                      type="button"
                      className={"seg-btn" + (source === v ? " active" : "")}
                      onClick={() => setSource(v)}
                    >
                      {label}
                    </button>
                  ))}
                </div>
                <div className="field-help">{SOURCE_HELP[source]}</div>

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
                {source === "none" && (
                  <label className="pick-field">
                    <span>ディレクトリ</span>
                    <input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="既定 ~（ホーム）" />
                  </label>
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

          {/* 名前 */}
          <div className="field">
            <div className="field-label">セッション名</div>
            <input
              className="name-input"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameEdited(true);
              }}
              placeholder="例: my-app（英数・_・- ）"
            />
            <div className="field-help">一覧に表示される識別名。自動入力済み（編集可）。</div>
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
