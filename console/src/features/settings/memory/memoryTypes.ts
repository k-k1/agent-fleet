export interface ProjectRef {
  slug: string;
  display: string;
}
export interface MemoryRoot {
  kind: string;
  label: string;
  scopes: boolean;
  files: number;
  bytes: number;
  modified?: string;
  busy?: boolean;
  projects: ProjectRef[];
  /** Whether the agent writes memory at all (codex only for now; docs/log/39 P4). */
  toggleable?: boolean;
  enabled?: boolean;
}
/**
 * A root that is declared but not active in this environment (docs/log/39 P4). codex's
 * memories are off by default upstream, so silently dropping it from the list would tell the
 * user neither why it is missing nor how to enable it. Toggleable ones switch on from here.
 */
export interface InactiveRoot {
  kind: string;
  label: string;
  reason: string;
  toggleable?: boolean;
  enabled?: boolean;
}
export interface RootsPayload {
  roots: MemoryRoot[];
  inactive?: InactiveRoot[];
  auto: boolean;
  autoLocked: boolean;
  lastSnapshot?: string;
}
export interface Snapshot {
  rev: string;
  short: string;
  at: string;
  subject: string;
  trigger: string;
  kinds: string[];
  projects: ProjectRef[];
  files: number;
}
export interface TreeKind {
  kind: string;
  label: string;
  scopes: boolean;
  files: number;
  bytes: number;
}
export interface TreeProject extends ProjectRef {
  files: number;
  bytes: number;
}
/** A suspected secret. The value itself is already masked by the Agent (hint only). */
export interface SecretFinding {
  path: string;
  line: number;
  rule: string;
  hint: string;
  history?: boolean;
}
/** Overview of an imported history (the POST import response). The scope to apply is chosen from it. */
export interface ImportPreview {
  importId: string;
  format: string;
  head: string;
  headTs?: string;
  snapshots: number;
  kinds: TreeKind[];
  projects: TreeProject[];
  unavailable: string[];
  rejected: string[];
  secrets: SecretFinding[];
  /** The secret scan itself failed (Go side: memoryImportPreview.SecretScanFailed). This does
   *  not mean the same as `secrets: []` — not "nothing found" but "could not look". The Go
   *  field is `json:"secretScanFailed,omitempty"`, so the key is absent when false. Keeping it
   *  optional and reading undefined as false is deliberate: on this surface "no key" and
   *  "false" do mean the same thing (the scan ran and raised no flag). The flag only carries
   *  meaning when true, so collapsing the zero value with the missing key is safe. */
  secretScanFailed?: boolean;
}

/**
 * One published change to the AF-owned agent memory (ADR 0108), newest first. `latest` marks
 * the newest change of that memory in the list, the only one the Agent lets the member revert;
 * `live` says the memory exists now.
 */
export interface MemoryChange {
  commit: string;
  at: string;
  op: string;
  scope: string;
  project?: MemoryChangeProject;
  name: string;
  authorKind: string;
  authorSession: string;
  revertOf?: string;
  /** The background review's run that published the change (ADR 0117): the id "revert this run"
   *  takes. Absent for any other author. */
  reviewRun?: string;
  latest: boolean;
  live: boolean;
  /** The history holds the memory's text on at least one side of this change. A forget of a
   *  file that was never committed has none, so there is nothing to bring back. */
  revertible: boolean;
  /** The memory's earlier revisions were purged from the history (#1982): this change cannot
   *  be reverted, only the memory forgotten. */
  purgedBefore?: boolean;
}

/** What purging one memory's past revisions would rewrite (#1982). No memory text. */
export interface PurgePreview {
  scope: string;
  project?: MemoryChangeProject;
  name: string;
  live: boolean;
  keepsCurrent: boolean;
  nothing: boolean;
  changes: { commit: string; at: string; op: string; authorKind: string }[];
  changesTotal: number;
  snapshots: number;
  refs: { name: string; kind: "main" | "branch" | "import" | "rescue"; affected: number }[];
  findings: SecretFinding[];
  plan: string;
}
/** One change's diff from the Agent, scanned first: withheld carries masked findings instead. */
export interface ChangeDiff {
  diff: string;
  withheld?: boolean;
  findings?: SecretFinding[];
}
export interface MemoryChangeProject {
  id: string;
  root: string;
  vcs: string;
  display: string;
  /** Normalised origin ("host/owner/repo", no credentials); absent when unknown. */
  remote?: string;
}

/** One claude project with memory files that could be imported into AF memory (ADR 0108). */
export interface ClaudeImportSource {
  slug: string;
  count: number;
  /** Where the files would go; absent when `reason` says why they cannot (no_project | ambiguous). */
  project?: MemoryChangeProject;
  reason?: string;
}
export type ClaudeImportStatus = "new" | "update" | "unchanged" | "forgotten" | "secret" | "invalid";
/** One claude file in a preview. Findings are masked by the Agent; a hit is never importable. */
export interface ClaudeImportItem {
  name: string;
  status: ClaudeImportStatus;
  reason?: string;
  description?: string;
  type?: string;
  sourceHash?: string;
  sourceModified?: string;
  afUpdated?: string;
  findings?: SecretFinding[];
}
export interface ClaudeImportPreview {
  slug: string;
  project?: MemoryChangeProject;
  reason?: string;
  items: ClaudeImportItem[];
  counts: Partial<Record<ClaudeImportStatus, number>>;
  withheld?: number;
  truncated?: boolean;
}
export interface ClaudeImportResult {
  name: string;
  result: "imported" | "updated" | "skipped";
  reason?: string;
  commit?: string;
}

export type MuseScope = "project" | "personal_project" | "personal";
/** One muse directory with notes: a working copy's `.agents/memory` (#1991), or one of muse's personal stores (#2005). */
export interface MuseImportSource {
  scope: MuseScope;
  /** Working copy name (project), muse's own directory name (personal_project), "" (personal). */
  dir: string;
  /** "user": the personal scope lands in AF's user scope and needs an explicit choice. */
  target?: "user";
  count: number;
  /** Files git tracks in the working copy: they may be shared with teammates. */
  shared: number;
  project?: MemoryChangeProject;
  reason?: string;
}
/** `diverged`: AF has its own version of that name, so muse's file is shown and not applied. */
export type MuseImportStatus = ClaudeImportStatus | "diverged";
export interface MuseImportItem extends Omit<ClaudeImportItem, "status"> {
  status: MuseImportStatus;
  /** git tracks this file in the working copy. */
  shared?: boolean;
  /** The note's path under the scope root, when the name is not simply that path minus ".md". */
  path?: string;
  /** No frontmatter: name from the file name, description from the first line, no type. */
  derived?: boolean;
}
export interface MuseImportPreview extends Omit<ClaudeImportPreview, "slug" | "items" | "counts"> {
  scope: MuseScope;
  dir: string;
  target: "project" | "user";
  items: MuseImportItem[];
  counts: Partial<Record<MuseImportStatus, number>>;
}

/** One AF project whose memory can be written back to claude's own (#1914). */
export interface ClaudeExportSource {
  project: MemoryChangeProject;
  count: number;
  /** no_root: no main working copy is recorded, so claude's directory is unknown. */
  reason?: string;
}
export type ClaudeExportStatus = "new" | "update" | "remove" | "conflict" | "secret" | "unchanged" | "native_only";
export interface ClaudeExportItem {
  name: string;
  status: ClaudeExportStatus;
  reason?: string;
  description?: string;
  type?: string;
  revision?: number;
  afUpdated?: string;
  nativeModified?: string;
  findings?: SecretFinding[];
}
export interface ClaudeExportPreview {
  project: MemoryChangeProject;
  slug: string;
  items: ClaudeExportItem[];
  counts: Partial<Record<ClaudeExportStatus, number>>;
  token: string;
  nativeExists: boolean;
  userScope: number;
  notForClaude?: number;
  withheld?: number;
  index: "new" | "rewrite" | "unchanged" | "symlink";
  indexListed: number;
  indexMore: number;
  switchOn: boolean;
  truncated?: boolean;
}
export interface ClaudeExportResult {
  name: string;
  result: "written" | "updated" | "removed" | "skipped";
  reason?: string;
  /** A hidden file in Claude's memory directory that holds a newer edit which could not be put back. */
  kept?: string;
}

/** One memory in the Console's list (#1703): the pin is the member's, the count is the agents' use. */
export interface MemoryListed {
  name: string;
  scope: "user" | "project";
  project?: MemoryChangeProject;
  description: string;
  type?: string;
  updated: string;
  pinned: boolean;
  uses: number;
  /** What a member forget passes back, so a memory changed since is not removed unseen. */
  revision: number;
  authorKind: string;
  authorSession: string;
}

/** One memory with its body (member-read). */
export interface MemoryBody extends MemoryListed {
  body: string;
  kinds?: string[];
  created?: string;
}

/** One hit of the Console's body search (member-search): no body, the lines that matched. */
export interface MemorySearchHit extends MemoryListed {
  snippets?: string[];
}

/** One project of the store (member `entries/projects`): orphaned means its working copy is known to be gone. */
export interface ProjectRow {
  project: MemoryChangeProject;
  count: number;
  /** Files left out because they failed the scan or cannot be parsed; they are never moved. */
  withheld?: number;
  orphaned: boolean;
  /** A working copy under ~/repos belongs to the project, so it can receive a move before it holds a memory. */
  live: boolean;
  /** The one live project with the same remote as this orphan: a suggested target, never applied on its own. */
  mergeInto?: MemoryChangeProject;
}

export type MoveStatus = "move" | "same" | "collision" | "rename" | "skip";
export type MoveCollision = "skip" | "rename";
export interface MoveItem {
  name: string;
  /** Present when the memory moves under another name (rename) or an earlier run already put it there (same). */
  newName?: string;
  status: MoveStatus;
  description: string;
  type?: string;
  revision: number;
}
export interface MovePreview {
  from: MemoryChangeProject;
  to: MemoryChangeProject;
  items: MoveItem[];
  counts: Partial<Record<MoveStatus, number>>;
  withheld?: number;
  collision?: MoveCollision;
  /** A name is taken in the target and no policy was chosen, so the Agent would refuse the move. */
  needsPolicy?: boolean;
}
export interface MoveResultItem {
  name: string;
  newName?: string;
  /** partial: the copy is in the target and the source copy is still there; failed: nothing was written. */
  result: "moved" | "merged" | "renamed" | "skipped" | "partial" | "failed";
  reason?: string;
  commit?: string;
}

/** One memory of a memory bundle's AF store (`af-import/preview`, #1983). */
export type BundleStatus = "new" | "unchanged" | "exists" | "forgotten" | "secret" | "invalid";
export interface BundleItem {
  name: string;
  status: BundleStatus;
  reason?: string;
  description?: string;
  type?: string;
  /** The bundle's own revision, for display only: the import resets it. */
  revision?: number;
  sourceHash?: string;
  findings?: SecretFinding[];
}
/** A source project of the bundle, or the user scope (`source` "user"). */
export interface BundleScope {
  source: string;
  display?: string;
  remote?: string;
  /** The local project it maps to; absent when `reason` says why not. */
  target?: MemoryChangeProject;
  match?: "id" | "remote" | "user";
  reason?: "no_local_project" | "ambiguous" | "search_incomplete";
  items: BundleItem[];
  counts: Record<string, number>;
  withheld?: number;
}
export interface BundlePreview {
  importId: string;
  hasAF: boolean;
  scopes: BundleScope[];
  /** Source project folders left out because their name fails the scan; never named. */
  withheld?: number;
  truncated?: boolean;
  /** The search for local working copies was cut short, so an unmatched project may only be one not found. */
  incomplete?: boolean;
}
export interface BundleResult {
  scope: string;
  name: string;
  result: "imported" | "skipped";
  reason?: string;
  commit?: string;
}

/** Where the Agent memory switch's reconciler is (`auto-sync`, #1972). */
/** "unreadable": the Agent's state file cannot be read, so the phase is unknown and native memory stays off. */
export type AutoSyncPhase = "off" | "importing" | "on" | "writing_back" | "unreadable";

export interface AutoSyncProject {
  id: string;
  display?: string;
  /** import: imported, updated, unchanged, conflict, secret, invalid, forgotten, skipped; write-back: written, updated, removed, unchanged, conflict, secret, skipped */
  counts: Record<string, number>;
}

/** One file a run did not apply. Names passed the scan; a secret row carries rule names only. */
export interface AutoSyncRow {
  project: string;
  name: string;
  status: string;
  reason?: string;
  rules?: string[];
}

export interface AutoSyncRun {
  kind: "import" | "write_back" | "catch_up";
  started: string;
  finished?: string;
  /** The kind of failure that stopped the run. */
  error?: string;
  complete: boolean;
  projects: AutoSyncProject[];
  unmapped?: number;
  noRoot?: number;
  rows?: AutoSyncRow[];
  rowsCut?: number;
}

export interface AutoSyncStatus {
  phase: AutoSyncPhase;
  want: boolean;
  /** Phase and switch agree: nothing is running. */
  settled: boolean;
  since?: string;
  failing?: boolean;
  attempts?: number;
  nextAttempt?: string;
  catchUpPending?: number;
  nativeMemoryOff: boolean;
  nativeBackOn?: boolean;
  /** What the Agent could not do by itself: state_unreadable, state_not_saved, codex_flag. */
  problems?: string[];
  run?: AutoSyncRun;
}

/** What flipping the switch would do (`auto-sync/preview?to=`). */
export interface AutoSyncPreview {
  to: "on" | "off";
  projects: AutoSyncProject[];
  totals: Record<string, number>;
  /** What a run would write: import new + update, write-back new + update + remove. */
  apply: number;
  unmapped?: number;
  noRoot?: number;
}

/** The background memory review's status (ADR 0117): counts and states, never text. `pending` is
 *  the queue length, stale proposals included. */
export interface ReviewStatus {
  mode: string;
  enabled: boolean;
  pending: number;
  releasePending?: number;
}

/** The live memory a proposal is about. */
export interface ProposalCurrent {
  revision: number;
  type?: string;
  description?: string;
  body?: string;
  withheld?: boolean;
}

/** One proposal of the background review waiting for the member's decision. `withheld` means it
 *  matches a secret rule now: its name, text and diff are not sent, and it can still be rejected.
 *  `stale` proposals cannot be approved: `staleReason` says why (changed | forgotten | secret |
 *  invalid). `diff` is a unified diff from the live text to the proposed one. */
export interface MemoryProposal {
  id: string;
  runId: string;
  session: string;
  op: "create" | "update" | "forget" | string;
  scope: string;
  projectId?: string;
  project?: string;
  name: string;
  type?: string;
  description?: string;
  body?: string;
  reason: string;
  evidence: number[];
  baseRevision?: number;
  created: string;
  withheld?: boolean;
  state: "pending" | "stale" | string;
  staleReason?: string;
  current?: ProposalCurrent;
  diff: string;
}

/** What "revert this run" did to one commit of the run. */
export interface RunRevertItem {
  commit: string;
  op: string;
  scope: string;
  projectId?: string;
  name: string;
  result: "reverted" | "already" | "conflict" | "failed" | string;
  reason?: "edited" | "blocked" | string;
}
export interface RunRevertReport {
  run: string;
  items: RunRevertItem[];
}
