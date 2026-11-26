// Reads of the held-call list can overlap; a late answer to an older read must not replace a
// newer list, or a held call's card would vanish until the next poll.
import { beforeEach, describe, expect, it, vi } from "vitest";

const pending: ((v: unknown) => void)[] = [];
const who = vi.hoisted(() => ({ tenant: "a", user: "u1", posts: [] as ((v: unknown) => void)[] }));
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => who.tenant,
  getUser: () => who.user,
  apiJSON: vi.fn(() => new Promise((r) => who.posts.push(r))),
  api: vi.fn(() => new Promise((r) => pending.push(r))),
}));

const { useMcpElicitStore } = await import("./store.ts");

const call = { id: "e1", serverId: "s", server: "s", tool: "t", inputs: [], createdAt: "", deadline: "" };

beforeEach(() => {
  pending.length = 0;
  who.tenant = "a";
  who.user = "u1";
  who.posts = [];
  useMcpElicitStore.getState().reset();
});

describe("the held-call list", () => {
  it("keeps the newer read when an older one answers last", async () => {
    const first = useMcpElicitStore.getState().refresh();
    const second = useMcpElicitStore.getState().refresh();
    pending[1]({ elicitations: [call] });
    await second;
    pending[0]({ elicitations: [] });
    await first;
    expect(useMcpElicitStore.getState().items.map((e) => e.id)).toEqual(["e1"]);
  });
  it("drops a read issued before a reset", async () => {
    const before = useMcpElicitStore.getState().refresh();
    useMcpElicitStore.getState().reset();
    pending[0]({ elicitations: [call] });
    await before;
    expect(useMcpElicitStore.getState().items).toEqual([]);
  });
  // A read issued before the tenant or the member settled (at start, or a sign-in as someone
  // else in the same tenant) belongs to whoever it was issued for.
  it("drops a read issued for another tenant or member", async () => {
    for (const change of [() => (who.tenant = "b"), () => (who.user = "u2")]) {
      who.tenant = "a";
      who.user = "u1";
      const read = useMcpElicitStore.getState().refresh();
      change();
      pending[pending.length - 1]({ elicitations: [call] });
      await read;
      expect(useMcpElicitStore.getState().items).toEqual([]);
    }
  });
  // The Agent's deadline is on its clock; the store counts the time left on the page's own.
  it("times a call from the seconds left, not from the Agent's clock", async () => {
    const read = useMcpElicitStore.getState().refresh();
    pending[0]({ elicitations: [{ ...call, deadline: "2000-01-01T00:00:00Z", expiresInSec: 600 }] });
    await read;
    const until = useMcpElicitStore.getState().until.get("e1")!;
    expect(until - performance.now()).toBeGreaterThan(590_000);
    expect(until - performance.now()).toBeLessThanOrEqual(600_000);
  });
  it("sends one answer per call at a time", async () => {
    const first = useMcpElicitStore.getState().answer(call, { k: {} });
    const second = await useMcpElicitStore.getState().answer(call, null);
    expect(second).toEqual({ code: "busy" });
    expect(who.posts.length).toBe(1);
    who.posts[0]({});
    await new Promise((r) => setTimeout(r, 0));
    pending.forEach((r) => r({ elicitations: [] }));
    expect(await first).toBe(null);
  });
});
