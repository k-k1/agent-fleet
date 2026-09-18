// RepoRowConnected — RepoRow with every handler wired from the stores, so a
// container just renders <RepoRowConnected r={r} ctx={ctx} /> wherever a working
// copy appears (the flat Repos list, each node of the project tree). All the launch
// / clone-target / delete / fast-forward / open-SCM logic that used to live inline
// in ReposSection lives here once.
import { useMemo, useState } from "react";
import { apiJSON, errDetail, errText, repoSetLock } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { resolveEffort, writeRepoLast, resolveModel, resolveStartMode } from "../../lib/repoLast.ts";
import { agentLaunchDefault, useSettings } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useReposStore } from "./store.ts";
import type { Repo } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal, openSessionTerminalSplit, openSessionChat, openSessionChatSplit } from "../sessions/open.ts";
import { RepoRow } from "./RepoRow.tsx";
import { useStartWork } from "./useStartWork.ts";
import { SvnAuthModal } from "./SvnAuthModal.tsx";
import { DeleteCopyModal } from "./DeleteCopyModal.tsx";
import { StopSessionsModal } from "./StopSessionsModal.tsx";
import { liveSessionCount } from "./stopTree.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import type { RepoRailContext } from "./useRepoRail.ts";

interface RepoRowConnectedProps {
  r: Repo;
  ctx: RepoRailContext;
  /** This row's node in the rail's tree. The delete modal lists the copies nested under it,
   * so a row rendered outside the tree (no node) simply offers the single-copy delete. */
  node?: RepoTreeNode;
  /** Plain card click toggles the owning node's fold (SCM is on the right-click menu). */
  onToggle?: () => void;
  /** Session tally badge (see RepoRow.sess) — computed by the owning node. */
  sess?: { alive: number; total: number };
  /** Bulk-archive stopped sessions (right-click menu). The owning node (RepoNode) passes a
   * count and a handler scoped to the sessions directly under this folder. */
  onArchiveStopped?: () => void;
  stoppedCount?: number;
}

export function RepoRowConnected({ r, ctx, node, onToggle, sess, onArchiveStopped, stoppedCount }: RepoRowConnectedProps) {
  const settings = useSettings(); // default model for a claude launch
  const tr = useT();
  const toast = useToast();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setActive = useLayoutStore((s) => s.setActive);
  const refreshRepos = useReposStore((s) => s.refresh);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const startWork = useStartWork();
  // SVN re-authentication (docs/log/41 amendment). Owned here rather than in RepoRow
  // because both routes into it are here: the menu item, and an update that came back
  // svn_auth_required — the failure IS the moment to ask, and answering it retries.
  const [authOpen, setAuthOpen] = useState(false);
  const [delOpen, setDelOpen] = useState(false);
  const [stopOpen, setStopOpen] = useState(false);
  // The subtree both plan dialogs work on. A row rendered outside the tree (the flat Repos
  // list) has no subtree; a leaf of one is the same shape. Memoized because each dialog
  // freezes its plan on this object's identity.
  const treeNode: RepoTreeNode = useMemo(() => node || { repo: r, children: [], spine: "" }, [node, r]);
  // The bulk stop's scope is that whole subtree, so the count that decides whether the menu
  // item appears is counted here rather than passed per folder like stoppedCount: a folded
  // spawn shows one row and holds four running sessions.
  const sessions = useSessionsStore((s) => s.sessions);
  const aliveCount = liveSessionCount(treeNode, sessions);

  const svnUpdate = async () => {
    const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/svn-update`, "POST", {});
    if (res && res.error) {
      const code = typeof res.error === "object" ? (res.error as { code?: string }).code : "";
      if (code === "svn_auth_required") {
        // No credential, or one the server rejected: offer the fix instead of a toast
        // that only restates the failure.
        setAuthOpen(true);
        return;
      }
      toast(tr("rp.svn_update_failed", { err: errText(res.error) }));
      return;
    }
    void refreshRepos();
    toast(tr("rp.svn_update_success", { name: r.name, rev: res?.revision || "?" }), { kind: "success" });
  };

  return (
    <>
    <RepoRow
      r={r}
      kinds={ctx.launchKinds}
      running={ctx.running}
      active={ctx.scmRepo === r.name}
      selected={r.name === ctx.activeRepo}
      sess={sess}
      onArchiveStopped={onArchiveStopped}
      stoppedCount={stoppedCount}
      // Bulk stop: the whole subtree's live sessions, planned per row in the modal.
      onStopSessions={() => setStopOpen(true)}
      aliveCount={aliveCount}
      opens={ctx.rPanes?.get(r.name)}
      onFocusPane={setActive}
      onToggle={onToggle}
      // One click opens Source Control; Ctrl/Cmd/middle-click → a freshly split pane.
      onOpen={(e) => {
        const target = { content: { kind: "scm", scmRepo: r.name } as const };
        if (e && (e.ctrlKey || e.metaKey || e.button === 1)) openTargetInNew(target);
        else openTarget(target);
      }}
      // Right-click -> open folder: expand + select the repo in the Files tree.
      onOpenFolder={() => useFilesStore.getState().revealInFiles("repos/" + r.name, { focus: true })}
      onOpenChanges={() => openTarget({ content: { kind: "changes", scmRepo: r.name } })}
      onFF={async () => {
        const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/ff`, "POST", {});
        if (res && res.error) {
          toast(tr("rp.ff_failed", { err: errText(res.error) }));
          return;
        }
        void refreshRepos();
        toast(tr("rp.ff_success", { name: r.name }), { kind: "success" });
      }}
      onParentFF={r.worktree && r.integration?.relation === "contained" ? async () => {
        const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/parent-ff`, "POST", {});
        if (res && res.error) {
          toast(tr("rp.parent_ff_failed", { err: errText(res.error) }));
          return;
        }
        void refreshRepos();
        toast(tr("rp.parent_ff_success", { name: r.name }), { kind: "success" });
      } : undefined}
      // SVN (docs/log/41): update to the latest revision (auto-heals a wedged lock server-side).
      onUpdate={r.vcs === "svn" ? () => void svnUpdate() : undefined}
      // SVN: explicitly clear a wedged working-copy lock (local; no auth needed).
      onCleanup={r.vcs === "svn" ? async () => {
        const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/svn-cleanup`, "POST", {});
        if (res && res.error) {
          toast(tr("rp.svn_cleanup_failed", { err: errText(res.error) }));
          return;
        }
        void refreshRepos();
        toast(tr("rp.svn_cleanup_success", { name: r.name }), { kind: "success" });
      } : undefined}
      onReauth={r.vcs === "svn" ? () => setAuthOpen(true) : undefined}
      // Deletion lock (docs/log/45): pin/unpin a working copy (worktrees included) against deletion.
      onToggleLock={async (locked) => {
        const res = await repoSetLock(r.name, locked);
        if (res?.error) {
          toast(tr("repo.lock_failed", { err: errText(res.error) }));
          return;
        }
        // The POST has already saved the value. Update the open row now;
        // refreshRepos below remains reconciliation only.
        useReposStore.getState().setLocked(r.name, res?.locked ?? locked);
        void refreshRepos();
        toast(locked ? tr("repo.locked_on", { name: r.name }) : tr("repo.locked_off", { name: r.name }), { kind: "success" });
      }}
      // Delete opens the plan (DeleteCopyModal) rather than a confirm: the copies the rail
      // nests under this one are almost always the rest of the same job, and the dirty /
      // force question is asked per row there instead of as a second dialog.
      onDelete={() => setDelOpen(true)}
      // Quick launch (▼ / right-click): no prompt, straight to a session.
      onLaunch={async (kind, split) => {
        const hasModel = agentOf(kind).caps.model;
        const defaults = agentLaunchDefault(settings, kind);
        // Shared per-kind chain: repo last-used → kind default (repoLast.ts resolveModel).
        const model = hasModel ? resolveModel(kind, r.name, defaults.model) : "";
        const effort = agentOf(kind).caps.effort ? resolveEffort(kind, r.name, defaults.effort) : "";
        // Kinds that can start in plan mode (planMode or tuiStartMode) honour the saved
        // default, so the per-repo start mode picked in the launch modal also applies to a
        // quick launch.
        const startMode =
          agentOf(kind).caps.planMode || agentOf(kind).caps.tuiStartMode
            ? resolveStartMode(kind, r.name, defaults.startMode)
            : "normal";
        const body: Record<string, unknown> = { dir: r.path, kind };
        // A quick launch defaults to managed like any new session (docs/log/27 §9.2 —
        // opencode). The CLI is reachable through the driver choice in the start-work modal.
        if (agentOf(kind).managedDriver) body.driver = "managed";
        if (model) body.model = model;
        if (effort) body.effort = effort;
        body.mode = startMode;
        let res = await apiJSON("api/sessions", "POST", body);
        // An old Agent (the P1.5 generation) rejects managed outright; retry with tui.
        if (res?.error && body.driver === "managed" && (res.error as { code?: string }).code === "driver_unsupported") {
          delete body.driver;
          if (!agentOf(kind).caps.tuiEffort) delete body.effort;
          if (!agentOf(kind).caps.tuiStartMode) delete body.mode;
          res = await apiJSON("api/sessions", "POST", body);
        }
        if (res && res.error) {
          // errDetail: a generic code (runtime_failed and friends) carries the why in message only.
          toast(tr("rp.launch_failed", { err: errDetail(res.error) }));
          return;
        }
        writeRepoLast(r.name, kind, hasModel ? model : undefined, effort, startMode);
        void refreshSessions();
        const chat = agentOf(kind).caps.chat;
        (chat
          ? split ? openSessionChatSplit : openSessionChat
          : split ? openSessionTerminalSplit : openSessionTerminal)(res.name);
      }}
      // Start work: worktree (default) or in-place, with an optional first prompt the
      // Agent delivers once the CLI is ready. Shared with the Start hub (useStartWork).
      onStartWork={(opts) => startWork({ dir: r.path || "", repo: r.name }, opts)}
      onBranchChanged={() => {
        // A checkout / new branch changed HEAD and the working tree.
        void refreshRepos();
        useFilesStore.getState().bump();
      }}
    />
    {stopOpen && (
      <StopSessionsModal
        node={treeNode}
        onClose={() => setStopOpen(false)}
        // Two counts rather than one line: "stopped" and "will stop when its turn ends" are
        // different promises, and a toast that merged them would overstate the first.
        onDone={(now, after) => {
          const parts = [
            ...(now > 0 ? [tr("rp.stop.done_now", { count: now })] : []),
            ...(after > 0 ? [tr("rp.stop.done_after", { count: after })] : []),
          ];
          toast(parts.join(tr("common.list_sep")), { kind: "success" });
        }}
      />
    )}
    {delOpen && (
      <DeleteCopyModal
        node={treeNode}
        onClose={() => setDelOpen(false)}
        // Not r.name: a run can end with the children gone and this row held back (a live
        // session, a lock), and a toast naming this copy would then be a lie.
        onDeleted={(count) => toast(tr("rp.del.done", { count }), { kind: "success" })}
      />
    )}
    {authOpen && (
      <SvnAuthModal
        repo={r.name}
        onClose={() => setAuthOpen(false)}
        // Saved means proven: run the update that failed, so the dialog ends in the
        // outcome the user asked for rather than in "now try again".
        onSaved={() => void svnUpdate()}
      />
    )}
    </>
  );
}
