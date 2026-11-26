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
  // A switch away and back: the read from the first visit passes the owner check again, but it
  // was sent before the reset, and the calls it lists may be gone since.
  it("drops a read sent before a switch away and back, and keeps the one after", async () => {
    const old = useMcpElicitStore.getState().refresh();
    who.tenant = "b";
    useMcpElicitStore.getState().reset();
    who.tenant = "a";
    useMcpElicitStore.getState().reset();
    const fresh = useMcpElicitStore.getState().refresh();
    pending[0]({ elicitations: [call] });
    await old;
    expect(useMcpElicitStore.getState().items).toEqual([]);
    pending[1]({ elicitations: [{ ...call, id: "e2" }] });
    await fresh;
    expect(useMcpElicitStore.getState().items.map((e) => e.id)).toEqual(["e2"]);
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
  it("times a call from the time left, not from the Agent's clock", async () => {
    const read = useMcpElicitStore.getState().refresh();
    pending[0]({ elicitations: [{ ...call, deadline: "2000-01-01T00:00:00Z", expiresInMs: 600_000 }] });
    await read;
    const until = useMcpElicitStore.getState().until.get("e1")!;
    expect(until - performance.now()).toBeGreaterThan(590_000);
    expect(until - performance.now()).toBeLessThanOrEqual(600_000);
  });
  // A list read and shown for this member, as the card sees it before it answers.
  const shown = async (items: unknown[]) => {
    const read = useMcpElicitStore.getState().refresh();
    pending[pending.length - 1]({ elicitations: items });
    await read;
  };
  it("sends one answer per call at a time", async () => {
    await shown([call]);
    const first = useMcpElicitStore.getState().answer(call, { k: {} });
    const second = await useMcpElicitStore.getState().answer(call, null);
    expect(second).toEqual({ code: "busy" });
    expect(who.posts.length).toBe(1);
    who.posts[0]({});
    await new Promise((r) => setTimeout(r, 0));
    pending.forEach((r) => r({ elicitations: [] }));
    expect(await first).toBe(null);
  });
  // Between a taken answer and the next read, the call must neither take a second answer nor
  // come back from a read sent before the answer.
  it("keeps an answered call answered until a later read", async () => {
    await shown([call]);
    const stale = useMcpElicitStore.getState().refresh(); // sent before the answer, answers after
    const first = useMcpElicitStore.getState().answer(call, { k: {} });
    who.posts[0]({});
    await new Promise((r) => setTimeout(r, 0));
    expect(useMcpElicitStore.getState().items).toEqual([]);
    expect(await useMcpElicitStore.getState().answer(call, null)).not.toBe(null);
    expect(who.posts.length).toBe(1);
    pending[1]({ elicitations: [call] });
    await stale;
    expect(useMcpElicitStore.getState().items).toEqual([]);
    pending[2]({ elicitations: [] }); // the answer's own read, sent after it
    await first;
    expect(useMcpElicitStore.getState().items).toEqual([]);
  });
  // A switch to another tenant and back resets what is shown, not the answer on its way.
  it("does not answer a call again after a switch away and back", async () => {
    await shown([call]);
    const first = useMcpElicitStore.getState().answer(call, { k: {} });
    who.tenant = "b";
    useMcpElicitStore.getState().reset();
    who.tenant = "a";
    useMcpElicitStore.getState().reset();
    await shown([call]);
    expect(await useMcpElicitStore.getState().answer(call, null)).toEqual({ code: "busy" });
    expect(who.posts.length).toBe(1);
    who.posts[0]({});
    await new Promise((r) => setTimeout(r, 0));
    pending.forEach((r) => r({ elicitations: [] }));
    await first;
  });
  // A card or toast left over from another tenant answers nothing on the new one.
  it("refuses a call that is not on the list shown now", async () => {
    await shown([call]);
    who.tenant = "b";
    useMcpElicitStore.getState().reset();
    expect(await useMcpElicitStore.getState().answer(call, null)).toEqual({ code: "stale" });
    expect(who.posts.length).toBe(0);
  });
  // An answer sent for one tenant that settles after a switch leaves the new tenant's list alone.
  it("leaves another tenant's list alone when an earlier answer settles", async () => {
    await shown([call]);
    const first = useMcpElicitStore.getState().answer(call, null);
    who.tenant = "b";
    useMcpElicitStore.getState().reset();
    await shown([{ ...call, id: "e2" }]);
    const reads = pending.length;
    who.posts[0]({ error: { code: "http_502" } });
    expect(await first).toEqual({ code: "http_502" });
    expect(useMcpElicitStore.getState().items.map((e) => e.id)).toEqual(["e2"]);
    expect(pending.length).toBe(reads); // no read on the old answer's behalf
  });
});
