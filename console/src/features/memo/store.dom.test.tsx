import { beforeEach, describe, expect, it } from "vitest";
import type { Memo } from "../../types/memo.ts";
import { memoScope, useMemoStore } from "./store.ts";

const held = (n: number) => Array.from({ length: n }, (_, i) => ({ id: "m" + i, state: "held", sentAt: "" }) as unknown as Memo);
const s = () => useMemoStore.getState();

describe("memo waiting count", () => {
  beforeEach(() => useMemoStore.setState({ waiting: null, scope: "" }));

  it("has no scope while the workspace is stopped", () => {
    expect(memoScope(false, "a")).toBe("");
    expect(memoScope(true, "a")).not.toBe(memoScope(true, "b"));
  });

  it("drops an older read that answers after a newer one", () => {
    s().setScope("t:a");
    const older = s().begin();
    const newer = s().begin();
    s().publish([], newer, "t:a");
    s().publish(held(2), older, "t:a");
    expect(s().waiting).toEqual({ scope: "t:a", n: 0 });
  });

  it("drops an answer for a scope that is no longer on screen, and clears on a scope change", () => {
    s().setScope("t:a");
    const t = s().begin();
    s().publish(held(1), t, "t:a");
    s().setScope("");
    expect(s().waiting).toBeNull();
    s().publish(held(3), s().begin(), "t:a"); // started before the stop
    s().publish(held(3), s().begin(), ""); // read while stopped
    expect(s().waiting).toBeNull();
  });
});
