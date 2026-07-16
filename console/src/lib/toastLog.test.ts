import { beforeEach, describe, expect, it } from "vitest";
import { useToastLog, toastLogUnseen, type ToastLogItem } from "./toastLog.ts";

// vitest runs in the node env (no localStorage) — read()/write() no-op via try/catch, so the
// store operates purely in-memory here, which is exactly what these logic tests exercise.
const reset = () => useToastLog.setState({ items: [] });

describe("toastLog store", () => {
  beforeEach(reset);

  it("push prepends an unseen entry and counts unseen", () => {
    useToastLog.getState().push("error", "削除に失敗しました");
    useToastLog.getState().push("success", "削除しました");
    const items = useToastLog.getState().items;
    expect(items.map((i) => i.message)).toEqual(["削除しました", "削除に失敗しました"]);
    expect(items.every((i) => !i.seen)).toBe(true);
    expect(toastLogUnseen(items)).toBe(2);
  });

  it("markAllSeen clears the unread count", () => {
    useToastLog.getState().push("error", "x");
    useToastLog.getState().markAllSeen();
    expect(toastLogUnseen(useToastLog.getState().items)).toBe(0);
  });

  it("remove drops the entry by id", () => {
    useToastLog.getState().push("warn", "y");
    const id = useToastLog.getState().items[0].id;
    useToastLog.getState().remove(id);
    expect(useToastLog.getState().items).toHaveLength(0);
  });

  it("prunes entries older than the 7-day retention window on push", () => {
    const old: ToastLogItem = {
      id: "old",
      kind: "error",
      message: "古い",
      createdAt: new Date(Date.now() - 8 * 864e5).toISOString(),
      seen: false,
    };
    useToastLog.setState({ items: [old] });
    useToastLog.getState().push("info", "新しい");
    const items = useToastLog.getState().items;
    expect(items.map((i) => i.id)).not.toContain("old");
    expect(items.map((i) => i.message)).toContain("新しい");
  });

  it("caps the log at 50 newest entries", () => {
    for (let i = 0; i < 55; i++) useToastLog.getState().push("info", "m" + i);
    const items = useToastLog.getState().items;
    expect(items).toHaveLength(50);
    expect(items[0].message).toBe("m54"); // newest kept
    expect(items.map((i) => i.message)).not.toContain("m0"); // oldest dropped
  });
});
