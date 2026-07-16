// SourceControlView — the per-repo commit graph (codeleaf lane layout), opened
// by clicking a repo. Changes+commit box and per-commit detail live in their own
// panes. Port of views/SourceControlView onto the zustand stores.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent, KeyboardEvent as RKeyboardEvent } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
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
import { CommitGraph } from "./CommitGraph.tsx";
import { openCommit, openCommitSplit, openChanges } from "./open.ts";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useLayoutStore } from "../../layout/store.ts";
import type { GraphCommit } from "../../lib/gitgraph.ts";

interface ScmStatus {
  branch?: string;
  ahead?: number;
  behind?: number;
}

interface SubmoduleInfo { name: string; path: string; initialized: boolean; sha?: string }

export function SourceControlView({ repo, path = "" }: { repo: string; path?: string }) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
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

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setStatus(await api(`api/repos/${enc}/status${path ? `?path=${encodeURIComponent(path)}` : ""}`));
    } catch {}
    try {
      const d = await api(`api/repos/${enc}/graph?limit=300${path ? `&path=${encodeURIComponent(path)}` : ""}`);
      setCommits(d.commits || []);
      setCurrent(d.current || "");
    } catch {
      setCommits([]);
    } finally {
      setLoading(false);
    }
  }, [enc, path]);

  useEffect(() => {
    api(`api/repos/${enc}/submodules`)
      .then((d) => setSubmodules(d.submodules || []))
      .catch(() => setSubmodules([]));
  }, [enc]);

  useEffect(() => {
    setSelected("");
    void refresh();
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
      <header className="view-head">
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
      </header>
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
              .map((rf) => (
                <li key={rf.name}>
                  <button type="button" className="ui-menu-item" onClick={() => void switchBranch(rf.name)}>
                    <Icon name="git-branch" /> {tr("scm.switch_branch_to", { name: rf.name })}
                  </button>
                </li>
              ))
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
