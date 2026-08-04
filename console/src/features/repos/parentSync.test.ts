import { beforeEach, describe, expect, it } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { parentSyncLabel } from "./parentSync.ts";

describe("parentSyncLabel", () => {
  beforeEach(() => setLocale("ja"));

  it("makes the parent-relative lag and fast-forward direction explicit", () => {
    expect(parentSyncLabel({ relation: "contained", targetUnique: 3, worktreeUnique: 0 })).toBe("取込済・親+3");
    expect(parentSyncLabel({ relation: "unmerged", targetUnique: 0, worktreeUnique: 2 })).toBe("親へ FF可 +2");
    expect(parentSyncLabel({ relation: "diverged", targetUnique: 3, worktreeUnique: 2 })).toBe("分岐 2↕3・FF不可");
  });

  it("keeps the concise state labels localized", () => {
    setLocale("en");
    expect(parentSyncLabel({ relation: "contained", targetUnique: 1, worktreeUnique: 0 })).toBe("merged · parent +1");
    expect(parentSyncLabel({ relation: "unmerged", targetUnique: 0, worktreeUnique: 1 })).toBe("FF parent +1");
  });
});
