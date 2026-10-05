import { describe, it, expect } from "vitest";
import type { Session } from "../../types/session.ts";
import type { WorkItemSessionRef } from "../workitems/read.ts";
import { matchSessionRef, refIndex, refQuery } from "./refSearch.ts";

const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", ...extra });
const ledgerRow = (sessionName: string, itemKey: string): WorkItemSessionRef => ({
  id: sessionName + itemKey,
  provider: "github",
  itemKey,
  sessionName,
  repo: "",
  branch: "",
  createdAt: "",
});
const pr = (number: number, path = "acme/app") => ({
  number,
  state: "open" as const,
  url: `https://github.com/${path}/pull/${number}`,
});

describe("refQuery", () => {
  it("reads numbers, qualified numbers and ticket keys; anything else is text", () => {
    expect(refQuery("#1662")).toEqual({ num: "1662", repo: "" });
    expect(refQuery(" 1662 ")).toEqual({ num: "1662", repo: "" });
    expect(refQuery("Acme/App#45")).toEqual({ num: "45", repo: "acme/app" });
    expect(refQuery("PROJ-123")).toEqual({ key: "proj-123" });
    expect(refQuery("acme/app45")).toBeNull();
    expect(refQuery("007")).toBeNull();
    expect(refQuery("mirror")).toBeNull();
    expect(refQuery("")).toBeNull();
  });
});

describe("matchSessionRef", () => {
  const idx = refIndex([
    ledgerRow("s1", "acme/app#45"),
    ledgerRow("s1", "acme/app#45"),
    ledgerRow("s2", "PROJ-123"),
    ledgerRow("s3", "other/lib#45"),
  ]);

  it("finds the PR by number, with or without #", () => {
    const s = sess("x", { pr: pr(1662) });
    expect(matchSessionRef(s, idx, refQuery("#1662"))).toBe("PR #1662");
    expect(matchSessionRef(s, idx, refQuery("1662"))).toBe("PR #1662");
    expect(matchSessionRef(s, idx, refQuery("acme/app#1662"))).toBe("PR #1662");
    expect(matchSessionRef(s, idx, refQuery("other/lib#1662"))).toBeNull();
  });

  it("matches whole numbers only", () => {
    const s = sess("x", { pr: pr(1662) });
    expect(matchSessionRef(s, idx, refQuery("#12"))).toBeNull();
    expect(matchSessionRef(s, idx, refQuery("166"))).toBeNull();
    expect(matchSessionRef(sess("s1"), idx, refQuery("#4"))).toBeNull();
  });

  it("finds a ledger issue by its short and full key, scoped by repository when qualified", () => {
    expect(matchSessionRef(sess("s1"), idx, refQuery("#45"))).toBe("acme/app#45");
    expect(matchSessionRef(sess("s3"), idx, refQuery("45"))).toBe("other/lib#45");
    expect(matchSessionRef(sess("s1"), idx, refQuery("ACME/app#45"))).toBe("acme/app#45");
    expect(matchSessionRef(sess("s3"), idx, refQuery("acme/app#45"))).toBeNull();
  });

  it("finds a ticket key case-insensitively", () => {
    expect(matchSessionRef(sess("s2"), idx, refQuery("proj-123"))).toBe("PROJ-123");
    expect(matchSessionRef(sess("s2"), idx, refQuery("PROJ-12"))).toBeNull();
    expect(matchSessionRef(sess("s1"), idx, refQuery("PROJ-123"))).toBeNull();
  });

  it("never matches ordinary text", () => {
    expect(matchSessionRef(sess("s1", { pr: pr(1) }), idx, refQuery("app"))).toBeNull();
  });
});

describe("refIndex", () => {
  it("dedupes a key launched twice in one session and skips incomplete rows", () => {
    const idx = refIndex([ledgerRow("s1", "a/b#1"), ledgerRow("s1", "a/b#1"), ledgerRow("", "a/b#2"), ledgerRow("s2", "")]);
    expect([...idx.entries()]).toEqual([["s1", ["a/b#1"]]]);
  });
});
