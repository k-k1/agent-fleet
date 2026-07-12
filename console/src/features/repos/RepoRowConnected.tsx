// RepoRowConnected — RepoRow with every handler wired from the stores, so a
// container just renders <RepoRowConnected r={r} ctx={ctx} /> wherever a working
// copy appears (the flat Repos list, each node of the project tree). All the launch
// / clone-target / delete / fast-forward / open-SCM logic that used to live inline
// in ReposSection lives here once.
import { apiJSON, raw, errText, pasteImage } from "../../core/api/client.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { writeRepoLast, resolveModel } from "../../lib/repoLast.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { useSettings } from "../../lib/settings.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useReposStore } from "./store.ts";
import type { Repo } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionTerminal, openSessionTerminalSplit, openSessionChat, openSessionChatSplit, sendPromptWhenAlive } from "../sessions/open.ts";
import { RepoRow } from "./RepoRow.tsx";
import type { RepoRailContext } from "./useRepoRail.ts";

interface RepoRowConnectedProps {
  r: Repo;
  ctx: RepoRailContext;
  /** Plain card click toggles the owning node's fold (SCM is on the right-click menu). */
  onToggle?: () => void;
  /** Session tally badge (see RepoRow.sess) — computed by the owning node. */
  sess?: { alive: number; total: number };
}

export function RepoRowConnected({ r, ctx, onToggle, sess }: RepoRowConnectedProps) {
  const settings = useSettings(); // default model for claude 起動
  const toast = useToast();
  const askConfirm = useConfirm();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setActive = useLayoutStore((s) => s.setActive);
  const refreshRepos = useReposStore((s) => s.refresh);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  return (
    <RepoRow
      r={r}
      kinds={ctx.launchKinds}
      running={ctx.running}
      active={ctx.scmRepo === r.name}
      selected={r.name === ctx.activeRepo}
      sess={sess}
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
      onOpenFolder={() => useFilesStore.getState().revealInFiles("repos/" + r.name)}
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
        useFilesStore.getState().bump();
        toast(`${r.name} を削除しました`, { kind: "success" });
      }}
      // Quick launch (▼ / right-click): no prompt, straight to a session.
      onLaunch={async (kind, split) => {
        const hasModel = agentOf(kind).caps.model;
        // Shared per-kind chain: repo last-used → kind default (repoLast.ts resolveModel).
        const model = hasModel ? resolveModel(kind, r.name, settings.defaultModel) : "";
        const body: Record<string, unknown> = { dir: r.path, kind };
        if (model) body.model = model;
        const res = await apiJSON("api/sessions", "POST", body);
        if (res && res.error) {
          toast("起動に失敗: " + errText(res.error));
          return;
        }
        writeRepoLast(r.name, kind, hasModel ? model : undefined);
        void refreshSessions();
        const chat = agentOf(kind).caps.chat;
        (chat
          ? split ? openSessionChatSplit : openSessionChat
          : split ? openSessionTerminalSplit : openSessionTerminal)(res.name);
      }}
      // 作業を始める: worktree (default) or in-place, with an optional first prompt
      // auto-sent once the session is alive.
      onStartWork={async ({ kind, model, prompt, images, worktree, base, newBranch, useExisting }) => {
        const hasModel = agentOf(kind).caps.model;
        const body: Record<string, unknown> = { dir: r.path, kind };
        if (hasModel && model) body.model = model;
        if (worktree) {
          body.worktree = true;
          body.branch = base;
          body.new_branch = newBranch;
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
        const chat = agentOf(kind).caps.chat;
        // Now that the session exists, upload any pasted images to it and fold their
        // saved paths into the first prompt (claude opens them with its Read tool).
        let seed = prompt;
        if (images?.length && agentOf(kind).caps.imagePaste) {
          const paths: string[] = [];
          for (const f of images) {
            try {
              const up = await pasteImage(res.name, f);
              if (up.status < 300 && up.path) paths.push(up.path);
              else toast("画像のアップロードに失敗しました: " + (up.error ? errText(up.error) : ""));
            } catch {
              toast("画像のアップロードに失敗しました（通信エラー）");
            }
          }
          seed = buildImagePrompt(prompt, paths, kind);
        }
        if (seed) {
          // Chat-capable: stash as a launch seed — MirrorView auto-sends it once the
          // session is alive. Other kinds: paste once the PTY is up.
          if (chat) setLaunchSeed(res.name, seed);
          else sendPromptWhenAlive(res.name, seed);
          if (prompt) pushPromptHistory(r.name, prompt);
        }
        if (worktree) {
          void refreshRepos();
          useFilesStore.getState().bump();
        }
        void refreshSessions();
        (chat ? openSessionChat : openSessionTerminal)(res.name);
        return { ok: true };
      }}
      onBranchChanged={() => {
        // A checkout / new branch changed HEAD and the working tree.
        void refreshRepos();
        useFilesStore.getState().bump();
      }}
    />
  );
}
