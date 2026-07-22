import { describe, it, expect, beforeEach } from "vitest";
import { useUiOpen } from "./uiOpen.ts";

describe("useUiOpen", () => {
  beforeEach(() => {
    useUiOpen.setState({
      seq: { notifications: 0, "usage-claude": 0, "usage-codex": 0, "usage-copilot": 0, "usage-agy": 0, resources: 0 },
    });
  });

  it("bumps only the targeted counter", () => {
    useUiOpen.getState().toggle("usage-codex");
    expect(useUiOpen.getState().seq).toEqual({
      notifications: 0,
      "usage-claude": 0,
      "usage-codex": 1,
      "usage-copilot": 0,
      "usage-agy": 0,
      resources: 0,
    });
  });

  it("is monotonic per target across repeated toggles", () => {
    const t = useUiOpen.getState().toggle;
    t("notifications");
    t("notifications");
    t("resources");
    const { seq } = useUiOpen.getState();
    expect(seq.notifications).toBe(2);
    expect(seq.resources).toBe(1);
    expect(seq["usage-claude"]).toBe(0);
  });
});
