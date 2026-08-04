import { beforeEach, describe, expect, it } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { canFastForwardFromParent, parentSyncLabel } from "./parentSync.ts";

describe("parentSyncLabel", () => {
  beforeEach(() => setLocale("ja"));

  it("makes the parent-relative lag and fast-forward direction explicit", () => {
    expect(parentSyncLabel({ relation: "contained", targetUnique: 3, worktreeUnique: 0 })).toBe("親+3・FF可");
    expect(parentSyncLabel({ relation: "unmerged", targetUnique: 0, worktreeUnique: 2 })).toBe("未取込 2");
    expect(parentSyncLabel({ relation: "diverged", targetUnique: 3, worktreeUnique: 2 })).toBe("分岐 2↕3・FF不可");
  });

  it("keeps the concise state labels localized", () => {
    setLocale("en");
    expect(parentSyncLabel({ relation: "contained", targetUnique: 1, worktreeUnique: 0 })).toBe("parent +1 · FF ok");
    expect(parentSyncLabel({ relation: "unmerged", targetUnique: 0, worktreeUnique: 1 })).toBe("unmerged 1");
  });

  it("offers the parent action only when this worktree can fast-forward from it", () => {
    expect(canFastForwardFromParent({ name: "wt", worktree: true, integration: { relation: "contained", targetUnique: 1, worktreeUnique: 0 } })).toBe(true);
    expect(canFastForwardFromParent({ name: "wt", worktree: true, integration: { relation: "diverged", targetUnique: 1, worktreeUnique: 1 } })).toBe(false);
    expect(canFastForwardFromParent({ name: "base", integration: { relation: "contained", targetUnique: 1, worktreeUnique: 0 } })).toBe(false);
  });
});
