import { beforeEach, describe, expect, it, vi } from "vitest";

const apiJSON = vi.fn();
const askConfirm = vi.fn();
vi.mock("../../core/api/client.ts", () => ({ apiJSON: (...a: unknown[]) => apiJSON(...a) }));
vi.mock("../../ui/confirmBridge.ts", () => ({ askConfirm: (...a: unknown[]) => askConfirm(...a) }));

import { postFastForward, STALE_INDEX_LOCK } from "./staleIndexLock.ts";

const stale = { error: { code: STALE_INDEX_LOCK, message: "m", lock: "/r/.git/index.lock" } };

describe("postFastForward", () => {
  beforeEach(() => {
    apiJSON.mockReset();
    askConfirm.mockReset();
  });

  it("returns any other answer as it is, without asking", async () => {
    apiJSON.mockResolvedValueOnce({ error: { code: "ff_failed", message: "diverged" } });
    expect(await postFastForward("api/repos/a/ff")).toEqual({ error: { code: "ff_failed", message: "diverged" } });
    expect(askConfirm).not.toHaveBeenCalled();
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("retries with removeStaleLock once the user confirms", async () => {
    apiJSON.mockResolvedValueOnce(stale).mockResolvedValueOnce({ branch: "main" });
    askConfirm.mockResolvedValueOnce(true);
    expect(await postFastForward("api/repos/a/parent-ff")).toEqual({ branch: "main" });
    expect(String(askConfirm.mock.calls[0][0].body)).toContain("/r/.git/index.lock");
    expect(apiJSON.mock.calls).toEqual([
      ["api/repos/a/parent-ff", "POST", {}],
      ["api/repos/a/parent-ff", "POST", { removeStaleLock: true }],
    ]);
  });

  it("leaves the lock and returns the error when the user declines", async () => {
    apiJSON.mockResolvedValueOnce(stale);
    askConfirm.mockResolvedValueOnce(false);
    expect(await postFastForward("api/repos/a/ff")).toBe(stale);
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });
});
