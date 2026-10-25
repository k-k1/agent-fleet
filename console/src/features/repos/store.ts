// Repos store (zustand): working copies under ~/repos. Replaces the old
// reposKey bump counter — sections call refresh() directly.
import { create } from "zustand";
import { api, getTenant, isTransientErr } from "../../core/api/client.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";

// A working copy from GET /api/repos.
export interface Repo {
  name: string;
  workingCopyId?: string;
  path?: string;
  branch?: string;
  dirty?: boolean;
  ahead?: number;
  behind?: number;
  provider?: string; // origin host slug: github/bitbucket/gitlab, or a bare host
  remote?: string; // origin host (tooltip)
  /** The repository's path on that host ("owner/name"), credentials stripped. `remote` +
   * this is the identity every working copy of one repository shares — the base clone, each
   * linked worktree, and a second clone under another folder name — which is what the
   * sessions overview groups by (ADR 0078). Absent for a working copy with no origin. */
  remotePath?: string;
  worktree?: boolean; // linked git worktree (not a standalone clone)
  parent?: string; // for a worktree, the parent working copy's folder name
  createdAt?: string; // for a worktree, its creation time (RFC3339); orders worktrees under a base
  /** Deletion lock (docs/log/45): while true, deletion is refused with 403 even when
   * forced, and an emptied worktree is excluded from automatic pruning. */
  locked?: boolean;

  /** A git working copy with no commit yet (just created by `POST /api/repos/init`, or a
   * clone of an empty remote). It has a branch name but HEAD does not resolve, so
   * `git worktree add` fails — the "new working copy" option cannot be offered here. */
  unborn?: boolean;

  /** Working-copy kind (docs/log/41): "git" (default/omitted) or "svn". SVN copies are
   * flat — no branch/ahead/behind/worktree — so the Console gates git-only actions
   * on it and shows the revision/URL below instead. */
  vcs?: "git" | "svn";
  revision?: string; // SVN: current working-copy revision
  url?: string; // SVN: repository URL of the working copy

  /** Commit relationship to the parent working copy's current HEAD. This is
   * independent of ahead/behind above, which are relative to the upstream. */
  integration?: {
    targetBranch?: string;
    /** targetBranch is the parent branch's upstream (e.g. origin/develop), not the parent's HEAD. */
    targetUpstream?: boolean;
    targetUnique: number;
    worktreeUnique: number;
    relation: "same" | "contained" | "unmerged" | "diverged" | "unknown";
  };
}

/** Outcome of the last load: "loading" until the first answer (or after clear()), "ok" once a
 * 2xx result carrying a repos array was committed, "unreachable" when the last attempt failed
 * TRANSIENTLY (5xx, dropped fetch) and the caller retries, "failed" when the answer was terminal
 * (a 4xx, a non-JSON body): retrying will not change it, so the UI must not promise it will. */
export type ReposLoadState = "loading" | "ok" | "unreachable" | "failed";

interface ReposStore {
  repos: Repo[];
  loadState: ReposLoadState;
  /** Loads GET /api/repos. Only a 2xx result whose `repos` is an array replaces the list; every
   * other outcome leaves `repos` untouched and records loadState "unreachable", because "could
   * not read the list" is not "there are no repos" — committing it is what used to drop the
   * whole rail into the empty state and every session into "Other sessions".
   * Resolves false when the failure is TRANSIENT (the CP's plain-text 502 while the agent is
   * still booting, a 5xx, a dropped fetch) so the caller retries; true once the answer is
   * terminal (a committed list, or a 4xx / non-JSON body that retrying will not change).
   * A *stopped* workspace answers with the same 502 (control-plane/proxy.go), so only a caller
   * that knows the workspace state can tell "booting" from "stopped". */
  refresh(): Promise<boolean>;
  /** Reflect a successful deletion-lock toggle before the next list refresh. */
  setLocked(name: string, locked: boolean): void;
  /** Forget the list and go back to "loading". For a tenant switch, where the rail's repos
   * belong to the previous workspace; never for a failed load. */
  clear(): void;
}

// Bumped by every refresh() and by clear(). A response only counts when no later refresh
// started (or clear ran) while it was in flight: refreshes run concurrently from the rail's
// retry loop, the 60s poll, SSE and the SCM view, and a slow old failure must not overwrite the
// outcome of a newer success (nor an old tenant's answer the new tenant's).
let generation = 0;

export const useReposStore = create<ReposStore>((set) => ({
  repos: [],
  loadState: "loading",
  async refresh() {
    const tenant = getTenant();
    const mine = ++generation;
    let d: { repos?: unknown; error?: unknown };
    try {
      d = await api("api/repos");
    } catch {
      // network drop — transient; keep what the rail has.
      if (mine === generation && getTenant() === tenant) set({ loadState: "unreachable" });
      return false;
    }
    // The answer describes the workspace of the tenant that was active when it was asked; one
    // landing after a tenant switch would put the previous tenant's repos back on the rail.
    if (getTenant() !== tenant || mine !== generation) return false;
    if (isTransientErr(d)) {
      set({ loadState: "unreachable" });
      return false;
    }
    // A 4xx body carries `error`; an HTML page from an auth gateway resolves as {}. Neither is
    // a list.
    if (!d || d.error || !Array.isArray(d.repos)) {
      set({ loadState: "failed" });
      return true;
    }
    set({ repos: d.repos as Repo[], loadState: "ok" });
    return true;
  },
  setLocked(name, locked) {
    set((s) => ({ repos: s.repos.map((r) => (r.name === name ? { ...r, locked } : r)) }));
  },
  clear: () => {
    generation++;
    set({ repos: [], loadState: "loading" });
  },
}));

/** Reveal-a-repo-in-the-rail signal (mirrors useFilesStore.revealInFiles): the
 * command palette's repo row lands here so the project tree expands the working
 * copy's node (and its base, for a worktree), scrolls it into view, and focuses
 * its row. `n` bumps per request so identical repeat reveals still fire. */
interface RepoRevealStore {
  name: string | null;
  n: number;
  reveal(name: string): void;
}

export const useRepoReveal = create<RepoRevealStore>((set) => ({
  name: null,
  n: 0,
  reveal: (name) => set((s) => ({ name, n: s.n + 1 })),
}));

/** Cross-surface "start work on this working copy" signal (launch flow Ph2/Ph3): the
 * start hub's repo pick and the clone-only toast's "start now" both land here;
 * StartHost renders the LaunchModal on it. */
interface LaunchTargetStore {
  target: Repo | null;
  /** Preselect this EXISTING branch in the launch dialog instead of the usual
   * new-branch flow ("" = new branch). Set by the SCM view's "start work on this branch",
   * which knows the branch before the dialog exists. */
  existingBranch: string;
  /** The caller already chose "directly in this copy" (docs/log/80: the work item flow picks the
   * working copy — new worktree or an existing one — before the launch dialog opens).
   * The dialog would otherwise re-default to "new worktree" and undo that answer. */
  inPlace: boolean;
  open(r: Repo, existingBranch?: string, inPlace?: boolean): void;
  clear(): void;
}

export const useLaunchTarget = create<LaunchTargetStore>((set) => ({
  target: null,
  existingBranch: "",
  inPlace: false,
  open: (r, existingBranch = "", inPlace = false) => set({ target: r, existingBranch, inPlace }),
  clear: () => set({ target: null, existingBranch: "", inPlace: false }),
}));

/** The work item a launch was seeded from (docs/log/80). */
export interface LaunchWorkItem {
	provider: string;
	key: string;
	branch: string;
	title: string;
	type: string;
	labels: string[];
}

/** A first-prompt seed for the next launch (docs/log/21 UI overhaul): the memo send modal's
 * "start a new session" stashes the composed memo text here, then opens the launch hub.
 * LaunchModal reads it once to prefill its prompt field, then it's cleared. */
interface LaunchSeedStore {
	prompt: string;
	title: string;
	/** Session whose handoff proposal seeded this launch ("" = not a handoff). The
	 *  dialog's success path badges that proposal as launched; a cancelled dialog leaves it
	 *  untouched, which is why the mark can't happen when the seed is set. */
	handoffSession: string;
	/** Which of that session's (possibly several) outstanding proposals this is — a
	 *  session may propose more than one handoff in a turn, so the session name alone
	 *  no longer identifies the card to badge. */
	handoffId: string;
	/** Offer id of a handoff received from another member (docs/log/77); "" = none. Held so
	 *  acceptance is declared only once the launch succeeds — a cancelled launch must not be
	 *  recorded as accepted, so, like handoffId, it cannot be declared when the seed is set. */
	handoffOfferId: string;
	/** The work item (docs/log/80) this launch came from. Held so the ledger gets its row only
	 *  AFTER the launch succeeds — same reason as handoffOfferId: seeding must not claim the
	 *  work was started, since the dialog can still be cancelled. `branch` is the Console's own
	 *  suggestion (branchForItem), used when the Agent has no branch-name resolver; title, type
	 *  and labels are what the resolver names the branch from (ADR 0103 decision 8). */
	workItem: LaunchWorkItem | null;
	/** The inbox request this launch comes from (ADR 0111); "" = none. The launch then goes
	 *  through the CP's request launch, which checks that the caller is still the assignee
	 *  before the session is created and records the session on the request. */
	inboxItemId: string;
	/** The memo taken in from the inbox this launch was seeded from (ADR 0111); "" = none. The
	 *  Control Plane looks it up and stamps the session's provenance from the memo's origin. */
	inboxMemoId: string;
	set(p: string, title?: string, handoffSession?: string, handoffId?: string, handoffOfferId?: string, workItem?: LaunchWorkItem | null): void;
	/** Runs once, after a launch seeded from this store has created its session (never on
	 *  cancel). Gets the new session's name ("" when the launch did not report one). Dropped by set()/clear(), so a cancelled dialog leaves nothing behind. */
	onLaunched: ((sessionName: string) => void) | null;
	setOnLaunched(fn: ((sessionName: string) => void) | null): void;
	setInboxItem(id: string): void;
	setInboxMemo(id: string): void;
  clear(): void;
}

export const useLaunchSeed = create<LaunchSeedStore>((set) => ({
	prompt: "",
	title: "",
	handoffSession: "",
	handoffId: "",
	handoffOfferId: "",
	workItem: null,
	inboxItemId: "",
	inboxMemoId: "",
	onLaunched: null,
	setOnLaunched: (onLaunched) => set({ onLaunched }),
	// set() drops inboxItemId: a seed left over from a cancelled inbox launch must not send an
	// unrelated launch through the request route. The inbox calls setInboxItem after set().
	set: (prompt, title = "", handoffSession = "", handoffId = "", handoffOfferId = "", workItem = null) =>
		set({ prompt, title, handoffSession, handoffId, handoffOfferId, workItem, inboxItemId: "", inboxMemoId: "", onLaunched: null }),
	setInboxItem: (inboxItemId) => set({ inboxItemId }),
	setInboxMemo: (inboxMemoId) => set({ inboxMemoId }),
	clear: () => set({ prompt: "", title: "", handoffSession: "", handoffId: "", handoffOfferId: "", workItem: null, inboxItemId: "", inboxMemoId: "", onLaunched: null }),
}));

/** Poll every 60s while the tab is visible AND the workspace is running, so the
 * origin-ahead badge (kept fresh server-side by the Agent's auto-fetch) updates
 * without a manual refresh. Skipped while stopped/booting — the agent proxy only
 * 502s then (docs/log/35 §35.9-9). Returns cleanup (StrictMode-safe). */
export function startReposPolling(): () => void {
  const load = () => {
    if (document.hidden || !wsRunning(useWorkspaceStore.getState().state)) return;
    void useReposStore.getState().refresh();
  };
  load();
  const t = setInterval(load, 60000);
  return () => clearInterval(t);
}
