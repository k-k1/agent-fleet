import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON } from "../api.js";
import Icon from "../components/Icon.jsx";
import BranchModal from "../components/BranchModal.jsx";
import Modal from "../components/Modal.jsx";
import CommitGraph from "../components/CommitGraph.jsx";
import { useConfirm } from "../components/ConfirmProvider.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import { placeFixed } from "../lib/placeFixed.js";
import EmptyState from "../components/EmptyState.jsx";
import type { FormEvent } from "react";
import type { GraphCommit } from "../lib/gitgraph.js";

interface ScmStatus {
  branch?: string;
  ahead?: number;
  behind?: number;
}

// SourceControlView is the per-repo commit-graph (codeleaf lane layout), opened by
// clicking a repo in the Repos section. The working-tree changes + commit box and the
// per-commit detail/diff are split out into their own panes (変更 / commit): clicking a
// commit opens its detail beside the graph; the 変更 button opens the changes pane.
export default function SourceControlView({ repo }: { repo?: string; wrap?: boolean }) {
  const { scmRepo: ctxRepo, bumpRepos, bumpFiles, showChanges, showCommit } = useApp();
  const askConfirm = useConfirm();
  const toast = useToast();
  const scmRepo = repo !== undefined ? repo : ctxRepo;
  const enc = encodeURIComponent(scmRepo || "");
  const [status, setStatus] = useState<ScmStatus | null>(null);
  const [commits, setCommits] = useState<GraphCommit[]>([]);
  const [current, setCurrent] = useState("");
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState("");
  const [showBranch, setShowBranch] = useState(false);
  const [menu, setMenu] = useState<{ commit: GraphCommit; x: number; y: number } | null>(null); // commit right-click
  const menuRef = useRef<HTMLUListElement>(null);
  const [nbAt, setNbAt] = useState<string | null>(null); // "new branch from this commit" sha
  const [nbName, setNbName] = useState("");

  // Commit context menu: clamp on-screen, close on outside click / Esc.
  useLayoutEffect(() => {
    if (menu && menuRef.current) placeFixed(menuRef.current, menu.x, menu.y);
  }, [menu]);
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    window.addEventListener("blur", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", close);
    };
  }, [menu]);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setStatus(await api(`api/repos/${enc}/status`));
    } catch {}
    try {
      const d = await api(`api/repos/${enc}/graph?limit=300`);
      setCommits(d.commits || []);
      setCurrent(d.current || "");
    } catch {
      setCommits([]);
    } finally {
      setLoading(false);
    }
  }, [enc]);

  useEffect(() => {
    setSelected("");
    refresh();
  }, [refresh]);

  const onSelect = (sha: string) => {
    setSelected(sha);
    if (scmRepo) showCommit(scmRepo, sha);
  };

  const fetchRepo = async () => {
    await apiJSON(`api/repos/${enc}/fetch`, "POST", { prune: true });
    refresh();
    bumpRepos();
  };

  // Fast-forward the current branch to its upstream (git pull --ff-only).
  const doFF = async () => {
    const res = await apiJSON(`api/repos/${enc}/ff`, "POST", {});
    if (res && res.error) {
      toast("ff 失敗: " + (res.error.message || res.error));
      return;
    }
    refresh();
    bumpRepos();
  };

  // Create a new branch from a specific commit (right-click → このコミットから新規ブランチ).
  const createBranchAt = async (e: FormEvent) => {
    e.preventDefault();
    const name = nbName.trim();
    const sha = nbAt;
    if (!name || !sha) return;
    const res = await apiJSON(`api/repos/${enc}/checkout`, "POST", { branch: name, ref: sha, create: true });
    if (res && res.error) {
      toast("ブランチ作成に失敗: " + (res.error.message || res.error));
      return;
    }
    setNbAt(null);
    refresh();
    bumpRepos();
    bumpFiles();
  };

  // Switch to a branch (a commit that has a local branch ref). Lands ON the branch.
  const switchBranch = async (name: string) => {
    setMenu(null);
    const res = await apiJSON(`api/repos/${enc}/checkout`, "POST", { branch: name });
    if (res && res.error) {
      toast("ブランチ切替に失敗: " + (res.error.message || res.error));
      return;
    }
    refresh();
    bumpRepos();
    bumpFiles();
  };

  // Detached checkout of a commit that has NO branch (right-click → チェックアウト).
  const checkoutCommit = async (sha: string) => {
    setMenu(null);
    const ok = await askConfirm({
      title: "コミットをチェックアウト",
      body: `${sha.slice(0, 10)} をチェックアウトします（detached HEAD）。未コミットの変更があると失敗することがあります。`,
      confirmLabel: "チェックアウト",
      danger: false,
    });
    if (!ok) return;
    const res = await apiJSON(`api/repos/${enc}/checkout`, "POST", { branch: sha });
    if (res && res.error) {
      toast("checkout 失敗: " + (res.error.message || res.error));
      return;
    }
    refresh();
    bumpRepos();
    bumpFiles();
  };

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title"><Icon name="repo" /> {scmRepo}</span>
        <button className="branch" type="button" title="ブランチ切替" onClick={() => setShowBranch(true)}>
          <Icon name="git-branch" /> {status?.branch || current || "?"} <Icon name="chevron-down" />
        </button>
        {status && (status.ahead || status.behind) ? (
          <span
            className="repo-state-chip ab"
            title={`リモートに対して 先行 ${status.ahead ?? 0} / 遅延 ${status.behind ?? 0}`}
          >
            {status.ahead ? `↑${status.ahead}` : ""}
            {status.ahead && status.behind ? " " : ""}
            {status.behind ? `↓${status.behind}` : ""}
          </span>
        ) : null}
        <span className="spacer" />
        <button className="ghost scm-act" title="変更をコミット（別ペイン）" onClick={() => scmRepo && showChanges(scmRepo)}>
          <Icon name="git-commit" /> <span className="lbl">変更</span>
        </button>
        <button className="ghost scm-act" title="git fetch --prune" onClick={fetchRepo}>
          <Icon name="cloud-download" /> <span className="lbl">fetch</span>
        </button>
        <button className="ghost scm-act" title="現在のブランチを upstream に fast-forward（pull --ff-only）" onClick={doFF}>
          <Icon name="arrow-down" /> <span className="lbl">ff</span>
        </button>
        <button className="ghost scm-act" title="更新" onClick={refresh}>
          <Icon name="refresh" /> <span className="lbl">更新</span>
        </button>
      </header>
      <div className="cgraph-body">
        {loading && commits.length === 0 ? (
          <EmptyState icon="loading" message="読み込み中…" />
        ) : commits.length === 0 ? (
          <EmptyState icon="git-commit" message="コミットがありません" />
        ) : (
          <CommitGraph
            commits={commits}
            current={current}
            selectedSha={selected}
            onSelect={onSelect}
            onContext={(commit, x, y) => setMenu({ commit, x, y })}
          />
        )}
      </div>
      {menu && (
        <ul
          className="ctxmenu"
          ref={menuRef}
          style={{ left: menu.x, top: menu.y }}
          role="menu"
          onMouseDown={(e) => e.stopPropagation()}
        >
          <li onClick={() => { const s = menu.commit.sha; setMenu(null); showCommit(scmRepo || "", s); }}>
            <Icon name="git-commit" /> 詳細を表示
          </li>
          {/* A commit with local branch(es) → switch to the branch; otherwise a detached
              checkout of the commit itself. */}
          {menu.commit.refs.filter((rf) => rf.type === "head").length > 0 ? (
            menu.commit.refs
              .filter((rf) => rf.type === "head")
              .map((rf) => (
                <li key={rf.name} onClick={() => switchBranch(rf.name)}>
                  <Icon name="git-branch" /> ブランチ切替: {rf.name}
                </li>
              ))
          ) : (
            <li onClick={() => checkoutCommit(menu.commit.sha)}>
              <Icon name="git-branch" /> チェックアウト（detached HEAD）
            </li>
          )}
          <li onClick={() => { const s = menu.commit.sha; setMenu(null); setNbName(""); setNbAt(s); }}>
            <Icon name="git-branch" /> このコミットから新規ブランチ…
          </li>
        </ul>
      )}
      {nbAt && (
        <Modal
          title={`新規ブランチ — ${nbAt.slice(0, 10)} から`}
          onClose={() => setNbAt(null)}
          className="branch-modal"
          as="form"
          onSubmit={createBranchAt}
        >
          <div className="modal-body">
            <label className="pick-field">
              <span>ブランチ名</span>
              <input autoFocus value={nbName} onChange={(e) => setNbName(e.target.value)} placeholder="feature/…" />
            </label>
          </div>
          <footer className="modal-foot">
            <button type="button" className="ghost" onClick={() => setNbAt(null)}>
              キャンセル
            </button>
            <button type="submit" className="primary" disabled={!nbName.trim()}>
              作成して切替
            </button>
          </footer>
        </Modal>
      )}
      {showBranch && (
        <BranchModal
          repoName={scmRepo || ""}
          onClose={() => setShowBranch(false)}
          onChecked={() => {
            setShowBranch(false);
            refresh();
            bumpRepos();
          }}
        />
      )}
    </div>
  );
}
