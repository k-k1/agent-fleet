// useStartWork — the "作業を始める" submit path (POST /api/sessions with worktree /
// branch-conflict handling, pasted-image upload, first-prompt seeding, refreshes,
// pane open), extracted from RepoRowConnected so the per-repo LaunchModal and the
// はじめる hub (StartModal / LaunchHost, 起動導線 Ph2) share one implementation.
// `dir: ""` launches in the home directory (repo-less session) — worktree options
// and repo-scoped bookkeeping (repoLast / prompt history) are skipped there.
import { apiJSON, errText, pasteImage } from "../../core/api/client.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { writeRepoLast } from "../../lib/repoLast.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal, sendPromptWhenAlive } from "../sessions/open.ts";
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

export interface StartTarget {
  /** Working-copy path ("" = home / repo-less). */
  dir: string;
  /** Working-copy folder name for repoLast / prompt history ("" = skip). */
  repo: string;
}

export function useStartWork(): (target: StartTarget, opts: LaunchOpts) => Promise<LaunchResult> {
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  return async ({ dir, repo }, { kind, model, prompt, images, worktree, base, newBranch, useExisting }) => {
    const hasModel = agentOf(kind).caps.model;
    const body: Record<string, unknown> = { dir, kind };
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
    if (repo) writeRepoLast(repo, kind, hasModel ? model : undefined);
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
      if (prompt && repo) pushPromptHistory(repo, prompt);
    }
    if (worktree) {
      void refreshRepos();
      useFilesStore.getState().bump();
    }
    void refreshSessions();
    (chat ? openSessionChat : openSessionTerminal)(res.name);
    return { ok: true };
  };
}
