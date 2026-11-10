import { describe, expect, it } from "vitest";
import { liveRepoMap, mergeRepoMaps, normalizeRepoMap, withRepoEntry } from "./repoMap.ts";

describe("repoMap", () => {
  it("reads legacy string maps as entries and drops malformed ones", () => {
    expect(normalizeRepoMap({ "jira:A": "web", "jira:B": "", "jira:C": 3, "jira:D": { repo: "x", at: 5 } })).toEqual({
      "jira:A": { repo: "web", at: 0 },
      "jira:D": { repo: "x", at: 5 },
    });
    expect(normalizeRepoMap(null)).toEqual({});
    expect(normalizeRepoMap(["a"])).toEqual({});
  });

  it("merges per entry: both tabs' entries survive, the newer change wins a shared one", () => {
    const a = { "jira:A": { repo: "web", at: 1 }, "jira:S": { repo: "old", at: 1 } };
    const b = { "jira:B": { repo: "sdk", at: 2 }, "jira:S": { repo: "new", at: 2 } };
    const m = mergeRepoMaps(a, b);
    expect(liveRepoMap(m)).toEqual({ "jira:A": "web", "jira:B": "sdk", "jira:S": "new" });
    expect(mergeRepoMaps(b, a)).toEqual(m); // commutative
    expect(mergeRepoMaps(m, m)).toEqual(m); // idempotent
  });

  it("a tombstone beats an older entry, is beaten by a newer one, and wins a tie", () => {
    const entry = { "jira:A": { repo: "web", at: 5 } };
    expect(liveRepoMap(mergeRepoMaps(entry, withRepoEntry({}, "jira:A", "", 9)))).toEqual({});
    expect(liveRepoMap(mergeRepoMaps(entry, { "jira:A": { repo: "", at: 3 } }))).toEqual({ "jira:A": "web" });
    expect(liveRepoMap(mergeRepoMaps(entry, { "jira:A": { repo: "", at: 5 } }))).toEqual({});
  });

  it("converges on a tie between live entries whatever the argument order", () => {
    const a = { "jira:A": { repo: "web", at: 0 } };
    const b = { "jira:A": { repo: "sdk", at: 0 } };
    expect(mergeRepoMaps(a, b)).toEqual(mergeRepoMaps(b, a));
    expect(liveRepoMap(mergeRepoMaps(a, b))).toEqual({ "jira:A": "web" });
  });

  it("stamps past the entry it replaces and keeps tombstones", () => {
    const now = 10_000_000_000_000;
    const old = { "jira:A": { repo: "web", at: now + 50 }, "jira:Z": { repo: "", at: 1 } };
    const out = withRepoEntry(old, "jira:A", "", now);
    expect(out["jira:A"]).toEqual({ repo: "", at: now + 51 });
    expect(out["jira:Z"]).toEqual({ repo: "", at: 1 });
  });
});
