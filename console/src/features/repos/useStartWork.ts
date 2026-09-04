// useStartWork — the "start work" submit path (POST /api/sessions with worktree /
// branch-conflict handling, pasted-image upload, first-prompt seeding, refreshes,
// pane open), kept out of RepoRowConnected so the per-repo LaunchModal and the
// start hub (StartModal / LaunchHost, launch flow Ph2) share one implementation.
// `dir: ""` launches in the home directory (repo-less session) — worktree options
// and repo-scoped bookkeeping (repoLast / prompt history) are skipped there.
import { apiJSON, errDetail, errText, pasteImage } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { writeRepoLast, writeRepoSubdir } from "../../lib/repoLast.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
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

  return async ({ dir, repo }, { kind, driver, model, effort, startMode, skipPermissions, prompt, title, images, worktree, subdir, base, newBranch, useExisting }) => {
    const hasModel = agentOf(kind).caps.model;
    // With attachments the first prompt's text cannot be fixed until the session exists,
    // because the saved paths have to be woven into it and the upload target IS that
    // session. Otherwise it rides on the create request and the Agent delivers it.
    const withImages = !!(images?.length && agentOf(kind).caps.imagePaste);
    const body: Record<string, unknown> = { dir, kind };
    if (driver) body.driver = driver;
    if (hasModel && model) body.model = model;
    if (effort) body.effort = effort;
    if (startMode) body.mode = startMode;
    // Permission confirmation (docs/log/76). Do not send the value when it matches the
    // default: left unspecified, the Agent resolves the per-kind default (ui-prefs), so a
    // session started after a settings change picks the new default up. Baking a value in
    // here every time would freeze that kind's default at launch time.
    if (typeof skipPermissions === "boolean" && agentOf(kind).caps.permissionChoice) {
      body.skip_permissions = skipPermissions;
    }
		if (title) body.title = title;
    // Working directory (Meta.Subdir): the Agent resolves it INSIDE whatever working
    // copy the launch lands in — including a worktree it creates in this same call —
    // and rejects a path that isn't there, so no client-side existence check.
    if (subdir) body.subdir = subdir;
    // Let the Agent deliver the first prompt (deliverInitialPrompt; driver.Send when
    // managed). It waits for the CLI to draw its composer, re-presses Enter once the paste
    // window has closed, and verifies delivery — and above all it runs even when nobody is
    // looking at the Console. Sending from the chat mirror only worked while that mirror was
    // mounted, so switching tabs right after launch made the prompt appear to be sent the
    // moment the tab was reopened (panes in background tabs do not render).
    if (prompt && !withImages) body.initial_prompt = prompt;
    if (worktree) {
      body.worktree = true;
      body.branch = base;
      body.new_branch = newBranch;
      if (useExisting) body.use_existing = true;
    }
    let res = await apiJSON("api/sessions", "POST", body);
    // Fleet rebuild lag: a P1.5-generation Agent rejects an explicit driver:"managed"
    // (driver_unsupported). If managed cannot be started, starting on tui is kinder than
    // failing — drop it once, resend, and say so in a toast.
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
      // errDetail, not errText: launch failure codes are generic ones like runtime_failed,
      // and WHY it failed lives only in the server's message. errText drops that message
      // whenever the catalogue has wording for the code, leaving a bare "wait and retry"
      // with the cause gone.
      toast(
        worktree
          ? t("rp.worktree_launch_failed", { err: errDetail(res.error) })
          : t("rp.launch_failed", { err: errDetail(res.error) }),
      );
      return { ok: false };
    }
    if (repo) {
      writeRepoLast(repo, kind, hasModel ? model : undefined, effort, startMode);
      writeRepoSubdir(repo, subdir || "");
    }
    // A repo-less (home) session has no group membership to inherit, so add it directly to
    // the selected working set (docs/log/52 §1). A launch inside a repo inherits the repo's.
    if (!dir) autoAddToActiveWorkingSet("sessions", res.name);
    const chat = agentOf(kind).caps.chat;
    // Now that the session exists, upload any pasted images to it and fold their
    // saved paths into the first prompt (claude opens them with its Read tool).
    let seed = prompt;
    if (withImages) {
      const paths: string[] = [];
      for (const f of images ?? []) {
        try {
          const up = await pasteImage(res.name, f);
          if (up.status < 300 && up.path) paths.push(up.path);
          else toast(t("rp.image_upload_failed", { err: up.error ? errText(up.error) : "" }));
        } catch {
          toast(t("rp.image_upload_failed_network"));
        }
      }
      seed = buildImagePrompt(prompt, paths, kind);
      // Hand what could not ride on the create request to the same delivery path (wait for
      // readiness + second Enter + delivery check). Do not swallow a rejection — losing it
      // silently reads as "I sent it and nothing started".
      const del = await apiJSON(`api/sessions/${encodeURIComponent(res.name)}/input`, "POST", {
        prompt: seed,
        when_ready: true,
      }).catch(() => ({ error: { message: t("err.network") } }));
      if (del?.error) toast(t("rp.first_prompt_failed", { err: errText(del.error) }));
    }
    if (seed) {
      // The send itself runs on the Agent side; the mirror only shows the submitted text as
      // an optimistic echo (launchSeed is a display-only handoff), so chat is not empty for
      // the few seconds between launch and the first turn reaching the transcript.
      if (chat) setLaunchSeed(res.name, seed);
      if (prompt && repo) pushPromptHistory(repo, prompt);
    }
    if (worktree) {
      void refreshRepos();
      useFilesStore.getState().bump();
    }
    void refreshSessions();
    (chat ? openSessionChat : openSessionTerminal)(res.name);
    // The created session's name is returned so that accepting a handoff (docs/log/77) can
    // tell the owner WHICH session took it. Callers may look at ok alone.
    return { ok: true, name: res.name };
  };
}
