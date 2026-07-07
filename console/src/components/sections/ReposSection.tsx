import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useApp } from "../../state.jsx";
import { api, apiJSON, raw, errText } from "../../api.js";
import Section from "../Section.jsx";
import Icon from "../Icon.jsx";
import { useToast } from "../ToastProvider.jsx";
import { useConfirm } from "../ConfirmProvider.jsx";
import EmptyState from "../EmptyState.jsx";
import { useDismiss } from "../../lib/useDismiss.js";
import { placeFixed } from "../../lib/placeFixed.js";
import NewRepoModal from "../NewRepoModal.jsx";
import BranchModal from "../BranchModal.jsx";
import LaunchModal from "../LaunchModal.jsx";
import type { LaunchOpts } from "../LaunchModal.jsx";
import { kindIcon, kindLabel } from "../../lib/sessionkind.js";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { setLaunchSeed } from "../../lib/launchSeed.js";
import { writeRepoLast } from "../../lib/repoLast.js";
import { pushPromptHistory } from "../../lib/promptHistory.js";
import { useSettings } from "../../lib/settings.js";
import { repoPanes, ordClass, paneCount } from "../../lib/panebadge.js";
import type { MouseEvent as RMouseEvent } from "react";
import type { ConnectionsStatus, Session } from "../../types/session.ts";

// A working copy under ~/repos, from GET /api/repos.
interface Repo {
  name: string;
  path?: string;
  branch?: string;
  dirty?: boolean;
  ahead?: number;
  behind?: number;
  provider?: string; // origin host slug: github/bitbucket/gitlab, or a bare host
  remote?: string; // origin host (tooltip)
  worktree?: boolean; // linked git worktree (not a standalone clone)
  parent?: string; // for a worktree, the parent working copy's folder name
}

// Provider display: known SaaS hosts get a friendly label (+ icon where a codicon
// exists); an unknown host slug shows as-is so self-hosted remotes still identify.
const PROVIDER_LABEL: Record<string, string> = {
  github: "GitHub",
  bitbucket: "Bitbucket",
  gitlab: "GitLab",
};
const providerLabel = (p: string) => PROVIDER_LABEL[p] || p;
const providerIcon = (p: string): string | null => (p === "github" ? "github" : null);

// guessRepoName derives a display name from a clone URL (last path segment minus
// .git) for the in-progress spinner row, before the server reports the real name.
const guessRepoName = (u: string | null | undefined) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

// Repos: working copies under ~/repos. Clone new ones (provider picker or URL),
// switch branch, fetch, start a session in the repo, delete, or open Source
// Control (→ main area). The dirty dot mirrors uncommitted changes.
export default function ReposSection() {
  const { reposKey, connKey, bumpRepos, bumpSessions, bumpFiles, revealInFiles, showSCM, showSCMSplit, showChanges, showTerminal, showTerminalSplit, showChat, showChatSplit, scmRepo, mode, session, wsState, layout, setActivePane } =
    useApp();
  const settings = useSettings(); // default model for claude 起動
  // repo name → panes showing it (ordinal badges), like the Sessions list; only when split.
  const multiPane = paneCount(layout) > 1;
  const rPanes = multiPane ? repoPanes(layout) : null;
  const toast = useToast();
  const askConfirm = useConfirm();
  const running = wsState === "running"; // WS down → clone/open/launch are inert
  const [repos, setRepos] = useState<Repo[]>([]);
  const [conns, setConns] = useState<ConnectionsStatus | null>(null); // null = unknown (loading/failed) → show all
  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null); // while a clone runs (left-pane spinner)
  const [activeRepo, setActiveRepo] = useState<string | null>(null); // repo used by the attached session

  // Run a clone in the background: the modal closed already, so progress shows as a
  // spinner row here until the server finishes, then the repo appears + is revealed.
  const doClone = async ({ remote_url, branch, name, new_branch }: { remote_url: string; branch: string; name: string; new_branch?: string }) => {
    setCloning({ name: name || guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch, name, new_branch: new_branch || "" });
      if (res && res.error) {
        toast("clone に失敗: " + (res.error.message || res.error));
        return;
      }
      bumpRepos();
      if (res && res.name) revealInFiles("repos/" + res.name);
      else bumpFiles();
    } catch (e) {
      toast("clone に失敗: " + e);
    } finally {
      setCloning(null);
    }
  };

  useEffect(() => {
    let alive = true;
    api("api/repos")
      .then((d) => alive && setRepos(d.repos || []))
      .catch(() => alive && setRepos([]));
    return () => {
      alive = false;
    };
  }, [reposKey]);

  // Agent connection state, so the 起動 menu only offers agents the user has set up.
  // Refetched when a connect/disconnect bumps connKey. Only a clean response sets it;
  // on error/unknown we leave it null and show every kind (don't hide on a blip).
  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? d : null))
      .catch(() => alive && setConns(null));
    return () => {
      alive = false;
    };
  }, [connKey]);

  // shell is always available; an agent kind only appears once its connection is set
  // up (Claude/Codex signed in, opencode has at least one API key env). While conns
  // is unknown (null) we show all so a slow/failed probe never hides a valid option.
  // Availability lives on each agent descriptor (src/agents/registry).
  const ready = (k: string) => !conns || agentOf(k).available({ conns });
  const launchKinds = repoLaunchKinds.filter(ready);

  // Resolve which repo the currently-attached session works in, so we can show it
  // selected (highlighted) in place. Refetched when the attached session changes.
  useEffect(() => {
    if (!session) {
      setActiveRepo(null);
      return;
    }
    let alive = true;
    api("api/sessions")
      .then((d) => {
        if (!alive) return;
        const s = (d.sessions || []).find((x: Session) => x.name === session);
        // A shell session doesn't "own" a repo for highlighting purposes — only
        // agent sessions (claude/opencode/codex, i.e. those that run in a dir)
        // mark their working repo.
        setActiveRepo(s && agentOf(s.kind).caps.runsInDir ? s.repo : null);
      })
      .catch(() => alive && setActiveRepo(null));
    return () => {
      alive = false;
    };
  }, [session]);

  return (
    <Section
      id="repos"
      title="Repos"
      icon="repo"
      count={repos.length}
      actions={
        <>
          <button
            className="ghost lblbtn"
            title={running ? "clone" : "clone（ワークスペース停止中）"}
            disabled={!!cloning || !running}
            onClick={() => setShowClone((s) => !s)}
          >
            <Icon name="add" />
            <span className="lbl">クローン</span>
          </button>
          <button className="ghost lblbtn" title="更新" onClick={bumpRepos}>
            <Icon name="refresh" />
            <span className="lbl">更新</span>
          </button>
        </>
      }
    >
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="list">
        {cloning && (
          <li className="repo-row cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {repos.length === 0 && !cloning && (
          <EmptyState
            icon="repo"
            message="リポジトリがありません"
            hint="clone するとここに並びます"
            action={running ? { label: "クローン", icon: "add", onClick: () => setShowClone(true) } : undefined}
          />
        )}
        {repos.map((r) => (
          <RepoRow
            key={r.name}
            r={r}
            kinds={launchKinds}
            running={running}
            active={(mode === "scm" || mode === "changes" || mode === "commit") && scmRepo === r.name}
            opens={rPanes?.get(r.name)}
            onFocusPane={setActivePane}
            selected={r.name === activeRepo}
            // One click on the repo opens the Source Control workbench in the main
            // pane (Ctrl/Cmd/middle-click → freshly split pane). Revealing the folder
            // in the Files tree is a separate, opt-in right-click action (onOpenFolder).
            onOpen={(e) => {
              if (e && (e.ctrlKey || e.metaKey || e.button === 1)) showSCMSplit(r.name);
              else showSCM(r.name);
            }}
            // Right-click → フォルダを開く: expand + select the repo in the Files tree.
            onOpenFolder={() => revealInFiles("repos/" + r.name)}
            // Right-click → 変更をコミット: open the changes/commit pane for this repo.
            onOpenChanges={() => showChanges(r.name)}
            // Right-click → fast-forward: advance the current branch to its upstream.
            onFF={async () => {
              const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/ff`, "POST", {});
              if (res && res.error) {
                toast("ff 失敗: " + errText(res.error));
                return;
              }
              bumpRepos();
              toast(`${r.name}: fast-forward しました`);
            }}
            // Right-click → ワーキングコピーを削除: repo lifecycle op (moved here from the
            // 変更 view). Reversible — history / remote stay; re-clone recreates it. Always
            // confirmed since it's destructive to local work.
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
              // A worktree with uncommitted/unpushed work is refused (worktree_dirty);
              // re-confirm the loss, then retry with force so it's an explicit choice.
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
              bumpRepos();
              bumpFiles();
              toast(`${r.name} を削除しました`);
            }}
            // split=true (middle-click) opens the new session in a freshly split
            // pane instead of replacing the active pane's content.
            onLaunch={async (kind: string, split: boolean) => {
              // The server allocates the session's identity (slug); we only say where
              // (dir), what kind, and (for a model-taking kind = claude) the user's
              // default model so a repo launch follows the setting like the dialog does.
              // Open the created session by its returned slug — claude → chat mirror
              // (live, PTY attached in bg), other kinds → terminal.
              const hasModel = agentOf(kind).caps.model;
              const model = hasModel ? settings.defaultModel || "" : "";
              const body: Record<string, unknown> = { dir: r.path, kind };
              if (model) body.model = model;
              const res = await apiJSON("api/sessions", "POST", body);
              if (res && res.error) {
                toast("起動に失敗: " + errText(res.error));
                return;
              }
              // Remember what was launched here so the 起動 modal defaults to it next
              // time (model only for a model-taking kind, else leave the stored one).
              writeRepoLast(r.name, kind, hasModel ? model : undefined);
              bumpSessions();
              const chat = agentOf(kind).caps.chat;
              (chat
                ? split ? showChatSplit : showChat
                : split ? showTerminalSplit : showTerminal)(res.name);
            }}
            // 作業を始める: unified launch. worktree (default) spins an isolated worktree
            // of this repo and starts a session there; in-place starts in the current
            // checkout. The provisional branch is derived from the prompt (server falls
            // back to wip-<slug>); the typed prompt is stashed as a launch seed and
            // auto-sent once MirrorView sees the session alive.
            onStartWork={async ({ kind, model, prompt, worktree, base, newBranch }) => {
              const hasModel = agentOf(kind).caps.model;
              const body: Record<string, unknown> = { dir: r.path, kind };
              if (hasModel && model) body.model = model;
              if (worktree) {
                body.worktree = true;
                body.branch = base;
                body.new_branch = newBranch;
              }
              const res = await apiJSON("api/sessions", "POST", body);
              if (res && res.error) {
                toast((worktree ? "worktree 起動に失敗: " : "起動に失敗: ") + errText(res.error));
                return;
              }
              writeRepoLast(r.name, kind, hasModel ? model : undefined);
              if (prompt) {
                setLaunchSeed(res.name, prompt);
                pushPromptHistory(r.name, prompt); // remember it for the 履歴 group next time
              }
              if (worktree) {
                bumpRepos();
                bumpFiles();
              }
              bumpSessions();
              const chat = agentOf(kind).caps.chat;
              (chat ? showChat : showTerminal)(res.name);
            }}
            // A checkout / new branch changed HEAD and the working tree — refresh the
            // repo row (branch label) and the Files tree.
            onBranchChanged={() => {
              bumpRepos();
              bumpFiles();
            }}
          />
        ))}
      </ul>
    </Section>
  );
}

// RepoRow: click the name to open the repo (Files + Source Control in the right
// pane), or 起動 to spawn a session in it. Branch switch / fetch / delete used to
// live here too — they now live in the Source Control header (the right pane), so
// the row stays compact: name + 起動 (where the branch chip used to sit). `active`
// = open in the SCM pane; `selected` = the attached session's repo — both just
// highlight the row in place (no reordering).
interface RepoRowProps {
  r: Repo;
  kinds?: string[];
  running?: boolean;
  active?: boolean;
  selected?: boolean;
  onOpen: (e?: RMouseEvent) => void;
  onOpenFolder?: () => void;
  onOpenChanges?: () => void;
  onFF?: () => void;
  onDelete?: () => void;
  onLaunch: (kind: string, split: boolean) => void;
  onStartWork: (opts: LaunchOpts) => void;
  onBranchChanged?: () => void;
  opens?: { ordinal: number; id: string }[];
  onFocusPane?: (id: string) => void;
}

function RepoRow({ r, kinds = repoLaunchKinds, running = true, active, selected, onOpen, onOpenFolder, onOpenChanges, onFF, onDelete, onLaunch, onStartWork, onBranchChanged, opens, onFocusPane }: RepoRowProps) {
  const [showLaunch, setShowLaunch] = useState(false);
  const [launchModal, setLaunchModal] = useState(false); // 起動 modal (agent + model + prompt)
  // Agent kinds only (chat-capable: claude/codex/opencode) — shell/ssm have no model
  // and no "first prompt", so the modal excludes them; they keep the ▼ quick path.
  const agentKinds = kinds.filter((k) => agentOf(k).caps.chat);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null); // right-click context menu
  const [branchOpen, setBranchOpen] = useState(false); // branch switch / create modal
  const menuRef = useRef<HTMLUListElement>(null);

  // Context menu: open at the cursor, clamp on-screen, close on outside click / Esc.
  useLayoutEffect(() => {
    if (menu && menuRef.current)
      // Clamp within the left rail's scroll container so the menu stops short of its
      // vertical scrollbar instead of painting over it (the menu is far narrower than
      // the viewport, so a viewport-only clamp never kicks in).
      placeFixed(menuRef.current, menu.x, menu.y, menuRef.current.closest<HTMLElement>(".leftpane-scroll"));
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

  // Close the launch dropdown on any outside click. Use a containment check
  // (not stopPropagation on the wrap): stopPropagation would swallow OTHER
  // dropdowns' document-level close listeners, so opening a session ⋯ menu
  // wouldn't close this one — both would stay open, both .pane-section would
  // lift to z-index:10, and the later REPOS section would paint over the
  // SESSIONS menu, blocking its 再開する item.
  useDismiss(wrapRef, showLaunch, () => setShowLaunch(false));

  return (
    <li
      className={"repo-row" + (active || selected ? " active" : "")}
      onContextMenu={(e) => {
        if (!running) return; // WS down → actions are inert; don't offer the menu
        e.preventDefault();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <div
        className={"repo-card" + (running ? "" : " disabled")}
        title={running ? "ソース管理を開く: " + r.path + "（Ctrl/中クリックで新ペイン）" : "ワークスペース停止中"}
        // The whole card opens Source Control (plain → active pane, Ctrl/middle → split);
        // the 起動 dropdown stops propagation so it stays independent.
        onClick={(e) => running && onOpen(e)}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && running && onOpen(e)}
      >
        <div className="repo-info">
          <span className="grow repo-name">
            <Icon name="repo" className="repo-ic" />
            {r.name}
          </span>
        {(r.dirty || ((r.ahead || r.behind) ?? 0) > 0) && (
          <span className="repo-state">
            {r.dirty && (
              <span className="repo-state-chip dirty" title="未コミット変更あり">
                <Icon name="circle-filled" /> 未コミット
              </span>
            )}
            {((r.ahead || r.behind) ?? 0) > 0 && (
              <span
                className="repo-state-chip ab"
                title={`リモートに対して 先行 ${r.ahead ?? 0} / 遅延 ${r.behind ?? 0}`}
              >
                {r.ahead ? `↑${r.ahead}` : ""}
                {r.ahead && r.behind ? " " : ""}
                {r.behind ? `↓${r.behind}` : ""}
              </span>
            )}
          </span>
        )}
        {opens && opens.length > 0 && (
          <span className="session-ords repo-ords">
            {opens.map((o) => (
              <button
                key={o.id}
                type="button"
                className={"session-ord " + ordClass(o.ordinal)}
                title={`ペイン${o.ordinal}にフォーカス`}
                onClick={(e) => { e.stopPropagation(); onFocusPane?.(o.id); }}
              >
                {o.ordinal}
              </button>
            ))}
          </span>
        )}
        <div className="launch-wrap" ref={wrapRef} onClick={(e) => e.stopPropagation()}>
          {/* Split button: 起動 opens the modal (agent + model + first prompt); the
              ▼ caret opens the quick per-kind dropdown (instant launch, no prompt).
              The main button needs an agent kind to be useful; when none is available
              (only shell), it's disabled and the caret still offers shell. */}
          <div className="launch-split">
            <button
              className="chip launch launch-main"
              title={running ? (agentKinds.length ? "作業を始める（既定は隔離 worktree・エージェント/モデル/最初の指示）" : "利用可能なエージェントがありません") : "ワークスペース停止中"}
              disabled={!running || !agentKinds.length}
              onClick={() => setLaunchModal(true)}
            >
              <Icon name="play" /> 起動
            </button>
            <button
              className="chip launch launch-caret"
              title={running ? "種別を選んで即起動（プロンプト無し）" : "ワークスペース停止中"}
              disabled={!running}
              onClick={() => setShowLaunch((v) => !v)}
            >
              <Icon name="chevron-down" />
            </button>
          </div>
          {showLaunch && (
            <div className="launch-menu">
              {kinds.map((k) => (
                <button
                  key={k}
                  title="Ctrl/中クリックで新ペインに起動"
                  // Ctrl/Cmd+click mirrors the middle-click: launch into a split pane.
                  onClick={(e) => { setShowLaunch(false); onLaunch(k, e.ctrlKey || e.metaKey); }}
                  // Middle-click launches into a freshly split pane. Suppress the
                  // mousedown default so the browser doesn't start autoscroll.
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
      {/* Meta line under the name: current branch (left) + git provider (right). */}
      {(r.branch || r.provider || r.worktree) && (
        <div className="repo-meta">
          {r.branch && (
            <span className="repo-branch" title={"現在のブランチ: " + r.branch}>
              <Icon name="git-branch" className="repo-branch-ic" />
              <span className="repo-branch-name">{r.branch}</span>
            </span>
          )}
          {r.worktree && (
            <span
              className="repo-worktree"
              title={r.parent ? `worktree（親: ${r.parent}）` : "worktree"}
            >
              <Icon name="repo-forked" className="repo-worktree-ic" /> worktree
            </span>
          )}
          {r.provider && (
            <span
              className={"repo-provider prov-" + r.provider}
              title={"リモート: " + (r.remote || r.provider)}
            >
              {providerIcon(r.provider) && (
                <Icon name={providerIcon(r.provider) as string} className="repo-provider-ic" />
              )}
              {providerLabel(r.provider)}
            </span>
          )}
        </div>
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
          <li onClick={() => { setMenu(null); onOpen(); }}>
            <Icon name="source-control" /> ソース管理を開く
          </li>
          {onOpenFolder && (
            <li onClick={() => { setMenu(null); onOpenFolder(); }}>
              <Icon name="folder-opened" /> フォルダを開く
            </li>
          )}
          {onOpenChanges && (
            <li onClick={() => { setMenu(null); onOpenChanges(); }}>
              <Icon name="git-commit" /> 変更をコミット
            </li>
          )}
          <li onClick={() => { setMenu(null); setBranchOpen(true); }}>
            <Icon name="git-branch" /> ブランチ切替
          </li>
          {onFF && (
            <li onClick={() => { setMenu(null); onFF(); }}>
              <Icon name="arrow-down" /> Fast-Forward
            </li>
          )}
          <li className="ctx-sep" role="separator" />
          {kinds.map((k) => (
            <li key={k} onClick={() => { setMenu(null); onLaunch(k, false); }}>
              <Icon name={kindIcon(k)} /> {kindLabel(k)} を起動
            </li>
          ))}
          {onDelete && (
            <>
              <li className="ctx-sep" role="separator" />
              <li className="danger" onClick={() => { setMenu(null); onDelete(); }}>
                <Icon name="trash" /> ワーキングコピーを削除
              </li>
            </>
          )}
        </ul>
      )}
      {branchOpen && (
        <BranchModal
          repoName={r.name}
          onClose={() => setBranchOpen(false)}
          onChecked={() => { setBranchOpen(false); onBranchChanged?.(); }}
        />
      )}
      {launchModal && (
        <LaunchModal
          repo={r.name}
          branch={r.branch}
          path={r.path}
          kinds={agentKinds}
          onClose={() => setLaunchModal(false)}
          onLaunch={(opts) => { setLaunchModal(false); onStartWork(opts); }}
        />
      )}
    </li>
  );
}
