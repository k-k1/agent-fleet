// StartModal —「はじめる」hub (起動導線 Ph2/Ph3): the WS bar's single entry point
// for starting anything. Place-first: chat (assistants, repo-less), an existing
// working copy (→ the per-repo 作業を始める dialog), clone-and-continue, a home
// (repo-less) agent session, and the folded その他 track (shell direct / SSM —
// its host picker lives here since NewSessionModal was retired in Ph3). Entry
// points that already know the place (repo row 起動) skip this hub.
import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { resolveModel } from "../../lib/repoLast.ts";
import { useSettings } from "../../lib/settings.ts";
import { ModelPicker } from "../../ui/ModelPicker.tsx";
import { groupedRepos } from "../../lib/project.ts";
import { hostColorBase } from "../../lib/termcolor.ts";
import { useSettingsUI } from "../settings/store.ts";
import { useReposStore } from "./store.ts";
import type { Repo } from "./store.ts";
import { CloneForm } from "./CloneForm.tsx";
import type { CloneSource } from "./CloneForm.tsx";
import { cloneRepo } from "./clone.ts";
import { useStartWork } from "./useStartWork.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal } from "../sessions/open.ts";
import { SsmLoginModal } from "../sessions/SsmLoginModal.tsx";
import { assistantList } from "../chat/api.ts";
import { openAssistantDraft } from "../chat/open.ts";
import type { Assistant } from "../../types/assistant.ts";
import type { SsmHost } from "../../types/session.ts";
import { deriveRepoName } from "../../lib/reponame.ts";

interface StartModalProps {
  /** Connection-gated chat-capable agent kinds (claude / opencode / codex). */
  kinds: string[];
  onClose: () => void;
  /** A working copy was picked (existing or freshly cloned) — the host closes
   * this hub and opens the per-repo 作業を始める dialog on it. */
  onPickRepo: (r: Repo) => void;
}

type Stage = "place" | "clone" | "home" | "ssm";

export function StartModal({ kinds, onClose, onPickRepo }: StartModalProps) {
  const toast = useToast();
  const settings = useSettings();
  const repos = useReposStore((s) => s.repos);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const startWork = useStartWork();

  const [stage, setStage] = useState<Stage>("place");
  const [busy, setBusy] = useState(false);

  // --- place: chat (assistants inline), repos, +clone, home, その他 ---
  const [chatOpen, setChatOpen] = useState(false);
  const [otherOpen, setOtherOpen] = useState(false);
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  useEffect(() => {
    let alive = true;
    assistantList()
      .then((r) => alive && setAssistants(r.assistants || []))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);
  // Base clones only — worktrees are task copies, launched from their tree rows.
  const bases = groupedRepos(repos).map((g) => g[0]);

  const startShell = async () => {
    if (busy) return;
    setBusy(true);
    const r = await startWork({ dir: "", repo: "" }, { kind: "shell", model: "", prompt: "", images: [], worktree: false, base: "", newBranch: "" });
    setBusy(false);
    if (r.ok) onClose();
  };

  // --- clone: shared CloneForm; forking/folder naming stays in the worktree stage ---
  const [src, setSrc] = useState<CloneSource>({ cloneUrl: "", branch: "" });
  const derived = src.cloneUrl ? deriveRepoName(src.cloneUrl) : "";
  const already = derived ? repos.find((r) => r.name === derived) : undefined;
  const doClone = async () => {
    if (!src.cloneUrl || busy) return;
    setBusy(true);
    const res = await cloneRepo({ remote_url: src.cloneUrl, branch: src.branch, name: "" }, toast);
    setBusy(false);
    if (!res.ok) return; // stay here; the toast said why
    const repo = useReposStore.getState().repos.find((r) => r.name === res.name);
    if (repo) {
      setStage("place"); // the hub stays mounted below the launch stage — leave it reset
      onPickRepo(repo);
    } else onClose(); // clone landed but the fresh list hasn't caught up — the tree has it
  };

  // --- ssm: host picker + SSO handshake (NewSessionModal の SSM 面を移設, Ph3) ---
  const [ssmHosts, setSsmHosts] = useState<SsmHost[] | null>(null); // null = not fetched yet
  const [ssmHostId, setSsmHostId] = useState("");
  const [ssmForce, setSsmForce] = useState(false);
  // After creating a kind=ssm session: the created name while the SSO handshake runs.
  const [ssmLogin, setSsmLogin] = useState<string | null>(null);
  useEffect(() => {
    if (stage !== "ssm" || ssmHosts !== null) return;
    let alive = true;
    api("api/ssm/hosts")
      .then((hosts) => alive && setSsmHosts(Array.isArray(hosts) ? hosts : []))
      .catch(() => alive && setSsmHosts([]));
    return () => {
      alive = false;
    };
  }, [stage, ssmHosts]);
  const startSsm = async () => {
    if (!ssmHostId || busy) return;
    setBusy(true);
    const res = await apiJSON("api/sessions", "POST", {
      kind: "ssm",
      ssm_host_id: ssmHostId,
      ssm_force_login: ssmForce,
      color: hostColorBase(settings.ssmHostColors?.[ssmHostId], ssmHostId),
    });
    setBusy(false);
    if (res && res.error) {
      toast("作成に失敗: " + errText(res.error));
      return;
    }
    setSsmLogin((res && res.name) || "");
  };

  // --- home: repo-less agent session (kind / model / first prompt) ---
  const [kind, setKind] = useState(kinds[0] || "claude");
  const [model, setModel] = useState(() => resolveModel(kinds[0] || "claude", "", settings.defaultModel));
  const [prompt, setPrompt] = useState("");
  const startHome = async () => {
    if (busy) return;
    setBusy(true);
    const r = await startWork(
      { dir: "", repo: "" },
      { kind, model: agentOf(kind).caps.model ? model : "", prompt: prompt.trim(), images: [], worktree: false, base: "", newBranch: "" },
    );
    setBusy(false);
    if (r.ok) onClose();
  };

  // SSO handshake takes over the dialog (modal swap — safe since backClose
  // suppresses its own history echoes).
  if (ssmLogin != null) {
    return (
      <SsmLoginModal
        name={ssmLogin}
        onReady={(n) => {
          void refreshSessions();
          openSessionTerminal(n);
          onClose();
        }}
        onCancel={onClose}
      />
    );
  }

  return (
    <Modal
      title={
        <>
          <Icon name="rocket" /> はじめる
        </>
      }
      onClose={onClose}
      lockClose={busy}
    >
      {stage === "place" && (
        <div className="ui-modal-body">
          <div className="ui-field">
            <span className="ui-field-label">どこで作業しますか？</span>
            <div className="start-list">
              <button type="button" className="start-row" onClick={() => setChatOpen((o) => !o)}>
                <Icon name="comment-discussion" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">チャット（アシスタント）</span>
                  <span className="start-row-desc">リポジトリ不要。質問・翻訳・要約はこちら</span>
                </span>
                <Icon name={chatOpen ? "chevron-down" : "chevron-right"} className="start-row-chev" />
              </button>
              {chatOpen &&
                assistants.map((a) => (
                  <button
                    key={a.id}
                    type="button"
                    className="start-row start-sub"
                    title={a.description || a.name}
                    onClick={() => {
                      openAssistantDraft(a.id);
                      onClose();
                    }}
                  >
                    <Icon name={a.icon || "comment"} className="start-row-ic" />
                    <span className="start-row-body">
                      <span className="start-row-title">{a.name}</span>
                    </span>
                    {a.builtin && <span className="start-row-meta">常設</span>}
                  </button>
                ))}
              {bases.map((r) => (
                <button key={r.name} type="button" className="start-row" onClick={() => onPickRepo(r)}>
                  <Icon name="repo" className="start-row-ic" />
                  <span className="start-row-body">
                    <span className="start-row-title">{r.name}</span>
                    <span className="start-row-desc">
                      {r.branch || ""}
                      {r.dirty ? " ・ 未コミットあり" : ""}
                    </span>
                  </span>
                  <Icon name="chevron-right" className="start-row-chev" />
                </button>
              ))}
              <button type="button" className="start-row action" onClick={() => setStage("clone")}>
                <Icon name="add" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">新しいリポジトリを clone…</span>
                </span>
              </button>
              <button type="button" className="start-row" onClick={() => setStage("home")}>
                <Icon name="home" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">ホームで起動（リポジトリなし）</span>
                  <span className="start-row-desc">~ でエージェントを走らせる・下書きや調べもの向け</span>
                </span>
                <Icon name="chevron-right" className="start-row-chev" />
              </button>
            </div>
          </div>
          <button type="button" className="start-other" onClick={() => setOtherOpen((o) => !o)}>
            <Icon name={otherOpen ? "chevron-down" : "chevron-right"} /> その他 — shell / SSM
          </button>
          {otherOpen && (
            <div className="start-list">
              <button type="button" className="start-row" disabled={busy} onClick={() => void startShell()}>
                <Icon name="terminal" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">shell</span>
                  <span className="start-row-desc">通常のシェル (bash) をすぐ開きます</span>
                </span>
              </button>
              <button type="button" className="start-row" onClick={() => setStage("ssm")}>
                <Icon name="vm" className="start-row-ic" />
                <span className="start-row-body">
                  <span className="start-row-title">SSM — 別ホストへログイン</span>
                  <span className="start-row-desc">AWS EC2 に SSM でログインします</span>
                </span>
                <Icon name="chevron-right" className="start-row-chev" />
              </button>
            </div>
          )}
        </div>
      )}

      {stage === "clone" && (
        <>
          <div className="ui-modal-body">
            <CloneForm onChange={setSrc} />
            {already && (
              <div className="ui-field">
                <span className="ui-field-hint">
                  「{already.name}」は clone 済みです。別コピーが必要なら Repos の「クローン」からフォルダ名を分けて clone してください。
                </span>
                <Button
                  small
                  icon="repo"
                  onClick={() => {
                    setStage("place");
                    onPickRepo(already);
                  }}
                >
                  既存の {already.name} からはじめる
                </Button>
              </div>
            )}
            <span className="ui-field-hint">
              新規ブランチやフォルダ名は、clone 後の「作業を始める」（worktree）で決められます。
            </span>
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={() => setStage("place")} disabled={busy}>
              場所を変更
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              キャンセル
            </Button>
            <Button variant="primary" onClick={() => void doClone()} disabled={!src.cloneUrl || !!already || busy}>
              {busy ? "Cloning…" : "clone して続行"}
            </Button>
          </footer>
        </>
      )}

      {stage === "home" && (
        <>
          <div className="ui-modal-body">
            <div className="ui-field">
              <span className="ui-field-label">エージェント</span>
              <div className="ui-seg big">
                {kinds.map((k) => {
                  const a = agentOf(k);
                  return (
                    <button
                      key={k}
                      type="button"
                      className={"seg-btn kind-" + a.cssClass + (kind === k ? " active" : "")}
                      onClick={() => {
                        setKind(k);
                        setModel(resolveModel(k, "", settings.defaultModel));
                      }}
                    >
                      <Icon name={a.icon} className="seg-ic" />
                      {a.label}
                      <span className="seg-sub">{a.launchHint}</span>
                    </button>
                  );
                })}
              </div>
            </div>
            {agentOf(kind).caps.model && (
              <div className="ui-field">
                <span className="ui-field-label">モデル</span>
                <ModelPicker kind={kind} model={model} onChange={setModel} />
              </div>
            )}
            <div className="ui-field">
              <span className="ui-field-label">場所</span>
              <span className="ui-field-hint">
                ホーム（<code>~</code>）で起動します。リポジトリの作業はせず、下書き・調べもの・使い捨ての作業向けです。
              </span>
            </div>
            <div className="ui-field">
              <span className="ui-field-label">最初のプロンプト（任意）</span>
              <textarea
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault();
                    void startHome();
                  }
                }}
                rows={4}
                autoFocus
                placeholder="起動後にこのプロンプトを送信します。空なら送信せず開くだけ。"
              />
            </div>
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={() => setStage("place")} disabled={busy}>
              場所を変更
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              キャンセル
            </Button>
            <Button variant="primary" onClick={() => void startHome()} disabled={busy}>
              {busy ? "起動中…" : "起動"}
            </Button>
          </footer>
        </>
      )}

      {stage === "ssm" && (
        <>
          <div className="ui-modal-body">
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
                    接続後、認証が必要ならモーダルに <code>aws sso login</code> の URL が出ます。別タブで承認すると
                    接続します（AWS の秘密情報は Agent Fleet に保存されません）。
                    <br />⚠ <b>自分で開始したこのログインのみ承認してください</b>（身に覚えのないコード/URL は入力しない）。
                  </span>
                </>
              )}
            </div>
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" className="launch-back" icon="arrow-left" onClick={() => setStage("place")} disabled={busy}>
              場所を変更
            </Button>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              キャンセル
            </Button>
            <Button variant="primary" onClick={() => void startSsm()} disabled={!ssmHostId || busy}>
              {busy ? "作成中…" : "接続"}
            </Button>
          </footer>
        </>
      )}
    </Modal>
  );
}
