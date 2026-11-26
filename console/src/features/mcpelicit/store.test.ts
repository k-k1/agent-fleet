// Reads of the held-call list can overlap; a late answer to an older read must not replace a
// newer list, or a held call's card would vanish until the next poll.
import { beforeEach, describe, expect, it, vi } from "vitest";

const pending: ((v: unknown) => void)[] = [];
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  apiJSON: async () => ({}),
  api: vi.fn(() => new Promise((r) => pending.push(r))),
}));

const { useMcpElicitStore } = await import("./store.ts");

const call = { id: "e1", serverId: "s", server: "s", tool: "t", inputs: [], createdAt: "", deadline: "" };

beforeEach(() => {
  pending.length = 0;
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
});
