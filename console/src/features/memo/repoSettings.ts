// Pure logic of the repository settings card (#4881): reading what the Control Plane answers for
// a category's .agent-fleet/autorun.yaml, the rows of the difference, and the words for a value.
// The Control Plane decides every difference and every refusal; this only lays them out.
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { MemoRepoDiff, MemoRepoExport, MemoRepoIssue, MemoRepoSettings } from "../../types/memo.ts";

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const int = (v: unknown): number => (typeof v === "number" && Number.isInteger(v) && v > 0 ? v : 0);

function readIssues(v: unknown): MemoRepoIssue[] {
  if (!Array.isArray(v)) return [];
  return v.filter(isObj).map((x) => ({ line: int(x.line), key: str(x.key), reason: str(x.reason) }));
}

function readDiffs(v: unknown): MemoRepoDiff[] {
  if (!Array.isArray(v)) return [];
  return v
    .filter((x): x is Record<string, unknown> => isObj(x) && typeof x.key === "string" && x.key !== "")
    .map((x) => ({
      key: x.key as string,
      field: str(x.field),
      line: int(x.line),
      current: x.current,
      file: x.file,
      raise: x.raise === true,
    }));
}

/** GET .../repo-settings, normalised. null = an error body or an answer this Console cannot read. */
export function readRepoSettings(raw: unknown): MemoRepoSettings | null {
  if (!isObj(raw) || raw.error || typeof raw.categoryId !== "string" || typeof raw.commit !== "string") return null;
  const errors = readIssues(raw.errors);
  return {
    categoryId: raw.categoryId,
    repo: str(raw.repo),
    category: str(raw.category),
    path: str(raw.path),
    remote: str(raw.remote),
    branch: str(raw.branch),
    commit: raw.commit,
    found: raw.found === true,
    entry: raw.entry === true,
    queue: raw.queue === true,
    settingsTag: str(raw.settingsTag),
    approvedCommit: str(raw.approvedCommit),
    approvedAt: str(raw.approvedAt),
    errors,
    warnings: readIssues(raw.warnings),
    // The Control Plane never offers a difference beside an error; a reader that did would let
    // the member approve values from a file that cannot be applied.
    diffs: errors.length > 0 ? [] : readDiffs(raw.diffs),
  };
}

/** GET .../repo-settings/export, normalised. null = an error body. */
export function readRepoExport(raw: unknown): MemoRepoExport | null {
  if (!isObj(raw) || raw.error || typeof raw.yaml !== "string" || raw.yaml === "") return null;
  return { path: str(raw.path), category: str(raw.category), yaml: raw.yaml, omitted: readIssues(raw.omitted) };
}

/** The settings that decide when a category runs by itself. They change as one, so the card
 *  shows them as one row and the approval sends all of them or none. */
export const REPO_FIRING_KEYS: readonly string[] = ["open_cron", "open_minutes", "tz", "open_after"];

/** One row of the difference: one setting, or the firing settings together. */
export interface RepoDiffRow {
  id: string;
  keys: string[];
  diffs: MemoRepoDiff[];
  raise: boolean;
}

export function repoDiffRows(diffs: readonly MemoRepoDiff[]): RepoDiffRow[] {
  const rows: RepoDiffRow[] = [];
  let firing: RepoDiffRow | null = null;
  for (const d of diffs) {
    if (REPO_FIRING_KEYS.includes(d.key)) {
      if (!firing) {
        firing = { id: "firing", keys: [], diffs: [], raise: false };
        rows.push(firing);
      }
      firing.keys.push(d.key);
      firing.diffs.push(d);
      firing.raise ||= d.raise;
      continue;
    }
    rows.push({ id: d.key, keys: [d.key], diffs: [d], raise: d.raise });
  }
  return rows;
}

/** The keys an approval of the chosen rows sends, in the order the difference listed them. */
export function approvedKeys(rows: readonly RepoDiffRow[], chosen: ReadonlySet<string>): string[] {
  return rows.filter((r) => chosen.has(r.id)).flatMap((r) => r.keys);
}

const KEY_LABELS: Record<string, MsgKey> = {
  agent_kind: "memo.q_agent",
  model: "memo.q_model",
  effort: "memo.q_effort",
  route: "memo.repo_k_route",
  spend_cap_usd: "memo.q_budget",
  max_active: "memo.q_max_active",
  batch: "memo.q_batch",
  open_cron: "memo.q_open_cron",
  open_minutes: "memo.q_open_minutes",
  tz: "sched.f_tz",
  open_after: "memo.q_open_after",
  max_fires_per_day: "memo.q_fires",
  max_task_hours: "memo.q_task_hours",
  worktree: "memo.q_worktree",
  tidy: "memo.q_tidy",
  continuation_max: "memo.q_continuation",
  instruction: "memo.q_instruction",
};

/** The Console's label of a file key; null for a key this Console does not know (a newer
 *  Control Plane), which the card shows as the key itself. */
export function repoKeyLabel(key: string): MsgKey | null {
  return KEY_LABELS[key] ?? null;
}

/** What a value reads as. null is "the default" (an empty string, or a spend cap of 0, which is
 *  no cap); booleans are "on" / "off" through the catalogue, so they come back as keys. */
export function repoValueText(key: string, v: unknown): { text: string } | { word: MsgKey } {
  if (typeof v === "boolean") return { word: v ? "memo.repo_on" : "memo.repo_off" };
  if (key === "spend_cap_usd" && v === 0) return { word: "memo.repo_no_cap" };
  if (v === "" || v === null || v === undefined) return { word: "memo.repo_default" };
  if (typeof v === "string") return { text: v };
  if (typeof v === "number") return { text: String(v) };
  return { text: JSON.stringify(v) };
}

/** A commit id cut to the length people read. */
export const shortCommit = (c: string): string => c.slice(0, 12);

