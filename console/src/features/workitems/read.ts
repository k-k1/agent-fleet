// Work item inbox — pure read helpers (docs/80 / ADR 0061).
//
// Kept free of React and of the API client so the parts that decide what the rail says
// (state tone, "last fetched", the seeded first prompt, the branch name) are testable on
// their own — they are the feature, the section is just their frame.
import type { ApiError } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";

export interface WorkItem {
  id: string;
  queryId: string;
  provider: string;
  /** "issue" | "pr" */
  kind: string;
  /** "owner/name#45" (GitHub) or "PROJ-123" (Jira, P1). */
  key: string;
  title: string;
  /** Normalised across providers: open / in_progress / done / other. */
  state: string;
  url: string;
  assignee: string;
  labels: string[];
  /** "owner/name" when the provider has one — seeds the launch target. */
  repo: string;
  updatedAt: string;
}

export interface WorkItemQuery {
  id: string;
  provider: string;
  label: string;
  query: string;
  repoHint: string;
  enabled: boolean;
  position: number;
  fetchedAt: string;
  lastError: string;
}

export interface WorkItemSessionRef {
  id: string;
  provider: string;
  itemKey: string;
  sessionName: string;
  repo: string;
  branch: string;
  createdAt: string;
}

export interface WorkItemPayload {
  items: WorkItem[];
  queries: WorkItemQuery[];
  sessions: WorkItemSessionRef[];
  /** Oldest enabled query's fetch stamp — the honest one to show (see the CP side). */
  fetchedAt: string;
  running: boolean;
}

const EMPTY: WorkItemPayload = { items: [], queries: [], sessions: [], fetchedAt: "", running: false };

/** Adopt one GET /api/work-items-shaped body. A CP error (or any non-object) becomes
 * `null` rather than an empty payload: "the fetch failed" and "there are no tickets"
 * must not look the same in the rail. */
export function readWorkItems(res: unknown): { payload: WorkItemPayload | null; error?: ApiError | string } {
  if (!res || typeof res !== "object") return { payload: null };
  const d = res as Partial<WorkItemPayload> & { error?: ApiError | string };
  if (d.error) return { payload: null, error: d.error };
  if (!Array.isArray(d.items) || !Array.isArray(d.queries)) return { payload: null };
  return {
    payload: {
      ...EMPTY,
      ...d,
      items: d.items as WorkItem[],
      queries: d.queries as WorkItemQuery[],
      sessions: Array.isArray(d.sessions) ? (d.sessions as WorkItemSessionRef[]) : [],
      fetchedAt: typeof d.fetchedAt === "string" ? d.fetchedAt : "",
      running: !!d.running,
    },
  };
}

/** Rail sort: still-open work first, then most recently updated. A done row only ever
 * appears because the user's query asks for it, so it goes to the bottom rather than
 * being hidden (hiding it would silently contradict their query). */
export function sortWorkItems(items: WorkItem[]): WorkItem[] {
  const rank = (s: string) => (s === "done" ? 1 : 0);
  return [...items].sort((a, b) => {
    const r = rank(a.state) - rank(b.state);
    if (r) return r;
    if (a.updatedAt !== b.updatedAt) return a.updatedAt < b.updatedAt ? 1 : -1;
    return a.key.localeCompare(b.key);
  });
}

/** State → the rail's existing tone vocabulary (same words the session chips use). */
export function stateTone(state: string): "ok" | "warn" | "muted" {
  switch (state) {
    case "done":
      return "muted";
    case "in_progress":
      return "warn";
    default:
      return "ok";
  }
}

export function stateLabel(state: string): string {
  switch (state) {
    case "done":
      return t("wi.state_done");
    case "in_progress":
      return t("wi.state_in_progress");
    case "open":
      return t("wi.state_open");
    default:
      return t("wi.state_other");
  }
}

/** "owner/name#45" → "#45"; a Jira key is already short and stays as-is. Rows are one
 * line, so the repo is shown once per group rather than on every key. */
export function shortKey(key: string): string {
  const i = key.indexOf("#");
  return i > 0 ? key.slice(i) : key;
}

/** Compact local stamp for 最終取得. "" when never fetched, so the caller can say
 * 「まだ取得していません」 instead of rendering an empty clock. */
export function shortLocal(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

/** Slug for a branch name: ASCII words from the title, dashed, short. Returns "" when the
 * title is all non-ASCII (Japanese titles are the normal case here) — the caller then
 * falls back to the key alone rather than emitting a branch of empty dashes. */
export function titleSlug(title: string, max = 32): string {
  const s = title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s.length > max ? s.slice(0, max).replace(/-+$/, "") : s;
}

/** The default branch template. `{key}` is the item key with the owner/name prefix
 * dropped (the working copy already says which repo it is) and "#" turned into
 * "issue-" — "#" cannot appear in a git ref. `{slug}` is the ASCII slug of the title and
 * is available, but NOT in the default: the key already identifies the work, the title
 * is often non-ASCII (so the slug is empty anyway), and a long English title made names
 * like feature/issue-45-empty-list-after-login-when. Add {slug} in the setting if you
 * want it. */
export const DEFAULT_BRANCH_TEMPLATE = "feature/{key}";

/** Branch name for a work item, from the user's template (docs/80 P2).
 *
 * ⚠️ A template that yields something git would refuse is worse than no template, so the
 * result is sanitised: only [A-Za-z0-9._/-] survives, empty path segments collapse, and
 * an empty `{slug}` (every Japanese title) must not leave a trailing separator behind —
 * "feature/issue-45-" is not a name anyone typed on purpose. */
export function branchForItem(item: { key: string; title: string }, template?: string): string {
  const key = shortKey(item.key).replace(/^#/, "issue-").replace(/[^A-Za-z0-9._-]+/g, "-").toLowerCase();
  const slug = titleSlug(item.title);
  const raw = (template && template.trim() ? template.trim() : DEFAULT_BRANCH_TEMPLATE)
    .replace(/\{key\}/g, key)
    .replace(/\{slug\}/g, slug);
  return sanitizeBranch(raw) || `feature/${key}`;
}

/** Trim a rendered template into something git accepts. Kept separate so the settings
 * field can show the user what their template actually produces. */
export function sanitizeBranch(raw: string): string {
  const cleaned = raw
    .replace(/[^A-Za-z0-9._/-]+/g, "-")
    .split("/")
    .map((seg) => seg.replace(/[-.]+$/g, "").replace(/^[-.]+/g, ""))
    .filter(Boolean)
    .join("/");
  // git は末尾 ".lock"・".." ・"@{" を拒む。ここまで来る形ではまず出ないが、テンプレート
  // は自由入力なので落としておく。
  return cleaned.replace(/\.\.+/g, ".").replace(/\.lock$/i, "");
}

/** The first prompt (docs/80 §80.9).
 *
 * ★ The body is NOT included by default. A ticket description is written by third
 * parties, so pasting it in by default is opening an injection path by default; instead
 * we say where it is and let the agent fetch it with `gh` / the Jira MCP. `withBody`
 * (the dialog's opt-in checkbox) wraps whatever the caller managed to fetch in a quoted
 * block that states plainly it is data, not instructions. */
export function promptForItem(item: WorkItem, body?: string): string {
  const lines = [
    t("wi.prompt_target", { key: item.key, title: item.title }),
    t("wi.prompt_url", { url: item.url }),
    "",
    readLine(item),
    t("wi.prompt_investigate"),
  ];
  if (body && body.trim()) {
    lines.push("", t("wi.prompt_body_notice"), "", ...body.trim().split("\n").map((l) => `> ${l}`));
  }
  return lines.join("\n");
}

// 本文の読み方は provider ごとに違う道具を指す。★ af が本文を運ばない代わりに、
// 「どこにあるか」は必ず書く —— これが無いと、エージェントはタイトルだけで作業を始める。
function readLine(item: WorkItem): string {
  switch (item.provider) {
    case "github":
      return t("wi.prompt_read_github", { key: shortKey(item.key).replace("#", "") });
    case "jira":
      return t("wi.prompt_read_jira");
    default:
      return t("wi.prompt_read_generic");
  }
}

/** Session title suggestion: the key plus a trimmed title, so the rail and the session
 * list name the same thing. */
export function titleForItem(item: WorkItem, max = 60): string {
  const head = `${shortKey(item.key)} ${item.title}`.trim();
  return head.length > max ? head.slice(0, max - 1) + "…" : head;
}

/** Which local working copy an item should launch in. GitHub gives "owner/name"; the
 * folder is usually just "name", and a query's repoHint wins when the user set one. */
export function repoForItem(item: WorkItem, hint: string, folders: string[]): string {
  const candidates = [hint, item.repo.split("/")[1] || "", item.repo].filter(Boolean);
  for (const c of candidates) {
    if (folders.includes(c)) return c;
  }
  return "";
}

/** Ledger lookup: the sessions already started for an item key. */
export function sessionsForItem(sessions: WorkItemSessionRef[], key: string): WorkItemSessionRef[] {
  return sessions.filter((s) => s.itemKey === key);
}
