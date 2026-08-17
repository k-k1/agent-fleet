// RepoRowConnected — RepoRow with every handler wired from the stores, so a
// container just renders <RepoRowConnected r={r} ctx={ctx} /> wherever a working
// copy appears (the flat Repos list, each node of the project tree). All the launch
// / clone-target / delete / fast-forward / open-SCM logic that used to live inline
// in ReposSection lives here once.
import { apiJSON, raw, errText, repoSetLock } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
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
import type { RepoRailContext } from "./useRepoRail.ts";

interface RepoRowConnectedProps {
  r: Repo;
  ctx: RepoRailContext;
  /** Plain card click toggles the owning node's fold (SCM is on the right-click menu). */
  onToggle?: () => void;
  /** Session tally badge (see RepoRow.sess) — computed by the owning node. */
  sess?: { alive: number; total: number };
  /** 停止中セッション一括アーカイブ（右クリックメニュー）— このフォルダ直下の
   * セッションだけを対象にした件数/ハンドラを、所有ノード（RepoNode）が渡す。 */
  onArchiveStopped?: () => void;
  stoppedCount?: number;
}

export function RepoRowConnected({ r, ctx, onToggle, sess, onArchiveStopped, stoppedCount }: RepoRowConnectedProps) {
  const settings = useSettings(); // default model for claude 起動
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setActive = useLayoutStore((s) => s.setActive);
  const refreshRepos = useReposStore((s) => s.refresh);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const startWork = useStartWork();

  return (
    <RepoRow
      r={r}
      kinds={ctx.launchKinds}
      running={ctx.running}
      active={ctx.scmRepo === r.name}
      selected={r.name === ctx.activeRepo}
      sess={sess}
      onArchiveStopped={onArchiveStopped}
      stoppedCount={stoppedCount}
      opens={ctx.rPanes?.get(r.name)}
      onFocusPane={setActive}
      onToggle={onToggle}
      // One click opens Source Control; Ctrl/Cmd/middle-click → a freshly split pane.
      onOpen={(e) => {
        const target = { content: { kind: "scm", scmRepo: r.name } as const };
        if (e && (e.ctrlKey || e.metaKey || e.button === 1)) openTargetInNew(target);
        else openTarget(target);
      }}
      // Right-click → フォルダを開く: expand + select the repo in the Files tree.
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
      // SVN (docs/41): update to the latest revision (auto-heals a wedged lock server-side).
      onUpdate={r.vcs === "svn" ? async () => {
        const res = await apiJSON(`api/repos/${encodeURIComponent(r.name)}/svn-update`, "POST", {});
        if (res && res.error) {
          toast(tr("rp.svn_update_failed", { err: errText(res.error) }));
          return;
        }
        void refreshRepos();
        toast(tr("rp.svn_update_success", { name: r.name, rev: res?.revision || "?" }), { kind: "success" });
      } : undefined}
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
      // 削除ロック（docs/45）: 作業コピー（worktree 含む）を削除保護に固定/解除する。
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
      onDelete={async () => {
        const ok = await askConfirm({
          title: tr("rp.delete_workcopy_title"),
          body: tr("rp.delete_workcopy_body", { name: r.name }),
          confirmLabel: tr("rp.delete_confirm"),
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
              title: tr("rp.unsaved_changes_title"),
              body: tr("rp.unsaved_changes_body", { name: r.name }),
              confirmLabel: tr("rp.force_delete"),
              danger: true,
            });
            if (!force) return;
            res = await del(true);
          }
        }
        if (!res.ok) {
          const j = await res.json().catch(() => null);
          toast(j?.error ? tr("rp.delete_failed", { err: errText(j.error) }) : tr("rp.delete_failed_generic"));
          return;
        }
        void refreshRepos();
        useFilesStore.getState().bump();
        toast(tr("rp.delete_success", { name: r.name }), { kind: "success", persist: true });
      }}
      // Quick launch (▼ / right-click): no prompt, straight to a session.
      onLaunch={async (kind, split) => {
        const hasModel = agentOf(kind).caps.model;
        const defaults = agentLaunchDefault(settings, kind);
        // Shared per-kind chain: repo last-used → kind default (repoLast.ts resolveModel).
        const model = hasModel ? resolveModel(kind, r.name, defaults.model) : "";
        const effort = agentOf(kind).caps.effort ? resolveEffort(kind, r.name, defaults.effort) : "";
        // plan 起動対応（planMode または tuiStartMode）の kind は保存済み既定を尊重する
        // — 起動モーダルで選んだ per-repo の開始モードがクイック起動でも効くように。
        const startMode =
          agentOf(kind).caps.planMode || agentOf(kind).caps.tuiStartMode
            ? resolveStartMode(kind, r.name, defaults.startMode)
            : "normal";
        const body: Record<string, unknown> = { dir: r.path, kind };
        // クイック起動も新規の既定は managed（docs/27 §9.2 — opencode）。CLI が
        // 欲しいときは 作業を始める モーダルのドライバ選択から。
        if (agentOf(kind).managedDriver) body.driver = "managed";
        if (model) body.model = model;
        if (effort) body.effort = effort;
        body.mode = startMode;
        let res = await apiJSON("api/sessions", "POST", body);
        // 旧 Agent（P1.5 世代）は managed を明示拒否する — tui で立て直す。
        if (res?.error && body.driver === "managed" && (res.error as { code?: string }).code === "driver_unsupported") {
          delete body.driver;
          if (!agentOf(kind).caps.tuiEffort) delete body.effort;
          if (!agentOf(kind).caps.tuiStartMode) delete body.mode;
          res = await apiJSON("api/sessions", "POST", body);
        }
        if (res && res.error) {
          toast(tr("rp.launch_failed", { err: errText(res.error) }));
          return;
        }
        writeRepoLast(r.name, kind, hasModel ? model : undefined, effort, startMode);
        void refreshSessions();
        const chat = agentOf(kind).caps.chat;
        (chat
          ? split ? openSessionChatSplit : openSessionChat
          : split ? openSessionTerminalSplit : openSessionTerminal)(res.name);
      }}
      // 作業を始める: worktree (default) or in-place, with an optional first prompt
      // auto-sent once the session is alive. Shared with the はじめる hub (useStartWork).
      onStartWork={(opts) => startWork({ dir: r.path || "", repo: r.name }, opts)}
      onBranchChanged={() => {
        // A checkout / new branch changed HEAD and the working tree.
        void refreshRepos();
        useFilesStore.getState().bump();
      }}
    />
  );
}
