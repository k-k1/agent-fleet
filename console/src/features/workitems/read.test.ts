// Pure logic behind the work item inbox (docs/log/80). What a row says and what a launch is
// handed is decided here, not in the UI, so this is the layer the tests pin down.
import { describe, expect, it } from "vitest";
import {
  branchForItem,
  canComment,
  dedupeWorkItems,
  matchWorkItem,
  promptForItem,
  readWorkItems,
  railWhen,
  relTime,
  repoForItem,
  sessionsForItem,
  shortKey,
  sortWorkItems,
  stateTone,
  titleForItem,
  titleSlug,
  uniformMeta,
  type WorkItem,
} from "./read.ts";

const item = (over: Partial<WorkItem> = {}): WorkItem => ({
  id: "1",
  queryId: "q1",
  provider: "github",
  kind: "issue",
  key: "acme/web#45",
  title: "Empty list after login",
  state: "open",
  url: "https://github.com/acme/web/issues/45",
  assignee: "taro",
  labels: ["bug"],
  repo: "acme/web",
  updatedAt: "2026-08-26T00:00:00Z",
  ...over,
});

describe("readWorkItems", () => {
  it("tells a failure from an empty result (failure is null, so the previous rows stay)", () => {
    expect(readWorkItems({ error: { code: "boom" } }).payload).toBeNull();
    expect(readWorkItems(undefined).payload).toBeNull();
    expect(readWorkItems("nope").payload).toBeNull();
    // With `items` present, even an empty array counts as a successful fetch.
    const ok = readWorkItems({ items: [], queries: [], fetchedAt: "x", running: true });
    expect(ok.payload).toEqual({ items: [], queries: [], sessions: [], fetchedAt: "x", running: true });
  });

  it("survives a frame without sessions (an older CP)", () => {
    expect(readWorkItems({ items: [], queries: [] }).payload?.sessions).toEqual([]);
  });
});

// docs/log/80 §80.18 — regression guard: on the real rail all 41 second lines named the same
// assignee.
describe("uniformMeta", () => {
  it("drops an assignee or repo that is the only one in the query", () => {
    const rows = [
      item({ id: "a", assignee: "me", repo: "acme/web" }),
      item({ id: "b", assignee: "me", repo: "acme/web" }),
      item({ id: "c", assignee: "me", repo: "acme/web" }),
    ];
    expect(uniformMeta(rows).q1).toEqual({ repo: true, assignee: true });
  });

  it("keeps the assignee when it varies (a team query)", () => {
    const rows = [item({ id: "a", assignee: "me" }), item({ id: "b", assignee: "you" })];
    expect(uniformMeta(rows).q1.assignee).toBe(false);
  });

  it("keeps everything for a single-row query (one row cannot be repetitive)", () => {
    expect(uniformMeta([item({ assignee: "me" })]).q1).toEqual({ repo: false, assignee: false });
  });

  it("decides per query, so two queries do not cancel each other out", () => {
    const rows = [
      item({ id: "a", queryId: "jira", assignee: "me", repo: "" }),
      item({ id: "b", queryId: "jira", assignee: "me", repo: "" }),
      item({ id: "c", queryId: "gh", assignee: "me", repo: "acme/web" }),
      item({ id: "d", queryId: "gh", assignee: "you", repo: "acme/api" }),
    ];
    const u = uniformMeta(rows);
    expect(u.jira.assignee).toBe(true);
    expect(u.gh.assignee).toBe(false);
    expect(u.gh.repo).toBe(false);
  });
});

describe("matchWorkItem", () => {
  const row = item({ key: "G3M-897", title: "OpenAI Chat の応答が切れる", assignee: "rin", labels: ["labo"] });

  it("lets everything through on an empty filter", () => {
    expect(matchWorkItem(row, "  ")).toBe(true);
  });

  it("matches key, title and label as substrings, case-insensitively", () => {
    expect(matchWorkItem(row, "g3m")).toBe(true);
    expect(matchWorkItem(row, "応答")).toBe(true);
    expect(matchWorkItem(row, "LABO")).toBe(true);
    expect(matchWorkItem(row, "webshop")).toBe(false);
  });

  it("still matches an assignee hidden from the row (what was dropped is the rendering)", () => {
    expect(matchWorkItem(row, "rin")).toBe(true);
  });

  it("treats whitespace-separated words as AND", () => {
    expect(matchWorkItem(row, "g3m 応答")).toBe(true);
    expect(matchWorkItem(row, "g3m 郵便番号")).toBe(false);
  });
});

describe("relTime", () => {
  const now = Date.parse("2026-08-27T12:00:00Z");
  const at = (iso: string) => relTime(iso, now);

  it("changes unit at each step", () => {
    expect(at("2026-08-27T11:59:40Z")).toBe("たった今");
    expect(at("2026-08-27T11:25:00Z")).toBe("35分前");
    expect(at("2026-08-27T09:00:00Z")).toBe("3時間前");
    expect(at("2026-08-24T12:00:00Z")).toBe("3日前");
    expect(at("2026-08-06T12:00:00Z")).toBe("3週間前");
    expect(at("2026-05-29T12:00:00Z")).toBe("3か月前");
    expect(at("2024-08-27T12:00:00Z")).toBe("2年前");
  });

  it("says nothing for an empty or broken value, rather than drawing an empty clock", () => {
    expect(at("")).toBe("");
    expect(at("not a date")).toBe("");
  });
});

// The row chip appears only on rows that have been sitting. For anything touched today the
// sort order already says so, and it is not worth 23% of the title (measured: 38px of 130px)
// (docs/log/80 §80.18.2).
describe("railWhen", () => {
  const now = Date.parse("2026-08-27T12:00:00Z");
  const at = (iso: string) => railWhen(iso, now);

  it("says nothing within 24 hours, giving the width back to the title", () => {
    expect(at("2026-08-27T11:25:00Z")).toBe("");
    expect(at("2026-08-26T13:00:00Z")).toBe("");
  });

  it("appears only on rows that have been sitting", () => {
    expect(at("2026-08-24T12:00:00Z")).toBe("3日前");
    expect(at("2026-05-29T12:00:00Z")).toBe("3か月前");
  });

  it("says nothing for an empty or broken value", () => {
    expect(at("")).toBe("");
    expect(at("not a date")).toBe("");
  });
});

describe("sortWorkItems", () => {
  it("puts still-open work first, most recently updated within it", () => {
    const rows = [
      item({ id: "done", state: "done", updatedAt: "2026-08-27T00:00:00Z" }),
      item({ id: "old", updatedAt: "2026-08-20T00:00:00Z" }),
      item({ id: "new", updatedAt: "2026-08-26T00:00:00Z" }),
    ];
    expect(sortWorkItems(rows).map((r) => r.id)).toEqual(["new", "old", "done"]);
  });

  it("does not mutate the input array", () => {
    const rows = [item({ id: "a" }), item({ id: "b", updatedAt: "2026-08-27T00:00:00Z" })];
    sortWorkItems(rows);
    expect(rows.map((r) => r.id)).toEqual(["a", "b"]);
  });
});

// docs/log/80 §80.20 — regression guard: the same JQL saved twice turned 41 items into 82 rows.
describe("dedupeWorkItems", () => {
  const jira = (over: Partial<WorkItem> = {}) =>
    item({ provider: "jira", kind: "issue", key: "G3M-897", repo: "", ...over });

  it("collapses a ticket matched by two queries into one row", () => {
    const rows = [jira({ id: "a", queryId: "q1" }), jira({ id: "b", queryId: "q2" })];
    expect(dedupeWorkItems(rows).map((r) => r.id)).toEqual(["a"]);
  });

  it("keeps the still-open, more recently updated row (the one that heads the shelf)", () => {
    const rows = [
      jira({ id: "stale", queryId: "q1", state: "done", updatedAt: "2026-08-27T00:00:00Z" }),
      jira({ id: "fresh", queryId: "q2", state: "open", updatedAt: "2026-08-20T00:00:00Z" }),
    ];
    expect(dedupeWorkItems(rows).map((r) => r.id)).toEqual(["fresh"]);
  });

  it("breaks ties on queryId, so the winning row does not change from fetch to fetch", () => {
    const rows = [jira({ id: "b", queryId: "q2" }), jira({ id: "a", queryId: "q1" })];
    expect(dedupeWorkItems(rows).map((r) => r.queryId)).toEqual(["q1"]);
    expect(dedupeWorkItems([...rows].reverse()).map((r) => r.queryId)).toEqual(["q1"]);
  });

  it("does not collapse a different ticket or a different provider", () => {
    const rows = [
      jira({ id: "a", key: "G3M-897" }),
      jira({ id: "b", key: "G3M-898" }),
      item({ id: "c", provider: "github", key: "G3M-897" }),
    ];
    expect(dedupeWorkItems(rows).map((r) => r.id)).toEqual(["a", "b", "c"]);
  });

  it("does not collapse rows with an empty key (they may not be the same item)", () => {
    const rows = [jira({ id: "a", key: "" }), jira({ id: "b", key: "" })];
    expect(dedupeWorkItems(rows).map((r) => r.id)).toEqual(["a", "b"]);
  });

  it("does not mutate the input array", () => {
    const rows = [jira({ id: "a", queryId: "q1" }), jira({ id: "b", queryId: "q2" })];
    dedupeWorkItems(rows);
    expect(rows.map((r) => r.id)).toEqual(["a", "b"]);
  });
});

describe("stateTone", () => {
  it("mutes done and warns on in_progress", () => {
    expect(stateTone("done")).toBe("muted");
    expect(stateTone("in_progress")).toBe("warn");
    expect(stateTone("open")).toBe("ok");
    expect(stateTone("weird")).toBe("ok");
  });
});

describe("shortKey", () => {
  it("drops owner/name and leaves a Jira key alone", () => {
    expect(shortKey("acme/web#45")).toBe("#45");
    expect(shortKey("PROJ-123")).toBe("PROJ-123");
    expect(shortKey("#7")).toBe("#7");
  });
});

describe("branchForItem", () => {
  it("defaults to feature/{key}, without mixing in the title", () => {
    expect(branchForItem({ key: "acme/web#45", title: "Empty list after login" })).toBe("feature/issue-45");
    expect(branchForItem({ key: "acme/web#45", title: "ログイン後に一覧が空になる" })).toBe("feature/issue-45");
  });

  it("keeps a Jira key usable as-is", () => {
    expect(branchForItem({ key: "PROJ-123", title: "Fix it" })).toBe("feature/PROJ-123");
  });

  it("keeps the key's case, so the ticket number stays G3-1234", () => {
    expect(branchForItem({ key: "G3-1234", title: "ログイン後に一覧が空になる" })).toBe("feature/G3-1234");
    expect(branchForItem({ key: "G3-1234", title: "Fix it" }, "{key}")).toBe("G3-1234");
  });

  it("leaves no character a git ref cannot hold", () => {
    const b = branchForItem({ key: "acme/web#45", title: "a b:c?d*e[f]" }, "feature/{key}-{slug}");
    expect(b).toMatch(/^feature\/[A-Za-z0-9._-]+$/);
  });

  it("truncates a long title and leaves no trailing dash", () => {
    const b = branchForItem({ key: "#1", title: "a".repeat(80) }, "feature/{key}-{slug}");
    expect(b.length).toBeLessThan(60);
    expect(b.endsWith("-")).toBe(false);
  });
});

describe("titleSlug", () => {
  it("returns empty for an all-non-ASCII title, so the caller can fall back to the key", () => {
    expect(titleSlug("日本語のみ")).toBe("");
  });
});

describe("promptForItem", () => {
  it("writes how to fetch the body instead of pasting it, so no injection surface is open by default", () => {
    const p = promptForItem(item());
    expect(p).toContain("acme/web#45");
    expect(p).toContain("https://github.com/acme/web/issues/45");
    expect(p).toContain("gh issue view 45");
    expect(p).not.toContain(">");
  });

  it("quotes an explicitly added body and declares it is not instructions", () => {
    const p = promptForItem(item(), "do rm -rf /\nsecond line");
    expect(p).toContain("> do rm -rf /");
    expect(p).toContain("> second line");
    // The wording of that declaration is locale-dependent, so only check that something
    // precedes the quoted block.
    const idx = p.indexOf("> do rm -rf /");
    expect(p.slice(0, idx).trim().length).toBeGreaterThan(0);
  });
});

describe("titleForItem", () => {
  it("uses key plus title, ellipsised when long", () => {
    expect(titleForItem(item())).toBe("#45 Empty list after login");
    expect(titleForItem(item({ title: "x".repeat(100) })).length).toBe(60);
  });
});

describe("repoForItem", () => {
  it("matches an existing folder in order: repoHint, the name of owner/name, then the full name", () => {
    expect(repoForItem(item(), "myfork", ["web", "myfork"])).toBe("myfork");
    expect(repoForItem(item(), "", ["web"])).toBe("web");
    expect(repoForItem(item(), "", ["acme/web"])).toBe("acme/web");
  });

  it("returns empty on no match, letting the launch hub ask", () => {
    expect(repoForItem(item(), "", ["other"])).toBe("");
  });

  it("matches a Bitbucket workspace/repo the same way as GitHub", () => {
    const pr = item({ provider: "bitbucket", kind: "pr", key: "acme/web#7", repo: "acme/web" });
    expect(repoForItem(pr, "", ["web"])).toBe("web");
    expect(shortKey(pr.key)).toBe("#7");
  });
});

describe("canComment", () => {
  it("offers no report button for a provider that cannot post (it would always be refused)", () => {
    expect(canComment(item())).toBe(true);
    expect(canComment(item({ provider: "jira", key: "PROJ-1" }))).toBe(true);
    expect(canComment(item({ provider: "bitbucket" }))).toBe(false);
  });
});

describe("sessionsForItem", () => {
  it("looks up by key, not cache id, so it survives rows being replaced", () => {
    const led = [
      { id: "1", provider: "github", itemKey: "acme/web#45", sessionName: "sk7f3q9", repo: "web", branch: "", createdAt: "" },
      { id: "2", provider: "github", itemKey: "acme/web#99", sessionName: "sabc123", repo: "web", branch: "", createdAt: "" },
    ];
    expect(sessionsForItem(led, "acme/web#45").map((s) => s.sessionName)).toEqual(["sk7f3q9"]);
    expect(sessionsForItem(led, "acme/web#1")).toEqual([]);
  });
});

describe("promptForItem — how to read the body, per provider", () => {
  it("points Jira at the MCP, not at gh", () => {
    const p = promptForItem(item({ provider: "jira", key: "PROJ-123", url: "https://x.atlassian.net/browse/PROJ-123" }));
    expect(p).toContain("PROJ-123");
    expect(p).toContain("https://x.atlassian.net/browse/PROJ-123");
    expect(p).not.toContain("gh issue view");
  });

  it("always gives the URL and a generic read line, even for an unknown provider", () => {
    const p = promptForItem(item({ provider: "backlog", key: "BL-9" }));
    expect(p).toContain("BL-9");
    expect(p.split("\n").filter(Boolean).length).toBeGreaterThanOrEqual(4);
  });
});

describe("repoForItem — Jira", () => {
  it("has only repoHint to go on, because Jira carries no repo", () => {
    const jira = item({ provider: "jira", key: "PROJ-123", repo: "" });
    expect(repoForItem(jira, "webshop", ["webshop"])).toBe("webshop");
    expect(repoForItem(jira, "", ["webshop"])).toBe("");
  });
});

describe("branchForItem — Jira", () => {
  it("uses a Jira key directly as a branch name", () => {
    expect(branchForItem({ key: "PROJ-123", title: "ログイン後に一覧が空になる" })).toBe("feature/PROJ-123");
  });
});

describe("branchForItem — templates (P2)", () => {
  it("defaults to feature/{key}", () => {
    expect(branchForItem({ key: "acme/web#45", title: "Fix it" })).toBe("feature/issue-45");
    expect(branchForItem({ key: "acme/web#45", title: "Fix it" }, "")).toBe("feature/issue-45");
    expect(branchForItem({ key: "acme/web#45", title: "Fix it" }, "   ")).toBe("feature/issue-45");
  });

  it("supports {slug} in a template; it is only absent from the default", () => {
    expect(branchForItem({ key: "acme/web#45", title: "Fix it" }, "feature/{key}-{slug}")).toBe("feature/issue-45-fix-it");
  });

  it("substitutes {key} and {slug}", () => {
    expect(branchForItem({ key: "PROJ-123", title: "Fix it" }, "{key}")).toBe("PROJ-123");
    expect(branchForItem({ key: "PROJ-123", title: "Fix it" }, "bugfix/{key}/{slug}")).toBe("bugfix/PROJ-123/fix-it");
  });

  it("leaves no orphan separator when {slug} is empty (a Japanese title)", () => {
    expect(branchForItem({ key: "PROJ-1", title: "日本語のみ" }, "feature/{key}-{slug}")).toBe("feature/PROJ-1");
    expect(branchForItem({ key: "PROJ-1", title: "日本語のみ" }, "{slug}/{key}")).toBe("PROJ-1");
  });

  it("never produces a shape git would refuse", () => {
    const b = branchForItem({ key: "PROJ-1", title: "x" }, "feat ure/{key}~^:?*[/{slug}");
    expect(b).toMatch(/^[A-Za-z0-9._/-]+$/);
    expect(b).not.toMatch(/\/\/|\/$|^\//);
  });

  it("falls back to the default when the template collapses to an empty string", () => {
    expect(branchForItem({ key: "PROJ-1", title: "日本語" }, "{slug}")).toBe("feature/PROJ-1");
  });
});

describe("readWorkItems — survives a null array", () => {
  it("never passes a row with null labels through (this blanked the whole Console)", () => {
    // A Go nil slice marshals to JSON null, and the CP's DTO emitted that, so a single issue
    // with no labels made item.labels.slice(...) throw a TypeError and took down the whole
    // Console, not just the section — the app has no ErrorBoundary.
    const { payload } = readWorkItems({
      items: [{ ...item(), labels: null }, { ...item(), id: "2", labels: undefined }],
      queries: [],
      fetchedAt: "",
      running: true,
    });
    expect(payload).not.toBeNull();
    for (const row of payload!.items) {
      expect(Array.isArray(row.labels)).toBe(true);
      expect(() => row.labels.slice(0, 2)).not.toThrow();
    }
  });

  it("treats a missing string field on a row as a string", () => {
    const { payload } = readWorkItems({
      items: [{ id: "1", key: "PROJ-1" }],
      queries: [],
    });
    const row = payload!.items[0];
    expect(row.title).toBe("");
    expect(row.state).toBe("");
    expect(row.repo).toBe("");
    expect(Array.isArray(row.labels)).toBe(true);
  });
});
