// Work item inbox — pure read helpers (docs/log/80 / ADR 0061).
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
      items: (d.items as unknown[]).map(normalizeItem),
      queries: d.queries as WorkItemQuery[],
      sessions: Array.isArray(d.sessions) ? (d.sessions as WorkItemSessionRef[]) : [],
      fetchedAt: typeof d.fetchedAt === "string" ? d.fetchedAt : "",
      running: !!d.running,
    },
  };
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** Make one row safe to render.
 *
 * This exists because of a real white screen: Go marshals a nil slice as JSON `null`,
 * the CP's DTO did that for a ticket with no labels, and `item.labels.slice(0, 2)` in the
 * row took the WHOLE Console down — there is no error boundary, so one null field is not
 * a broken section, it is a blank app. The producers were fixed too; this is the boundary
 * that makes the rail survive the next one (including an older CP still sending null). */
function normalizeItem(raw: unknown): WorkItem {
  const r = (raw || {}) as Partial<WorkItem> & Record<string, unknown>;
  return {
    id: str(r.id),
    queryId: str(r.queryId),
    provider: str(r.provider),
    kind: str(r.kind),
    key: str(r.key),
    title: str(r.title),
    state: str(r.state),
    url: str(r.url),
    assignee: str(r.assignee),
    labels: Array.isArray(r.labels) ? r.labels.filter((l): l is string => typeof l === "string") : [],
    repo: str(r.repo),
    updatedAt: str(r.updatedAt),
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

/** One row per ticket (docs/log/80 §80.20).
 *
 * Duplicate rows come from overlapping queries: saving the same JQL twice, or keeping both
 * `assignee = currentUser()` and `project = G3M`. Both are ordinary usage, and they turned a
 * 41-item list into 82 rows of the same tickets. The rail is a shelf of the user's work items,
 * not a listing of query results, so the same identifier is one row no matter how many queries
 * matched it.
 *
 * Which row survives is decided deterministically (per provider+key: still open, then most
 * recently updated, then lowest queryId). The CP's `ORDER BY updated_at DESC, item_key` leaves
 * ties unordered, so without this the winning row — and with it the repoHint used as the launch
 * target — would change from fetch to fetch. Rows with an empty key are never merged: better to
 * show something twice than to drop what may not be the same item. */
export function dedupeWorkItems(items: WorkItem[]): WorkItem[] {
  const idOf = (it: WorkItem) => `${it.provider}\u0000${it.key}`;
  const rank = (s: string) => (s === "done" ? 1 : 0);
  const best = new Map<string, number>();
  items.forEach((it, i) => {
    if (!it.key) return;
    const cur = best.get(idOf(it));
    if (cur === undefined || better(it, items[cur], rank)) best.set(idOf(it), i);
  });
  return items.filter((it, i) => !it.key || best.get(idOf(it)) === i);
}

function better(a: WorkItem, b: WorkItem, rank: (s: string) => number): boolean {
  if (rank(a.state) !== rank(b.state)) return rank(a.state) < rank(b.state);
  if (a.updatedAt !== b.updatedAt) return a.updatedAt > b.updatedAt;
  return a.queryId < b.queryId;
}

/** How many rows the rail draws before folding, and the threshold above which the
 * one-line filter appears (docs/log/80 §80.18.4).
 *
 * The fold is a DISPLAY decision, not a fetch one: the stopped rail draws the CP cache
 * and cannot go and get "more" — going would mean waking the Workspace to render a list,
 * which ADR 0061 decision 1 forbids. So the payload stays whole and the section folds. */
export const RAIL_VISIBLE = 10;

/** Which meta fields carry no information for a given query's rows (docs/log/80 §80.18.2).
 *
 * The bug this fixes: a Jira query of `assignee = currentUser()` put the SAME
 * `@display name` on all 41 rows — 41 second lines that say one thing, paid for by
 * ellipsising the titles. The rule is not "hide me", it is **a value every row shares is
 * not row information**; that also covers a team query where 40 rows happen to be one
 * colleague, and needs nothing from the server (the stopped rail has no idea who "me" is).
 *
 * Scoped per query: two saved queries in one rail must not cancel each other's meta out.
 * A query with a single row is left alone (one row cannot be repetitive). */
export function uniformMeta(items: WorkItem[]): Record<string, { repo: boolean; assignee: boolean }> {
  const seen: Record<string, { repo: Set<string>; assignee: Set<string>; n: number }> = {};
  for (const it of items) {
    const g = (seen[it.queryId] ||= { repo: new Set(), assignee: new Set(), n: 0 });
    g.n++;
    if (it.repo) g.repo.add(it.repo);
    if (it.assignee) g.assignee.add(it.assignee);
  }
  const out: Record<string, { repo: boolean; assignee: boolean }> = {};
  for (const [q, g] of Object.entries(seen)) {
    out[q] = { repo: g.n > 1 && g.repo.size <= 1, assignee: g.n > 1 && g.assignee.size <= 1 };
  }
  return out;
}

/** Rail filter: a substring search over what the row is ABOUT (docs/log/80 §80.18.4).
 *
 * Not a query. It never reaches the provider, is never saved, has no operators and does
 * not reorder — it only helps the eye find one row among 41. Assignee and repo are matched
 * even when `uniformMeta` hid them from the row: what was dropped is the rendering, not
 * the data. */
export function matchWorkItem(item: WorkItem, needle: string): boolean {
  const q = needle.trim().toLowerCase();
  if (!q) return true;
  const hay = [item.key, item.title, item.assignee, item.repo, ...item.labels].join(" ").toLowerCase();
  return q.split(/\s+/).every((w) => hay.includes(w));
}

/** Relative "last updated" for the head line (docs/log/80 §80.18.2). Gives the sort order a
 * visible reason and makes a three-month-old ticket obvious; the absolute stamp stays in
 * the row's tooltip. Coarse on purpose — this is 4 characters at the end of a rail row,
 * not a timestamp. */
export function relTime(iso: string, now = Date.now()): string {
  if (!iso) return "";
  const ms = new Date(iso).getTime();
  if (Number.isNaN(ms)) return "";
  const sec = Math.max(0, Math.round((now - ms) / 1000));
  if (sec < 60) return t("wi.rel_now");
  const min = Math.round(sec / 60);
  if (min < 60) return t("wi.rel_min", { n: min });
  const hour = Math.round(min / 60);
  if (hour < 24) return t("wi.rel_hour", { n: hour });
  const day = Math.round(hour / 24);
  if (day < 7) return t("wi.rel_day", { n: day });
  if (day < 30) return t("wi.rel_week", { n: Math.round(day / 7) });
  if (day < 365) return t("wi.rel_month", { n: Math.round(day / 30) });
  return t("wi.rel_year", { n: Math.floor(day / 365) });
}

/** A row is "stale" once it has not moved for a day. */
const RAIL_STALE_MS = 24 * 3600_000;

/** What the row's right edge says about age — "" for anything touched today.
 *
 * Measured, not assumed (docs/log/80 §80.18.2): at the default rail width the title gets
 * ~130px and this chip takes 38px of it. Spending a quarter of the title to say "3 hours ago"
 * on the freshest rows re-commits the very sin this pass is fixing — and the list is
 * already sorted newest-first, so for the top rows the position says it. What position
 * canNOT say is "this one has been sitting for three months", so the chip appears exactly
 * there. Same rule as the meta line: shown when it carries information, gone when it does
 * not. The exact stamp stays in the tooltip either way. */
export function railWhen(iso: string, now = Date.now()): string {
  if (!iso) return "";
  const ms = new Date(iso).getTime();
  if (Number.isNaN(ms) || now - ms < RAIL_STALE_MS) return "";
  return relTime(iso, now);
}

/** Full local stamp for the row's tooltip and the detail modal — `relTime` is deliberately
 * coarse, so the exact value has to stay reachable somewhere.
 *
 * Seconds are dropped: a bare `toLocaleString()` writes "10:49:02 PM", but seconds carry no
 * meaning for a ticket's update time and the detail modal renders this as text to read, so they
 * only add digits (checked against the real rendering). */
export function fullLocal(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
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

/** Compact local stamp for the "last fetched" line. "" when never fetched, so the caller can say
 * "not fetched yet" instead of rendering an empty clock. */
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
 * "issue-" — "#" cannot appear in a git ref. The key keeps the case it was written in:
 * G3-1234 is how the ticket, the commit message and the PR title all spell it, so
 * lower-casing it to g3-1234 only made the branch the odd one out. `{slug}` is the ASCII
 * slug of the title and is available, but NOT in the default: the key already identifies
 * the work, the title is often non-ASCII (so the slug is empty anyway), and a long
 * English title made names like feature/issue-45-empty-list-after-login-when. Add {slug}
 * in the setting if you want it. */
export const DEFAULT_BRANCH_TEMPLATE = "feature/{key}";

/** Branch name for a work item, from the user's template (docs/log/80 P2).
 *
 * A template that yields something git would refuse is worse than no template, so the
 * result is sanitised: only [A-Za-z0-9._/-] survives, empty path segments collapse, and
 * an empty `{slug}` (every Japanese title) must not leave a trailing separator behind —
 * "feature/issue-45-" is not a name anyone typed on purpose. */
export function branchForItem(item: { key: string; title: string }, template?: string): string {
  const key = shortKey(item.key).replace(/^#/, "issue-").replace(/[^A-Za-z0-9._-]+/g, "-");
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
  // git refuses a trailing ".lock", "..", and "@{". These barely survive the steps above, but
  // the template is free text, so strip them anyway.
  return cleaned.replace(/\.\.+/g, ".").replace(/\.lock$/i, "");
}

/** The first prompt (docs/log/80 §80.9).
 *
 * The body is NOT included by default. A ticket description is written by third
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

// How to read the body differs per provider, so each line names a different tool. af never
// carries the body itself, so it must always say where the body is; without that the agent
// starts working from the title alone.
function readLine(item: WorkItem): string {
  switch (item.provider) {
    case "github":
      return t("wi.prompt_read_github", { key: shortKey(item.key).replace("#", "") });
    case "jira":
      return t("wi.prompt_read_jira");
    default:
      // Bitbucket lands here. There is no in-session tool equivalent to `gh`, so the URL is all
      // this can point at (docs/log/80 §80.19.5). The point is not to name a tool that is absent.
      return t("wi.prompt_read_generic");
  }
}

/** Whether af can post the report comment back to this item's tracker.
 *
 * No control is offered for a provider that lacks the capability. Bitbucket was added read-only
 * (posting needs `pullrequest:write`, which means the tenant admin re-registering the connection
 * and everyone re-authorising — docs/log/80 §80.19.3), so a comment button on such a row would
 * always be refused once pressed. */
export function canComment(item: WorkItem): boolean {
  return item.provider !== "bitbucket";
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
