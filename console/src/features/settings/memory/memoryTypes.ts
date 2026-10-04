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
  latest: boolean;
  live: boolean;
  /** The history holds the memory's text on at least one side of this change. A forget of a
   *  file that was never committed has none, so there is nothing to bring back. */
  revertible: boolean;
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
  shortened?: boolean;
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
