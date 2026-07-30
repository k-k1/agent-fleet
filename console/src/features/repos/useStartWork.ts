// useStartWork — the "作業を始める" submit path (POST /api/sessions with worktree /
// branch-conflict handling, pasted-image upload, first-prompt seeding, refreshes,
// pane open), extracted from RepoRowConnected so the per-repo LaunchModal and the
// はじめる hub (StartModal / LaunchHost, 起動導線 Ph2) share one implementation.
// `dir: ""` launches in the home directory (repo-less session) — worktree options
// and repo-scoped bookkeeping (repoLast / prompt history) are skipped there.
import { apiJSON, errText, pasteImage } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
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

  return async ({ dir, repo }, { kind, driver, model, effort, startMode, prompt, images, worktree, base, newBranch, useExisting }) => {
    const hasModel = agentOf(kind).caps.model;
    const body: Record<string, unknown> = { dir, kind };
    if (driver) body.driver = driver;
    if (hasModel && model) body.model = model;
    if (effort) body.effort = effort;
    if (startMode) body.mode = startMode;
    if (worktree) {
      body.worktree = true;
      body.branch = base;
      body.new_branch = newBranch;
      if (useExisting) body.use_existing = true;
    }
    let res = await apiJSON("api/sessions", "POST", body);
    // フリート再ビルドのラグ: P1.5 世代の Agent は driver:"managed" を明示拒否する
    // （driver_unsupported）。managed で立てられないなら tui で立てて動く方が親切 —
    // 1 回だけ落として再送し、その旨をトーストする。
    if (res?.error && driver === "managed" && (res.error as { code?: string }).code === "driver_unsupported") {
      toast(t("rp.managed_unsupported"));
      delete body.driver;
      if (!agentOf(kind).caps.tuiEffort) delete body.effort;
      if (!agentOf(kind).caps.tuiStartMode) delete body.mode;
      res = await apiJSON("api/sessions", "POST", body);
    }
    if (res && res.error) {
      const code = typeof res.error === "object" ? res.error.code : "";
      if (code === "branch_exists") return { ok: false, conflict: "local" as const };
      if (code === "branch_exists_remote") return { ok: false, conflict: "remote" as const };
      // The branch is checked out in another working copy (git allows only one). The
      // payload names it so the dialog can say WHERE instead of just failing.
      if (code === "branch_in_use")
        return { ok: false, conflict: "in_use" as const, worktree: String(res.error.worktree || "") };
      toast(
        worktree
          ? t("rp.worktree_launch_failed", { err: errText(res.error) })
          : t("rp.launch_failed", { err: errText(res.error) }),
      );
      return { ok: false };
    }
    if (repo) writeRepoLast(repo, kind, hasModel ? model : undefined, effort, startMode);
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
          else toast(t("rp.image_upload_failed", { err: up.error ? errText(up.error) : "" }));
        } catch {
          toast(t("rp.image_upload_failed_network"));
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
