// The Jira project list is per member and tenant: a switch must never show the previous owner's keys.
import { beforeEach, describe, expect, it, vi } from "vitest";

let answers: ((v: unknown) => void)[] = [];
const apiCalls = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  api: (p: string) => {
    apiCalls(p);
    return new Promise((r) => answers.push(r));
  },
}));

const { ensureJiraProjects, resetJiraProjects, useJiraProjects } = await import("./jiraProjects.ts");
// Importing the tenant store is what wires its switches to the list.
const { useTenantStore } = await import("../../core/store/tenant.ts");

const settle = () => new Promise((r) => setTimeout(r, 0));
const land = async (i: number, keys: string[]) => {
  answers[i]({ connected: true, keys, truncated: false });
  await settle();
};

beforeEach(() => {
  answers = [];
  apiCalls.mockReset();
  useTenantStore.setState({ tenant: "ta", identityRev: 0 });
  resetJiraProjects();
});

describe("jira project list ownership", () => {
  it("drops the keys at once and asks again after a tenant switch", async () => {
    void ensureJiraProjects();
    await land(0, ["AAA"]);
    expect(useJiraProjects.getState().keys).toEqual(["AAA"]);

    useTenantStore.setState({ tenant: "tb" });
    expect(useJiraProjects.getState().keys).toEqual([]);
    void ensureJiraProjects();
    expect(apiCalls).toHaveBeenCalledTimes(2);
    await land(1, ["BBB"]);
    expect(useJiraProjects.getState().keys).toEqual(["BBB"]);
  });

  it("does the same when another account signs in on the same tenant", async () => {
    void ensureJiraProjects();
    await land(0, ["AAA"]);
    useTenantStore.setState({ identityRev: 1 });
    expect(useJiraProjects.getState().keys).toEqual([]);
    void ensureJiraProjects();
    expect(apiCalls).toHaveBeenCalledTimes(2);
  });

  it("discards a late answer asked for under the previous owner, and keeps the new request pending", async () => {
    void ensureJiraProjects(); // owner A, slow
    useTenantStore.setState({ tenant: "tb" });
    void ensureJiraProjects(); // owner B
    expect(apiCalls).toHaveBeenCalledTimes(2);
    await land(0, ["AAA"]); // A's answer arrives late
    expect(useJiraProjects.getState().keys).toEqual([]);
    // A's settling must not have cleared B's pending request. Past the retry window only the
    // pending guard stops a third request, so move the clock beyond it.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(Date.now() + 5 * 60 * 1000);
    void ensureJiraProjects();
    vi.useRealTimers();
    expect(apiCalls).toHaveBeenCalledTimes(2);
    await land(1, ["BBB"]);
    expect(useJiraProjects.getState().keys).toEqual(["BBB"]);
  });
});
