// useStartWork — the "start work" submit path (POST /api/sessions with worktree /
// branch-conflict handling, pasted-image upload, first-prompt seeding, refreshes,
// pane open), kept out of RepoRowConnected so the per-repo LaunchModal and the
// start hub (StartModal / LaunchHost, launch flow Ph2) share one implementation.
// `dir: ""` launches in the home directory (repo-less session) — worktree options
// and repo-scoped bookkeeping (repoLast / prompt history) are skipped there.
import { apiJSON, errDetail, errText, pasteImage } from "../../core/api/client.ts";
import { routeSelectionBody } from "./bedrockProfiles.ts";
import { policyRefusalKey, policyRefusalOf } from "./launchPolicy.ts";
import { AUTH_EXPIRED_CODE, toastSendFailure } from "../../lib/authExpiredToast.tsx";
import { t } from "../../lib/i18n/index.ts";
import { buildImagePrompt } from "../../lib/pastedImages.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { agentOf } from "../../agents/registry.ts";
import { writeRepoLast, writeRepoSubdir } from "../../lib/repoLast.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { pushPromptHistory } from "../../lib/promptHistory.ts";
import { setLaunchSeed } from "../../lib/launchSeed.ts";
import { useLaunchSeed, useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { useInboxStore } from "../inbox/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import { stashLaunchDraft } from "../memo/launchStash.ts";
import { useSpacesStore } from "../spaces/store.ts";

export interface StartTarget {
  /** Working-copy path ("" = home / repo-less). */
  dir: string;
  /** Working-copy folder name for repoLast / prompt history ("" = skip). */
  repo: string;
  /** The launch does not carry the launch seed's text (a repository picked in the Start hub
   *  without the hub's draft): none of the seed's lineage, work item or inbox provenance rides
   *  along either, since the session never received what they describe. */
  seedless?: boolean;
  /** The inbox request this launch answers, named by the caller instead of read from the launch
   *  seed: the seed is global, so a dialog that outlives its opener (or is opened for two
   *  requests at once) must not take whichever request set it last. */
  inboxItem?: string;
}

// What a seedless launch reads in place of the launch seed.
const EMPTY_SEED = { handoffSession: "", handoffId: "", workItem: null, inboxItemId: "", inboxMemoId: "" } as const;

/** The handoff take a launch from another member's offer carries (acceptHandoff.ts). */
export interface HandoffTake {
  offerId: string;
  token: string;
  /** Gives this take up and takes the offer again, for a second create: the CP lets a take
   *  start one create only (#2040). null when the offer could not be taken again. */
  renew?: () => Promise<HandoffTake | null>;
}

/**
 * useStartWork creates a session for a launch. policyInline: the caller is the launch dialog,
 * which explains a refusal of the credential policy beside its route picker; every other caller
 * gets the same sentence as a toast.
 */
export function useStartWork({ policyInline = false }: { policyInline?: boolean } = {}): (
  target: StartTarget,
  opts: LaunchOpts,
  take?: HandoffTake,
) => Promise<LaunchResult> {
  const toast = useToast();
  const refreshRepos = useReposStore((s) => s.refresh);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  return async ({ dir, repo, seedless = false, inboxItem = "" }, { kind, driver, model, effort, route, routeConnection, startMode, skipPermissions, prompt, title, images, worktree, subdir, base, newBranch, useExisting, spendCapUsd, space }, take) => {
    const hasModel = agentOf(kind).caps.model;
    // With attachments the first prompt's text cannot be fixed until the session exists,
    // because the saved paths have to be woven into it and the upload target IS that
    // session. Otherwise it rides on the create request and the Agent delivers it.
    const withImages = !!(images?.length && agentOf(kind).caps.imagePaste);
    const body: Record<string, unknown> = { dir, kind };
    if (driver) body.driver = driver;
    if (hasModel && model) body.model = model;
    if (effort) body.effort = effort;
    // An explicit model route (ADR 0128), only when one was chosen: the default sends no field,
    // so a launch that never touched the picker is byte-identical to what it always was. The
    // connection rides only with a route that needs one (bedrock: the member's marked profile).
    if (route) body.route = routeSelectionBody(route, routeConnection);
    if (startMode) body.mode = startMode;
    // Permission confirmation (docs/log/76). Do not send the value when it matches the
    // default: left unspecified, the Agent resolves the per-kind default (ui-prefs), so a
    // session started after a settings change picks the new default up. Baking a value in
    // here every time would freeze that kind's default at launch time.
    if (typeof skipPermissions === "boolean" && agentOf(kind).caps.permissionChoice) {
      body.skip_permissions = skipPermissions;
    }
		if (title) body.title = title;
    // Spend budget (#1054): only when the user typed one; absent, the Agent applies the default.
    if (typeof spendCapUsd === "number") body.spend_cap_usd = spendCapUsd;
    // Working directory (Meta.Subdir): the Agent resolves it INSIDE whatever working
    // copy the launch lands in — including a worktree it creates in this same call —
    // and rejects a path that isn't there, so no client-side existence check.
    if (subdir) body.subdir = subdir;
    // A space (ADR 0142) names the directory in place of dir, which stays "": the Agent makes or
    // looks up ~/spaces/<folder> and refuses the field together with dir or a worktree.
    if (space) body.space = space;
    // Let the Agent deliver the first prompt (deliverInitialPrompt; driver.Send when
    // managed). It waits for the CLI to draw its composer, re-presses Enter once the paste
    // window has closed, and verifies delivery — and above all it runs even when nobody is
    // looking at the Console. Sending from the chat mirror only worked while that mirror was
    // mounted, so switching tabs right after launch made the prompt appear to be sent the
    // moment the tab was reopened (panes in background tabs do not render).
    if (prompt && !withImages) body.initial_prompt = prompt;
    // Lineage for a launch seeded by a session's handoff proposal (ADR 0073 decision 1,
    // amendment 2026-09-10). The origin stays "user" — a person opened this session and that is
    // what the accounting axis records — so only the lineage rides along. The Agent proves the
    // pair against that session's stored proposals and drops it when it cannot, so this is a
    // hint the server checks, never a claim it takes. Read from the store rather than through a
    // prop because it is the same source StartHost badges the proposal from, and the two must
    // not be able to disagree about which proposal this launch was.
    const launchSeed = seedless ? EMPTY_SEED : useLaunchSeed.getState();
    const { handoffSession, handoffId } = launchSeed;
    if (handoffSession && handoffId) {
      body.origin_session = handoffSession;
      body.origin_proposal = handoffId;
    }
    // The work item a launch was seeded from goes into the session meta, so a later rename
    // through the branch-name resolver keeps {ref} and the kind (ADR 0103 decision 8). The same
    // store as the lineage above, for the same reason. An older Agent ignores the field (the
    // create decodes leniently), so it needs no fallback.
    const { workItem } = launchSeed;
    if (workItem) {
      body.work_item = { provider: workItem.provider, key: workItem.key, title: workItem.title, type: workItem.type, labels: workItem.labels };
    }
    // A launch from another member's offer carries its take (#2020). The CP confirms it
    // immediately before the Agent is asked and strips it from the body, so a take that was
    // lost meanwhile (handoff_take_lost) creates nothing.
    if (take) body.handoff_take = { offerId: take.offerId, token: take.token };
    if (worktree) {
      body.worktree = true;
      body.branch = base;
      body.new_branch = newBranch;
      if (useExisting) body.use_existing = true;
    }
    // A launch from an inbox request goes through the CP's request launch: the same create
    // body, but the CP checks the claim first and records the session on the request.
    const inboxItemId = inboxItem || launchSeed.inboxItemId;
    const { inboxMemoId } = launchSeed;
    // The memo a launch was seeded from: the Control Plane turns it into the session's
    // provenance badge when the memo was taken in from the inbox. It names the memo, never
    // the origin itself, which the server alone sets.
    if (inboxMemoId) body.inbox_memo_id = inboxMemoId;
    const createPath = inboxItemId ? `api/inbox/items/${encodeURIComponent(inboxItemId)}/launch` : "api/sessions";
    // A launch from an offer must tell "nothing was created" from "nobody knows": the first
    // gives the take back, the second keeps it so the next press can ask the Agent (#2040).
    // No answer, or an answer that is not the server's own JSON error (a gateway page, the
    // proxy's "unreachable"), is the second.
    const post = () =>
      apiJSON(createPath, "POST", body).catch((e: unknown) => {
        if (take) return { lost: true };
        throw e;
      });
    let res = await post();
    // Fleet rebuild lag: a P1.5-generation Agent rejects an explicit driver:"managed"
    // (driver_unsupported). If managed cannot be started, starting on tui is kinder than
    // failing — drop it once, resend, and say so in a toast.
    if (res?.error && driver === "managed" && (res.error as { code?: string }).code === "driver_unsupported") {
      toast(t("rp.managed_unsupported"));
      delete body.driver;
      if (!agentOf(kind).caps.tuiEffort) delete body.effort;
      if (!agentOf(kind).caps.tuiStartMode) delete body.mode;
      if (take) {
        const next = take.renew ? await take.renew() : null;
        if (!next) return { ok: false };
        take = next;
        body.handoff_take = { offerId: next.offerId, token: next.token };
      }
      res = await post();
    }
    if (res?.lost) return { ok: false, unknown: true };
    if (res && res.error) {
      const code = typeof res.error === "object" ? res.error.code : "";
      if (take && (String(code).startsWith("http_") || code === "create_in_progress")) return { ok: false, unknown: true };
      // The space was removed since the list was read: read it again, so the form shows that it is
      // gone instead of offering the same pick for a retry.
      if (code === "space_not_found") void useSpacesStore.getState().refresh();
      // An agent is running in the space (#5230): the form asks "new space or here anyway", so no
      // toast. The session list is read again so the place chip marks that space too.
      if (code === "space_busy") {
        void refreshSessions();
        return { ok: false, spaceBusy: { session: String(res.error.session || ""), display: res.error.display ? String(res.error.display) : undefined } };
      }
      if (code === "branch_exists") return { ok: false, conflict: "local" as const };
      if (code === "branch_exists_remote") return { ok: false, conflict: "remote" as const };
      // The branch is checked out in another working copy (git allows only one). The
      // payload names it so the dialog can say WHERE instead of just failing.
      if (code === "branch_in_use")
        return { ok: false, conflict: "in_use" as const, worktree: String(res.error.worktree || "") };
      // The credential policy refused it: said in the Console's own words, which name nothing
      // the policy hides (ADR 0138 D10a).
      const policy = policyRefusalOf(res.error);
      if (policy) {
        if (!policyInline) toast(t(policyRefusalKey(policy)));
        return { ok: false, policy };
      }
      // errDetail, not errText: launch failure codes are generic ones like runtime_failed,
      // and WHY it failed lives only in the server's message. errText drops that message
      // whenever the catalogue has wording for the code, leaving a bare "wait and retry"
      // with the cause gone.
      toast(
        worktree
          ? t("rp.worktree_launch_failed", { err: errDetail(res.error) })
          : t("rp.launch_failed", { err: errDetail(res.error) }),
      );
      return { ok: false, spaceGone: code === "space_not_found" || undefined };
    }
    if (repo) {
      writeRepoLast(repo, kind, hasModel ? model : undefined, effort, startMode);
      writeRepoSubdir(repo, subdir || "");
    }
    // A repo-less (home) session has no group membership to inherit, so add it directly to
    // the selected working set (docs/log/52 §1). A launch inside a repo inherits the repo's.
    if (!dir) autoAddToActiveWorkingSet("sessions", res.name);
    const chat = agentOf(kind).caps.chat;
    // Now that the session exists, upload any staged attachments to it and fold their
    // saved paths into the first prompt (claude opens them with its Read tool).
    let seed = prompt;
    // What of the draft did not reach the session that now exists (#5077): the names of the
    // files whose upload failed, and whether the first prompt's delivery was refused. Without
    // this the caller finishes the seed (deletes the memo, clears the draft) and both are lost.
    const lostFiles: string[] = [];
    let promptLost = false;
    if (withImages) {
      const paths: string[] = [];
      // A failed file is dropped with the rest of the draft, so its toast names it: that is
      // the user's only way to know what to resend. No client-side size check — the per-file
      // cap is the Agent's AF_UPLOAD_MAX (413 paste_too_large), which the Console never sees.
      for (const f of images ?? []) {
        try {
          const up = await pasteImage(res.name, f);
          if (up.status < 300 && up.path) paths.push(up.path);
          else {
            lostFiles.push(f.name);
            toast(t("rp.image_upload_failed", { name: f.name, err: up.error ? errText(up.error) : "" }));
          }
        } catch {
          lostFiles.push(f.name);
          toast(t("rp.image_upload_failed_network", { name: f.name }));
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
      if (del?.error) {
        promptLost = true;
        if (del.error.code === AUTH_EXPIRED_CODE) toastSendFailure(toast, { code: del.error.code }, "", kind);
        else toast(t("rp.first_prompt_failed", { err: errText(del.error) }));
      }
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
    // The request card shows the session the CP just recorded on it.
    if (inboxItemId) void useInboxStore.getState().refresh();
    (chat ? openSessionChat : openSessionTerminal)(res.name);
    // The created session's name is returned so that accepting a handoff (docs/log/77) can
    // tell the owner WHICH session took it. Callers may look at ok alone.
    // The session exists, so this is still ok; the draft's missing part is reported apart so
    // that no caller deletes the memo or clears the draft the person would resend from.
    if (lostFiles.length || promptLost) {
      // The form closes on ok and a hub or space dialog keeps no draft of its own, so what the
      // person typed or attached would be gone. Save the whole draft as a new memo (the restored
      // memo, if any, stays too) and say whether that worked.
      const stash = await stashLaunchDraft(repo, prompt, images ?? []);
      toast(t(stash.ok ? "rp.draft_saved" : "rp.draft_kept"));
      return { ok: true, name: res.name, incomplete: { files: lostFiles, prompt: promptLost, saved: stash.ok } };
    }
    return { ok: true, name: res.name };
  };
}
