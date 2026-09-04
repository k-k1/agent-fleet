// sessionFiles — the model behind "Changed files" (docs/log/68): the files THIS session's
// agent edited, joined with the working tree's current git state.
//
// Two sources, deliberately:
//   - the population comes from the transcript (`files` on GET /sessions/{name}/messages),
//     which is the only thing that knows the session axis. It is aggregated over the
//     WHOLE transcript server-side — the turns the mirror holds are a tail window, so
//     anything counted here would undercount and then grow as the reader scrolls up.
//   - each row's STATE comes from GET /fs/changes, because the transcript only records
//     that an edit happened, never that it was later reverted or committed.
//
// The join key is (repo, rel), never `path`: browse-relative paths are rooted at the
// Agent's browse root (AF_BROWSE_ROOT) while /fs/changes always reports
// "repos/<repo>/<rel>". They agree by default, which is exactly why a mismatch would go
// unnoticed. See decisions/0049 decision 2.
import { create } from "zustand";
import { compareText } from "../../lib/intl.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { openFileDiff } from "../scm/open.ts";
import { openFileMode } from "../viewer/openFile.ts";
import { useLayoutStore } from "../../layout/store.ts";

/** One file the session edited, as the Agent aggregates it (transcript.FileTouch). */
export interface SessionFile {
  path: string; // browse-root relative — what the file view opens
  repo?: string; // working-copy folder; absent = outside ~/repos
  rel?: string; // repo-relative; absent = outside ~/repos
  verb: string; // "edit" | "add" | "delete"
  added?: number;
  removed?: number;
  count: number; // how many edit calls touched it
  lastIdx: number;
  lastTs?: string;
  sidechain?: boolean; // ONLY subagents touched it
}

/** One working-tree change from GET /fs/changes. */
export interface FsChange {
  path: string;
  repo: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

/**
 * What the working tree says about a file the session edited.
 *   unstaged/staged/untracked — git has a diff to show
 *   committed — no working-tree diff left, but the path appeared in a commit made since
 *             this session started (docs/log/68 P2).
 *   clean   — no working-tree diff and no such commit. This is NOT "reverted": the
 *             edit may have landed in an older commit, in another working copy, or under
 *             a name git no longer reports. Only the positive claim is made; the rest
 *             stays "No diff". And these rows are KEPT either way — dropping them reads
 *             to the user as "I just edited that and it is not in the list", i.e. as a
 *             broken feature.
 *   outside — edited outside ~/repos (an agent config, a file in the home dir). Listed,
 *             but there is no git side and no diff to open.
 */
export type FileState = "unstaged" | "staged" | "untracked" | "committed" | "clean" | "outside";

export interface FileRow extends SessionFile {
  name: string; // basename, the row's main label
  dir: string; // repo-relative directory, shown faintly after the name ("" = repo root)
  state: FileState;
  deleted: boolean; // nothing left to open (git says D, or the agent deleted it)
}

export type FileSort = "recent" | "path";

const changeKey = (repo: string, rel: string) => repo + "\u0000" + rel;

/** The porcelain column that decides a change's state (worktree wins, then index) — the
 *  same rule the rail's Changes list uses, so the two never disagree about a badge. */
const codeOf = (c: FsChange): string => (c.worktree && c.worktree !== " " ? c.worktree : c.index || "");

/** Join the session's edited files with the working tree. Order is preserved (the Agent
 *  sends newest touch first); use sortRows to switch to path order.
 *
 *  `committed` is the repo-relative set from GET /sessions/{name}/committed — only
 *  consulted for rows the working tree has nothing to say about. */
export function joinChanges(files: SessionFile[], changes: FsChange[], committed?: string[]): FileRow[] {
  const wasCommitted = committed?.length ? new Set(committed) : null;
  const byKey = new Map<string, FsChange>();
  for (const c of changes) {
    const prefix = "repos/" + c.repo + "/";
    const rel = c.path.startsWith(prefix) ? c.path.slice(prefix.length) : c.path;
    byKey.set(changeKey(c.repo, rel), c);
  }
  return files.map((f) => {
    const rel = f.rel || "";
    const slash = rel.lastIndexOf("/");
    const change = f.repo && rel ? byKey.get(changeKey(f.repo, rel)) : undefined;
    let state: FileState = "clean";
    if (!f.repo || !rel) state = "outside";
    else if (change?.untracked) state = "untracked";
    else if (change) state = change.worktree && change.worktree !== " " ? "unstaged" : "staged";
    else if (wasCommitted?.has(rel)) state = "committed";
    return {
      ...f,
      name: (rel ? rel.slice(slash + 1) : f.path.split("/").pop()) || f.path,
      dir: slash > 0 ? rel.slice(0, slash) : "",
      state,
      deleted: f.verb === "delete" || (!!change && !change.untracked && codeOf(change) === "D"),
    };
  });
}

export function sortRows(rows: FileRow[], sort: FileSort): FileRow[] {
  if (sort === "recent") {
    // The Agent already sorts by newest touch; re-sorting locally keeps the toggle
    // reversible without another fetch.
    return [...rows].sort((a, b) => (b.lastTs || "").localeCompare(a.lastTs || "") || b.lastIdx - a.lastIdx);
  }
  return [...rows].sort(
    (a, b) => compareText(a.repo || "", b.repo || "") || compareText(a.rel || a.path, b.rel || b.path),
  );
}

/** The badge a row shows for its git state. `clean` and `outside` are muted on purpose:
 *  they are still part of the session's work, they just have no diff behind them. */
export function stateBadge(state: FileState): { cls: string; label: MsgKey } {
  switch (state) {
    case "untracked":
      return { cls: "st-add", label: "pj.st_untracked" };
    case "unstaged":
      return { cls: "st-mod", label: "mirror.files.st_unstaged" };
    case "staged":
      return { cls: "st-mod", label: "mirror.files.st_staged" };
    case "committed":
      return { cls: "st-done", label: "mirror.files.st_committed" };
    case "outside":
      return { cls: "st-muted", label: "mirror.files.st_outside" };
    default:
      return { cls: "st-muted", label: "mirror.files.st_clean" };
  }
}

/**
 * Open a row. The default gesture is the DIFF, because that is what "what did this
 * session do to this file" means — but only where git actually has one:
 *
 * an untracked file has no diff (`git diff` prints nothing), so opening one would hand
 * the reader an empty pane. Those rows, and rows with no working-tree change at all,
 * open the file instead — the same rule as the rail's Changes list.
 */
export function openRow(row: FileRow, split = false): void {
  if (row.repo && row.rel && (row.state === "unstaged" || row.state === "staged")) {
    if (split) {
      useLayoutStore
        .getState()
        .openTargetInNew(
          { content: { kind: "wtdiff", scmRepo: row.repo, filePath: row.rel, diffStaged: row.state === "staged" } },
          true,
        );
    } else {
      openFileDiff(row.repo, row.rel, row.state === "staged");
    }
    return;
  }
  if (row.deleted) return; // nothing left on disk to show
  if (split) {
    useLayoutStore.getState().openTargetInNew({ content: { kind: "file", filePath: row.path } }, true);
    return;
  }
  openFileMode(row.path, "view");
}

/**
 * Session → its edited files. The mirror's transcript poll is the writer (the list rides
 * that response, so there is no second poll and the strip can never show a different
 * moment than the turns above it); the command palette is a reader, and fetches once for
 * itself when the mirror for that session was never open.
 */
interface SessionFilesStore {
  bySession: Record<string, SessionFile[]>;
  set(session: string, files: SessionFile[]): void;
  forget(session: string): void;
}

export const useSessionFilesStore = create<SessionFilesStore>((set) => ({
  bySession: {},
  set: (session, files) => set((s) => ({ bySession: { ...s.bySession, [session]: files } })),
  forget: (session) =>
    set((s) => {
      const next = { ...s.bySession };
      delete next[session];
      return { bySession: next };
    }),
}));
