// ReposSection — working copies under ~/repos: clone (provider picker / URL),
// branch switch, fast-forward, launch a session (作業を始める modal / quick ▼ /
// right-click), delete, open Source Control. Ported from the old console onto
// the zustand stores.
//
// Interim notes: SCM/変更 panes render as P3 placeholders; reveal-in-Files is
// wired in P2d; launch prompts auto-send via sendPromptWhenAlive until the chat
// mirror lands (P5).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent } from "react";
import { api, apiJSON, raw, errText } from "../../core/api/client.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { writeRepoLast } from "../../lib/repoLast.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { useSettings } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { repoPanes, ordClass, paneCount } from "../../layout/badges.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal, openSessionTerminalSplit, sendPromptWhenAlive } from "../sessions/open.ts";
import { useReposStore } from "./store.ts";
import type { Repo } from "./store.ts";
import { NewRepoModal } from "./NewRepoModal.tsx";
import { BranchModal } from "./BranchModal.tsx";
import { LaunchModal } from "./LaunchModal.tsx";
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import type { ConnectionsStatus } from "../../types/session.ts";

// Provider display: known SaaS hosts get a friendly label; unknown slugs show as-is.
const PROVIDER_LABEL: Record<string, string> = {
  github: "GitHub",
  bitbucket: "Bitbucket",
  gitlab: "GitLab",
};
const providerLabel = (p: string) => PROVIDER_LABEL[p] || p;
const providerIcon = (p: string): string | null => (p === "github" ? "github" : null);

// guessRepoName derives a display name from a clone URL for the in-progress
// spinner row, before the server reports the real name.
const guessRepoName = (u: string | null | undefined) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

export function ReposSection() {
  const repos = useReposStore((s) => s.repos);
  const refreshRepos = useReposStore((s) => s.refresh);
  const sessions = useSessionsStore((s) => s.sessions);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const layout = useLayoutStore((s) => s.layout);
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setActive = useLayoutStore((s) => s.setActive);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const settings = useSettings(); // default model for claude 起動
  const toast = useToast();
  const askConfirm = useConfirm();

  // repo name → panes showing it (ordinal badges); only when split.
  const multiPane = paneCount(layout) > 1;
  const rPanes = multiPane ? repoPanes(layout) : null;

  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null);

  useEffect(() => {
    void refreshRepos();
  }, [refreshRepos]);

  // Agent connection state gates the 起動 menu. Unknown (null) → show all.
  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? d : null))
      .catch(() => alive && setConns(null));
    return () => {
      alive = false;
    };
  }, []);
  const ready = (k: string) => !conns || agentOf(k).available({ conns });
  const launchKinds = repoLaunchKinds.filter(ready);

  // The attached session's repo → highlighted row. Derived from the shared
  // sessions list (no extra fetch, unlike the old per-change refetch).
  const activeSessionName = activePane(layout)?.session ?? null;
  const activeSession = sessions.find((s) => s.name === activeSessionName);
  const activeRepo = activeSession && agentOf(activeSession.kind).caps.runsInDir ? activeSession.repo : null;

  // The SCM pane target → active row (kind scm/changes/commit on the active pane).
  const ac = activePane(layout)?.content;
  const scmRepo = ac && (ac.kind === "scm" || ac.kind === "changes" || ac.kind === "commit") ? ac.scmRepo : null;

  // Run a clone in the background: the modal closed already, so progress shows
  // as a spinner row here until the server finishes.
  const doClone = async ({ remote_url, branch, name, new_branch }: { remote_url: string; branch: string; name: string; new_branch?: string }) => {
    setCloning({ name: name || guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch, name, new_branch: new_branch || "" });
      if (res && res.error) {
        toast("clone に失敗: " + (res.error.message || res.error));
        return;
      }
      void refreshRepos();
      // TODO(P2d): reveal the new repo in the Files tree once FilesSection lands.
    } catch (e) {
      toast("clone に失敗: " + e);
    } finally {
      setCloning(null);
    }
  };

  return (
    <Section
      id="repos"
      title="Repos"
      icon="repo"
      count={repos.length}
      actions={
        <>
          <Button
            small
            variant="ghost"
            icon="add"
            title={running ? "clone" : "clone（ワークスペース停止中）"}
            disabled={!!cloning || !running}
            onClick={() => setShowClone((s) => !s)}
          >
            クローン
          </Button>
          <Button small variant="ghost" icon="refresh" title="更新" onClick={() => void refreshRepos()}>
            更新
          </Button>
        </>
      }
    >
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="sess-list">
        {cloning && (
          <li className="repo-cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {repos.length === 0 && !cloning && (
          <EmptyState icon="repo" title="リポジトリがありません" hint="clone するとここに並びます">
            {running && (
              <Button small variant="primary" icon="add" onClick={() => setShowClone(true)}>
                クローン
              </Button>
            )}
          </EmptyState>
        )}
        {repos.map((r) => (
          <RepoRow
            key={r.name}
            r={r}
            kinds={launchKinds}
            running={running}
            active={scmRepo === r.name}
            opens={rPanes?.get(r.name)}
            onFocusPane={setActive}
            selected={r.name === activeRepo}
            // One click opens the Source Control workbench (P3 placeholder for
            // now); Ctrl/Cmd/middle-click → a freshly split pane.
            onOpen={(e) => {
              const target = { content: { kind: "scm", scmRepo: r.name } as const };
              if (e && (e.ctrlKey || e.metaKey || e.button === 1)) openTargetInNew(target);
              else openTarget(target);
            }}
            onOpenChanges={() => openTarget({ content: { kind: "changes", scmRepo: r.name } })}
            onFF={async () => {
              const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/ff`, "POST", {});
              if (res && res.error) {
                toast("ff 失敗: " + errText(res.error));
                return;
              }
              void refreshRepos();
              toast(`${r.name}: fast-forward しました`, { kind: "success" });
            }}
            onDelete={async () => {
              const ok = await askConfirm({
                title: "ワーキングコピーを削除",
                body: `"${r.name}" のローカル作業コピーを削除します。履歴・リモートはそのまま残ります。`,
                confirmLabel: "削除する",
                danger: true,
              });
              if (!ok) return;
              const del = (force: boolean) =>
                raw(`api/repos/${encodeURIComponent(r.name)}${force ? "?force=true" : ""}`, { method: "DELETE" });
              let res = await del(false);
              // A dirty worktree is refused (worktree_dirty) — re-confirm, then force.
              if (!res.ok) {
                const j = await res.json().catch(() => null);
                const code = j?.error && typeof j.error === "object" ? j.error.code : "";
                if (code === "worktree_dirty") {
                  const force = await askConfirm({
                    title: "未保存の変更があります",
                    body: `"${r.name}" には未コミット/未pushの変更があります。強制的に削除すると失われます。続けますか？`,
                    confirmLabel: "強制削除",
                    danger: true,
                  });
                  if (!force) return;
                  res = await del(true);
                }
              }
              if (!res.ok) {
                const j = await res.json().catch(() => null);
                toast(j?.error ? "削除に失敗: " + errText(j.error) : "削除に失敗しました");
                return;
              }
              void refreshRepos();
              toast(`${r.name} を削除しました`, { kind: "success" });
            }}
            // Quick launch (▼ / right-click): no prompt, straight to a session.
            onLaunch={async (kind: string, split: boolean) => {
              const hasModel = agentOf(kind).caps.model;
              const model = hasModel ? settings.defaultModel || "" : "";
              const body: Record<string, unknown> = { dir: r.path, kind };
              if (model) body.model = model;
              const res = await apiJSON("api/sessions", "POST", body);
              if (res && res.error) {
                toast("起動に失敗: " + errText(res.error));
                return;
              }
              writeRepoLast(r.name, kind, hasModel ? model : undefined);
              void refreshSessions();
              (split ? openSessionTerminalSplit : openSessionTerminal)(res.name); // TODO(P5): chat mirror
            }}
            // 作業を始める: worktree (default) or in-place, with an optional first
            // prompt auto-sent once the session is alive.
            onStartWork={async ({ kind, model, prompt, worktree, base, newBranch, folder, useExisting }) => {
              const hasModel = agentOf(kind).caps.model;
              const body: Record<string, unknown> = { dir: r.path, kind };
              if (hasModel && model) body.model = model;
              if (worktree) {
                body.worktree = true;
                body.branch = base;
                body.new_branch = newBranch;
                if (folder) body.folder = folder;
                if (useExisting) body.use_existing = true;
              }
              const res = await apiJSON("api/sessions", "POST", body);
              if (res && res.error) {
                const code = typeof res.error === "object" ? res.error.code : "";
                if (code === "branch_exists") return { ok: false, conflict: "local" as const };
                if (code === "branch_exists_remote") return { ok: false, conflict: "remote" as const };
                toast((worktree ? "worktree 起動に失敗: " : "起動に失敗: ") + errText(res.error));
                return { ok: false };
              }
              writeRepoLast(r.name, kind, hasModel ? model : undefined);
              if (prompt) {
                sendPromptWhenAlive(res.name, prompt); // TODO(P5): launchSeed + mirror auto-send
                pushPromptHistory(r.name, prompt);
              }
              if (worktree) void refreshRepos();
              void refreshSessions();
              openSessionTerminal(res.name); // TODO(P5): chat mirror
              return { ok: true };
            }}
            onBranchChanged={() => {
              void refreshRepos();
              // TODO(P2d): refresh the Files tree too.
            }}
          />
        ))}
      </ul>
    </Section>
  );
}

// RepoRow: click the card to open the repo's Source Control; 起動 to spawn a
// session. `active` = open in the SCM pane; `selected` = the attached session's
// repo — both highlight in place (no reordering).
interface RepoRowProps {
  r: Repo;
  kinds?: string[];
  running?: boolean;
  active?: boolean;
  selected?: boolean;
  onOpen: (e?: RMouseEvent) => void;
  onOpenChanges?: () => void;
  onFF?: () => void;
  onDelete?: () => void;
  onLaunch: (kind: string, split: boolean) => void;
  onStartWork: (opts: LaunchOpts) => Promise<LaunchResult>;
  onBranchChanged?: () => void;
  opens?: { ordinal: number; id: string }[];
  onFocusPane?: (id: string) => void;
}

function RepoRow({ r, kinds = repoLaunchKinds, running = true, active, selected, onOpen, onOpenChanges, onFF, onDelete, onLaunch, onStartWork, onBranchChanged, opens, onFocusPane }: RepoRowProps) {
  const [showLaunch, setShowLaunch] = useState(false);
  const [launchModal, setLaunchModal] = useState(false);
  // Agent kinds only — shell/ssm have no model/prompt, so the modal excludes
  // them; they keep the ▼ quick path.
  const agentKinds = kinds.filter((k) => agentOf(k).caps.chat);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [branchOpen, setBranchOpen] = useState(false);
  const menuRef = useRef<HTMLUListElement>(null);

  // Context menu: open at the cursor, clamp within the rail, close on outside
  // click / Esc / window blur.
  useLayoutEffect(() => {
    if (menu && menuRef.current)
      placeFixed(menuRef.current, menu.x, menu.y, menuRef.current.closest<HTMLElement>(".app-rail"));
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

  // Close the launch dropdown on outside click (containment check, so opening
  // another menu closes this one).
  useDismiss(wrapRef, showLaunch, () => setShowLaunch(false));

  return (
    <li
      className={"repo-row" + (active || selected ? " active" : "")}
      onContextMenu={(e) => {
        if (!running) return;
        e.preventDefault();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <div
        className={"repo-card" + (running ? "" : " disabled")}
        title={running ? "ソース管理を開く: " + r.path + "（Ctrl/中クリックで新ペイン）" : "ワークスペース停止中"}
        onClick={(e) => running && onOpen(e)}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && running && onOpen(e)}
      >
        <div className="repo-info">
          <span className="repo-name">
            <Icon name="repo" />
            {r.name}
          </span>
          {(r.dirty || ((r.ahead || r.behind) ?? 0) > 0) && (
            <span className="repo-state">
              {r.dirty && (
                <span className="repo-chip dirty" title="未コミット変更あり">
                  <Icon name="circle-filled" /> 未コミット
                </span>
              )}
              {((r.ahead || r.behind) ?? 0) > 0 && (
                <span className="repo-chip ab" title={`リモートに対して 先行 ${r.ahead ?? 0} / 遅延 ${r.behind ?? 0}`}>
                  {r.ahead ? `↑${r.ahead}` : ""}
                  {r.ahead && r.behind ? " " : ""}
                  {r.behind ? `↓${r.behind}` : ""}
                </span>
              )}
            </span>
          )}
          {opens && opens.length > 0 && (
            <span className="sess-ords repo-ords">
              {opens.map((o) => (
                <button
                  key={o.id}
                  type="button"
                  className={"rail-ord " + ordClass(o.ordinal)}
                  title={`ペイン${o.ordinal}にフォーカス`}
                  onClick={(e) => {
                    e.stopPropagation();
                    onFocusPane?.(o.id);
                  }}
                >
                  {o.ordinal}
                </button>
              ))}
            </span>
          )}
          <div className="launch-wrap" ref={wrapRef} onClick={(e) => e.stopPropagation()}>
            {/* Split button: 起動 opens the modal; the ▼ caret opens the quick
                per-kind dropdown (instant launch, no prompt). */}
            <div className="launch-split">
              <button
                type="button"
                className="launch-main"
                title={
                  running
                    ? agentKinds.length
                      ? "作業を始める（既定は隔離 worktree・エージェント/モデル/最初の指示）"
                      : "利用可能なエージェントがありません"
                    : "ワークスペース停止中"
                }
                disabled={!running || !agentKinds.length}
                onClick={() => setLaunchModal(true)}
              >
                <Icon name="play" /> 起動
              </button>
              <button
                type="button"
                className="launch-caret"
                title={running ? "種別を選んで即起動（プロンプト無し）" : "ワークスペース停止中"}
                disabled={!running}
                onClick={() => setShowLaunch((v) => !v)}
              >
                <Icon name="chevron-down" />
              </button>
            </div>
            {showLaunch && (
              <div className="ui-menu launch-menu">
                {kinds.map((k) => (
                  <button
                    key={k}
                    type="button"
                    className="ui-menu-item"
                    title="Ctrl/中クリックで新ペインに起動"
                    onClick={(e) => {
                      setShowLaunch(false);
                      onLaunch(k, e.ctrlKey || e.metaKey);
                    }}
                    onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                    onAuxClick={(e) => {
                      if (e.button === 1) {
                        e.preventDefault();
                        setShowLaunch(false);
                        onLaunch(k, true);
                      }
                    }}
                  >
                    <Icon name={kindIcon(k)} /> {kindLabel(k)}
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
        {/* Meta line: current branch (left) + git provider (right). */}
        {(r.branch || r.provider || r.worktree) && (
          <div className="repo-meta">
            {r.branch && (
              <span
                className={"repo-branch" + (r.worktree ? " worktree" : "")}
                title={
                  r.worktree
                    ? `worktree — ブランチ: ${r.branch}${r.parent ? "（親: " + r.parent + "）" : ""}（名前変更はセッションのメニューから）`
                    : "現在のブランチ: " + r.branch
                }
              >
                <Icon name={r.worktree ? "repo-forked" : "git-branch"} />
                <span className="repo-branch-name">{r.branch}</span>
              </span>
            )}
            {r.provider && (
              <span className="repo-provider" title={"リモート: " + (r.remote || r.provider)}>
                {providerIcon(r.provider) && <Icon name={providerIcon(r.provider) as string} />}
                {providerLabel(r.provider)}
              </span>
            )}
          </div>
        )}
      </div>

      {menu && (
        <ul className="ui-menu repo-ctxmenu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
          <li>
            <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpen(); }}>
              <Icon name="source-control" /> ソース管理を開く
            </button>
          </li>
          {onOpenChanges && (
            <li>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpenChanges(); }}>
                <Icon name="git-commit" /> 変更をコミット
              </button>
            </li>
          )}
          <li>
            <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); setBranchOpen(true); }}>
              <Icon name="git-branch" /> ブランチ切替
            </button>
          </li>
          {onFF && (
            <li>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onFF(); }}>
                <Icon name="arrow-down" /> Fast-Forward
              </button>
            </li>
          )}
          <li className="ui-menu-sep" role="separator" />
          {kinds.map((k) => (
            <li key={k}>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onLaunch(k, false); }}>
                <Icon name={kindIcon(k)} /> {kindLabel(k)} を起動
              </button>
            </li>
          ))}
          {onDelete && (
            <>
              <li className="ui-menu-sep" role="separator" />
              <li>
                <button type="button" className="ui-menu-item danger" onClick={() => { setMenu(null); onDelete(); }}>
                  <Icon name="trash" /> ワーキングコピーを削除
                </button>
              </li>
            </>
          )}
        </ul>
      )}
      {branchOpen && (
        <BranchModal
          repoName={r.name}
          onClose={() => setBranchOpen(false)}
          onChecked={() => {
            setBranchOpen(false);
            onBranchChanged?.();
          }}
        />
      )}
      {launchModal && (
        <LaunchModal
          repo={r.name}
          branch={r.branch}
          path={r.path}
          kinds={agentKinds}
          onClose={() => setLaunchModal(false)}
          onLaunch={onStartWork}
        />
      )}
    </li>
  );
}
