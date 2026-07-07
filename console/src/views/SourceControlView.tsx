import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useApp } from "../state.jsx";
import { api, apiJSON, errText } from "../api.js";
import Icon from "../components/Icon.jsx";
import BranchModal from "../components/BranchModal.jsx";
import Modal from "../components/Modal.jsx";
import CommitGraph from "../components/CommitGraph.jsx";
import { useConfirm } from "../components/ConfirmProvider.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import { useDismiss } from "../lib/useDismiss.js";
import { placeFixed } from "../lib/placeFixed.js";
import EmptyState from "../components/EmptyState.jsx";
import type { FormEvent, KeyboardEvent as RKeyboardEvent } from "react";
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
  const { scmRepo: ctxRepo, bumpRepos, bumpFiles, showChanges, showCommit, showCommitSplit } = useApp();
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
  const [moreOpen, setMoreOpen] = useState(false); // narrow-pane overflow (⋯) menu
  const moreRef = useRef<HTMLDivElement>(null);
  useDismiss(moreRef, moreOpen, () => setMoreOpen(false));

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

  const bodyRef = useRef<HTMLDivElement>(null);

  // Plain click / keyboard just selects (highlights) the commit — no pane opens.
  const onSelect = (sha: string) => setSelected(sha);
  // Ctrl/⌘/middle-click opens the detail: newPane → a fresh pane, else the (reused) one.
  const onOpen = (sha: string, newPane: boolean) => {
    setSelected(sha);
    if (!scmRepo) return;
    if (newPane) showCommitSplit(scmRepo, sha);
    else showCommit(scmRepo, sha);
  };

  // ↑/↓ move the selection through commits; Enter opens the selected commit's detail.
  const onKey = (e: RKeyboardEvent) => {
    if (!commits.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const idx = commits.findIndex((c) => c.sha === selected);
      const next =
        idx < 0 ? 0 : Math.min(commits.length - 1, Math.max(0, idx + (e.key === "ArrowDown" ? 1 : -1)));
      setSelected(commits[next].sha);
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (selected && scmRepo) showCommit(scmRepo, selected);
    }
  };

  // Keep the selected row in view during keyboard navigation.
  useEffect(() => {
    if (selected) bodyRef.current?.querySelector(".cgraph-row.active")?.scrollIntoView({ block: "nearest" });
  }, [selected]);

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
      toast("ブランチ作成に失敗: " + errText(res.error));
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
      toast("ブランチ切替に失敗: " + errText(res.error));
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
      toast("checkout 失敗: " + errText(res.error));
      return;
    }
    refresh();
    bumpRepos();
    bumpFiles();
  };

  // Header actions, shared by the inline row and the ⋯ overflow menu.
  const scmActions = [
    { key: "changes", icon: "git-commit", label: "変更", title: "変更をコミット（別ペイン）", onClick: () => scmRepo && showChanges(scmRepo) },
    { key: "fetch", icon: "cloud-download", label: "fetch", title: "git fetch --prune", onClick: fetchRepo },
    { key: "ff", icon: "arrow-down", label: "Fast-Forward", title: "現在のブランチを upstream に fast-forward（pull --ff-only）", onClick: doFF },
    { key: "refresh", icon: "refresh", label: "更新", title: "更新", onClick: refresh },
  ];

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
        {/* One action set, rendered two ways: an inline icon row when the pane is wide
            enough, and a ⋯ overflow menu when it's narrow (CSS container query toggles
            which is shown — see .scm-acts / .scm-more in styles.css). */}
        <div className="scm-acts">
          {scmActions.map((a) => (
            <button key={a.key} className="ghost scm-act" title={a.title} onClick={a.onClick}>
              <Icon name={a.icon} /> <span className="lbl">{a.label}</span>
            </button>
          ))}
        </div>
        <div className="launch-wrap scm-more" ref={moreRef}>
          <button
            className="ghost scm-more-btn"
            title="操作"
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((o) => !o)}
          >
            <Icon name="ellipsis" />
          </button>
          {moreOpen && (
            <div className="launch-menu">
              {scmActions.map((a) => (
                <button key={a.key} onClick={() => { setMoreOpen(false); a.onClick(); }}>
                  <Icon name={a.icon} /> {a.label}
                </button>
              ))}
            </div>
          )}
        </div>
      </header>
      <div className="cgraph-body" ref={bodyRef} tabIndex={0} onKeyDown={onKey}>
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
            onOpen={onOpen}
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
