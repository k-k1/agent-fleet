// Every real delete the existing cleanups make retires the walk behind the low-disk notice, so a
// cleanup from Settings › Machine or the cleanup modal cannot leave a stale figure on the toast.
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.fn();
const raw = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  raw: (...a: unknown[]) => raw(...a),
  errText: () => "",
  apiJSON: async () => ({}),
  getTenant: () => "t",
}));

import { emptyToolCache } from "./toolCaches.ts";
import { applyWorktreeOutput } from "../../sessions/worktreeOutput.ts";
import { __test, publishWalk, reclaimEstimate } from "../../../core/store/homeReclaim.ts";

const walk = () => ({ measured_at: new Date().toISOString(), categories: [], repos: [{ bytes: 9, regenerable: 5 }] });

beforeEach(() => {
  __test.reset();
  api.mockReset();
  raw.mockReset();
  publishWalk(walk());
});

describe("shared delete boundaries", () => {
  it("emptying a tool cache retires the held walk, even when the answer is an error", async () => {
    expect(reclaimEstimate()).toBe(5);
    api.mockResolvedValue({ error: { code: "x" } });
    await emptyToolCache("go-build");
    expect(reclaimEstimate()).toBeNull();
  });
  it("retires it when the request itself failed", async () => {
    api.mockRejectedValue(new Error("lost"));
    await expect(emptyToolCache("go-build")).rejects.toThrow();
    expect(reclaimEstimate()).toBeNull();
  });
  it("a worktree dry run keeps the walk; a real delete retires it", async () => {
    raw.mockResolvedValue({ status: 200, json: async () => ({ name: "a", status: "candidate", bytes: 1 }) });
    await applyWorktreeOutput("a", true);
    expect(reclaimEstimate()).toBe(5);
    await applyWorktreeOutput("a", false);
    expect(reclaimEstimate()).toBeNull();
  });
});
