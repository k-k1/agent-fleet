import { beforeEach, describe, expect, it } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { canFastForwardFromParent, parentFFFailedText, parentFFMenuLabel, parentFFSuccessText, parentSyncLabel, parentSyncTitle } from "./parentSync.ts";

describe("parentSyncLabel", () => {
  beforeEach(() => setLocale("ja"));

  it("makes the parent-relative lag and fast-forward direction explicit", () => {
    expect(parentSyncLabel({ relation: "contained", targetUnique: 3, worktreeUnique: 0 })).toBe("親+3・FF 可");
    expect(parentSyncLabel({ relation: "unmerged", targetUnique: 0, worktreeUnique: 2 })).toBe("未取込 2");
    expect(parentSyncLabel({ relation: "diverged", targetUnique: 3, worktreeUnique: 2 })).toBe("分岐 2↕3・FF 不可");
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

  it("names the upstream as the target in the tooltip, menu item and toasts", () => {
    const up = { name: "wt", worktree: true, integration: { relation: "contained" as const, targetBranch: "origin/develop", targetUpstream: true, targetUnique: 2, worktreeUnique: 0 } };
    expect(parentSyncLabel(up.integration)).toBe("親+2・FF 可");
    expect(parentSyncTitle(up.integration)).toBe("比較先: origin/develop\nWT の HEAD は Git 履歴上、比較先に含まれています（比較先固有 2 コミット）。比較先から fast-forward で取り込めます");
    expect(parentFFMenuLabel(up)).toBe("origin/develop を Fast-Forward で取り込む");
    expect(parentFFSuccessText(up)).toBe("wt: origin/develop を fast-forward で取り込みました");
    expect(parentFFFailedText(up, "boom")).toBe("origin/develop の fast-forward 取り込みに失敗しました: boom");
    setLocale("en");
    expect(parentFFMenuLabel(up)).toBe("Fast-forward from origin/develop");
    expect(parentFFSuccessText(up)).toBe("wt: fast-forwarded from origin/develop");
  });

  it("keeps the parent wording when the target is the parent's HEAD", () => {
    const local = { name: "wt", worktree: true, integration: { relation: "contained" as const, targetBranch: "develop", targetUnique: 1, worktreeUnique: 0 } };
    expect(parentFFMenuLabel(local)).toBe("親を Fast-Forward で取り込む");
    expect(parentFFSuccessText(local)).toBe("wt: 親の変更を fast-forward で取り込みました");
    expect(parentFFFailedText(local, "boom")).toBe("親の fast-forward 取り込みに失敗しました: boom");
    const unnamed = { name: "wt", worktree: true, integration: { relation: "contained" as const, targetUpstream: true, targetUnique: 1, worktreeUnique: 0 } };
    expect(parentFFMenuLabel(unnamed)).toBe("親を Fast-Forward で取り込む");
  });
});
