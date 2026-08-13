// SourceControlView — the per-repo commit graph (codeleaf lane layout), opened
// by clicking a repo. Changes+commit box and per-commit detail live in their own
// panes. Port of views/SourceControlView onto the zustand stores.
import { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent, KeyboardEvent as RKeyboardEvent, ReactNode } from "react";
import { api, apiJSON, errText, isTransientErr } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { BranchModal } from "../repos/BranchModal.tsx";
import { wtFolder } from "../repos/BranchList.tsx";
import type { Branch } from "../repos/BranchList.tsx";
import { CommitGraph } from "./CommitGraph.tsx";
import { openCommit, openCommitSplit, openChanges, openRepoScm } from "./open.ts";
import { useReposStore, useLaunchTarget } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useLayoutStore } from "../../layout/store.ts";
import type { GraphCommit } from "../../lib/gitgraph.ts";

interface ScmStatus {
  branch?: string;
  ahead?: number;
  behind?: number;
}

interface SubmoduleInfo { name: string; path: string; initialized: boolean; sha?: string }

export function SourceControlView({ repo, path = "", headerActions }: { repo: string; path?: string; headerActions?: ReactNode }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
  const repos = useReposStore((s) => s.repos);
  const openLaunch = useLaunchTarget((s) => s.open);
  const bumpFiles = useFilesStore((s) => s.bump);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const enc = encodeURIComponent(repo || "");
  const [status, setStatus] = useState<ScmStatus | null>(null);
  const [commits, setCommits] = useState<GraphCommit[]>([]);
  const [current, setCurrent] = useState("");
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState("");
  const [showBranch, setShowBranch] = useState(false);
  const [menu, setMenu] = useState<{ commit: GraphCommit; x: number; y: number } | null>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const [nbAt, setNbAt] = useState<string | null>(null); // "new branch from this commit"
  const [nbName, setNbName] = useState("");
  const [moreOpen, setMoreOpen] = useState(false); // narrow-pane overflow (⋯)
  const [submodules, setSubmodules] = useState<SubmoduleInfo[]>([]);
  // branch -> the OTHER working copy holding it. git allows a branch in one worktree
  // at a time, so an occupied branch is not switchable here; the menu offers that copy
  // instead of an action git would refuse.
  const [occupied, setOccupied] = useState<Record<string, string>>({});
  const moreRef = useRef<HTMLDivElement>(null);
  useDismiss(moreRef, moreOpen, () => setMoreOpen(false));
  useMenuRoving(moreRef, moreOpen);

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

  // Load status + graph. Returns whether the load succeeded. A gateway/empty response
  // while the workspace agent is still booting (WS just started) is resolved by api() as
  // { error } — NOT a throw — so committing it would leave the pane on a "?" branch with
  // コミットがありません forever; the mount effect retries instead (keepLoadingOnFail keeps
  // the spinner up between attempts rather than flashing "no commits").
  const refresh = useCallback(async (keepLoadingOnFail = false) => {
    setLoading(true);
    let ok = true;
    try {
      const s = await api(`api/repos/${enc}/status${path ? `?path=${encodeURIComponent(path)}` : ""}`);
      if (isTransientErr(s)) ok = false;
      else setStatus(s);
    } catch {
      ok = false;
    }
    try {
      const d = await api(`api/repos/${enc}/graph?limit=300${path ? `&path=${encodeURIComponent(path)}` : ""}`);
      if (isTransientErr(d)) ok = false;
      else {
        setCommits(d.commits || []);
        setCurrent(d.current || "");
      }
    } catch {
      ok = false;
    }
    // Occupancy for the branch actions. Local-only git calls (for-each-ref + worktree
    // list), and never fatal to the view: on failure we simply offer no shortcut.
    // Submodule targets have no worktrees of their own, so skip the call entirely.
    if (!path) {
      try {
        const b = await api(`api/repos/${enc}/branches`);
        if (!isTransientErr(b)) {
          const m: Record<string, string> = {};
          for (const br of (b?.branches || []) as Branch[]) {
            const folder = wtFolder(br.worktree_path);
            if (folder) m[br.name] = folder;
          }
          setOccupied(m);
        }
      } catch {
        /* keep the last map — a missing shortcut is better than a broken view */
      }
    }
    if (ok || !keepLoadingOnFail) setLoading(false);
    return ok;
  }, [enc, path]);

  useEffect(() => {
    api(`api/repos/${enc}/submodules`)
      .then((d) => setSubmodules(d.submodules || []))
      .catch(() => setSubmodules([]));
  }, [enc]);

  useEffect(() => {
    setSelected("");
    let cancelled = false;
    let timer = 0;
    let tries = 0;
    const attempt = async () => {
      const ok = await refresh(true); // keep the spinner up while retrying
      if (cancelled || ok) return;
      const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
      tries++;
      timer = window.setTimeout(() => void attempt(), delay);
    };
    void attempt();
    const onVis = () => {
      if (!document.hidden && !cancelled) {
        tries = 0;
        window.clearTimeout(timer);
        void attempt();
      }
    };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [refresh]);

  const bodyRef = useRef<HTMLDivElement>(null);

  // Plain click / keyboard selects; Ctrl/⌘/middle-click opens the detail pane.
  const onSelect = (sha: string) => setSelected(sha);
  const onOpen = (sha: string, newPane: boolean) => {
    setSelected(sha);
    if (!repo) return;
    if (newPane) openCommitSplit(repo, sha, path || undefined);
    else openCommit(repo, sha, path || undefined);
  };

  const onKey = (e: RKeyboardEvent) => {
    if (!commits.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const idx = commits.findIndex((c) => c.sha === selected);
      const next = idx < 0 ? 0 : Math.min(commits.length - 1, Math.max(0, idx + (e.key === "ArrowDown" ? 1 : -1)));
      setSelected(commits[next].sha);
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (selected && repo) openCommit(repo, selected);
    }
  };

  useEffect(() => {
    if (selected) bodyRef.current?.querySelector(".cgraph-row.active")?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  const fetchRepo = async () => {
    await apiJSON(`api/repos/${enc}/fetch`, "POST", { prune: true });
    void refresh();
    void refreshRepos();
  };

  const doFF = async () => {
    const res = await apiJSON(`api/repos/${enc}/ff`, "POST", {});
    if (res && res.error) {
      toast(tr("scm.ff_failed", { err: res.error.message || res.error }));
      return;
    }
    void refresh();
    void refreshRepos();
  };

  const createBranchAt = async (e: FormEvent) => {
    e.preventDefault();
    const name = nbName.trim();
    const sha = nbAt;
    if (!name || !sha) return;
    const res = await apiJSON(`api/repos/${enc}/checkout`, "POST", { branch: name, ref: sha, create: true });
    if (res && res.error) {
      toast(tr("scm.branch_create_failed", { err: errText(res.error) }));
      return;
    }
    setNbAt(null);
    void refresh();
    void refreshRepos();
    bumpFiles();
  };

  // 「このブランチで作業を始める」: a worktree is always spun off the BASE clone (one
  // can't hang off another worktree), so from a worktree view the launch targets its
  // parent. Undefined until the repo list has loaded — the menu item hides until then.
  const me = repos.find((x) => x.name === repo);
  const launchBase = me?.worktree ? repos.find((x) => x.name === me.parent) : me;
  const startOnBranch = (name: string) => {
    setMenu(null);
    setShowBranch(false);
    if (launchBase) openLaunch(launchBase, name);
  };

  const switchBranch = async (name: string) => {
    setMenu(null);
    const res = await apiJSON(`api/repos/${enc}/checkout`, "POST", { branch: name });
    if (res && res.error) {
      toast(tr("scm.branch_switch_failed", { err: errText(res.error) }));
      return;
    }
    void refresh();
    void refreshRepos();
    bumpFiles();
  };

  const checkoutCommit = async (sha: string) => {
    setMenu(null);
    const ok = await askConfirm({
      title: tr("scm.checkout_commit_title"),
      body: tr("scm.checkout_commit_confirm", { sha: sha.slice(0, 10) }),
      confirmLabel: tr("scm.checkout"),
      danger: false,
    });
    if (!ok) return;
    const res = await apiJSON(`api/repos/${enc}/checkout`, "POST", { branch: sha });
    if (res && res.error) {
      toast(tr("scm.checkout_failed", { err: errText(res.error) }));
      return;
    }
    void refresh();
    void refreshRepos();
    bumpFiles();
  };

  // One action set, rendered inline when wide and as a ⋯ menu when narrow.
  const scmActions = [
    { key: "changes", icon: "git-commit", label: tr("scm.changes"), title: tr("scm.changes_title"), onClick: () => repo && openChanges(repo) },
    { key: "fetch", icon: "cloud-download", label: "fetch", title: "git fetch --prune", onClick: () => void fetchRepo() },
    { key: "ff", icon: "arrow-down", label: "Fast-Forward", title: tr("scm.ff_title"), onClick: () => void doFF() },
    { key: "refresh", icon: "refresh", label: tr("scm.refresh"), title: tr("scm.refresh"), onClick: () => void refresh() },
  ].filter((a) => !path || a.key === "refresh");

  return (
    <div className="scmview">
      {/* .scm-acts / .scm-more own their own spacing and the collapse-to-⋯
          behaviour, so they stay inline after the spacer rather than moving into
          the actions slot. */}
      <ViewHead actions={headerActions}>
        <span className="view-title">
          <Icon name={path ? "repo-forked" : "repo"} /> {repo}{path ? ` / ${path}` : ""}
        </span>
        {submodules.length > 0 && (
          <select
            className="scm-target"
            aria-label={tr("scm.target_aria")}
            title={tr("scm.target_title")}
            value={path}
            onChange={(e) => openTarget({ content: { kind: "scm", scmRepo: repo, scmPath: e.target.value || undefined } })}
          >
            <option value="">{tr("scm.parent_repo")}</option>
            {submodules.map((sm) => (
              <option key={sm.path} value={sm.path} disabled={!sm.initialized}>
                submodule: {sm.path}{sm.initialized ? "" : tr("scm.not_fetched")}
              </option>
            ))}
          </select>
        )}
        <button className="scm-branch" type="button" title={path ? tr("scm.submodule_branch_ro") : tr("scm.switch_branch")} onClick={() => !path && setShowBranch(true)}>
          <Icon name="git-branch" /> {status?.branch || current || "?"} {!path && <Icon name="chevron-down" />}
        </button>
        {status && (status.ahead || status.behind) ? (
          <span className="repo-chip ab" title={tr("scm.ahead_behind", { ahead: status.ahead ?? 0, behind: status.behind ?? 0 })}>
            {status.ahead ? `↑${status.ahead}` : ""}
            {status.ahead && status.behind ? " " : ""}
            {status.behind ? `↓${status.behind}` : ""}
          </span>
        ) : null}
        <span className="view-spacer" />
        <div className="scm-acts">
          {scmActions.map((a) => (
            <button key={a.key} type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title={a.title} onClick={a.onClick}>
              <Icon name={a.icon} /> <span className="lbl">{a.label}</span>
            </button>
          ))}
        </div>
        <div className="scm-more" ref={moreRef}>
          <button
            type="button"
            className="ui-btn ui-btn-ghost ui-iconbtn"
            title={tr("scm.actions")}
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((o) => !o)}
          >
            <Icon name="ellipsis" />
          </button>
          {moreOpen && (
            <div className="ui-menu scm-more-menu">
              {scmActions.map((a) => (
                <button
                  key={a.key}
                  type="button"
                  className="ui-menu-item"
                  onClick={() => {
                    setMoreOpen(false);
                    a.onClick();
                  }}
                >
                  <Icon name={a.icon} /> {a.label}
                </button>
              ))}
            </div>
          )}
        </div>
      </ViewHead>
      <div className="cgraph-body" ref={bodyRef} tabIndex={0} onKeyDown={onKey}>
        {loading && commits.length === 0 ? (
          <EmptyState icon="loading" title={tr("scm.loading")} />
        ) : commits.length === 0 ? (
          <EmptyState icon="git-commit" title={tr("scm.no_commits")} />
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
        <ul className="ui-menu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
          <li>
            <button
              type="button"
              className="ui-menu-item"
              onClick={() => {
                const s = menu.commit.sha;
                setMenu(null);
                openCommit(repo, s, path || undefined);
              }}
            >
              <Icon name="git-commit" /> {tr("scm.show_detail")}
            </button>
          </li>
          {!path && (menu.commit.refs.filter((rf) => rf.type === "head").length > 0 ? (
            menu.commit.refs
              .filter((rf) => rf.type === "head")
              .map((rf) => {
                const holder = occupied[rf.name];
                // 「作業を始める」creates an isolated worktree; 「切り替え」moves THIS
                // copy's HEAD. In a worktree (1 copy = 1 task = 1 branch) the isolated
                // option leads; in the base clone, switching is still the primary act.
                const items = [
                  launchBase && !holder ? (
                    <li key="start">
                      <button type="button" className="ui-menu-item" onClick={() => startOnBranch(rf.name)}>
                        <Icon name="play" /> {tr("scm.start_work_on", { name: rf.name })}
                      </button>
                    </li>
                  ) : null,
                  holder ? (
                    // git refuses a second checkout of a live branch, so offer the copy
                    // that has it rather than an action that can only fail.
                    <li key="open">
                      <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); openRepoScm(holder); }}>
                        <Icon name="repo-forked" /> {tr("scm.open_branch_worktree", { name: rf.name, folder: holder })}
                      </button>
                    </li>
                  ) : (
                    <li key="switch">
                      <button type="button" className="ui-menu-item" onClick={() => void switchBranch(rf.name)}>
                        <Icon name="git-branch" /> {tr("scm.switch_branch_to", { name: rf.name })}
                      </button>
                    </li>
                  ),
                ];
                return <Fragment key={rf.name}>{me?.worktree ? items : items.reverse()}</Fragment>;
              })
          ) : (
            <li>
              <button type="button" className="ui-menu-item" onClick={() => void checkoutCommit(menu.commit.sha)}>
                <Icon name="git-branch" /> {tr("scm.checkout_detached")}
              </button>
            </li>
          ))}
          {!path && <li>
            <button
              type="button"
              className="ui-menu-item"
              onClick={() => {
                const s = menu.commit.sha;
                setMenu(null);
                setNbName("");
                setNbAt(s);
              }}
            >
              <Icon name="git-branch" /> {tr("scm.new_branch_from_commit")}
            </button>
          </li>}
        </ul>
      )}
      {nbAt && (
        <Modal title={tr("scm.new_branch_title", { sha: nbAt.slice(0, 10) })} onClose={() => setNbAt(null)} as="form" onSubmit={createBranchAt}>
          <div className="ui-modal-body">
            <label className="ui-field">
              <span className="ui-field-label">{tr("scm.branch_name")}</span>
              <input autoFocus value={nbName} onChange={(e) => setNbName(e.target.value)} placeholder="feature/…" />
            </label>
          </div>
          <footer className="ui-modal-foot">
            <Button variant="ghost" onClick={() => setNbAt(null)}>
              {tr("scm.cancel")}
            </Button>
            <Button variant="primary" type="submit" disabled={!nbName.trim()}>
              {tr("scm.create_and_switch")}
            </Button>
          </footer>
        </Modal>
      )}
      {showBranch && !path && (
        <BranchModal
          repoName={repo}
          onClose={() => setShowBranch(false)}
          onOpenWorktree={(folder) => {
            setShowBranch(false);
            openRepoScm(folder);
          }}
          // In a worktree, switching HEAD in place fights what the worktree is for —
          // offer a dedicated working copy per branch and let switching be the fallback.
          onStartWork={me?.worktree && launchBase ? startOnBranch : undefined}
          onChecked={() => {
            setShowBranch(false);
            void refresh();
            void refreshRepos();
            bumpFiles();
          }}
        />
      )}
    </div>
  );
}
