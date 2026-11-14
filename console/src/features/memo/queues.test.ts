// The pure side of memo auto-run (ADR 0137): what the Control Plane's answers are turned into
// before anything renders them, and the words and rules the screen is built from.
import { describe, expect, it } from "vitest";
import type { Memo, MemoQueue } from "../../types/memo.ts";
import {
  deliveryStateKey,
  draftBody,
  draftErrors,
  draftOf,
  memoOriginWords,
  memoStateWords,
  pendingCount,
  readDeliveries,
  readMemoQueues,
  releaseCards,
  staleIdsOf,
} from "./queues.ts";

const memo = (over: Partial<Memo>): Memo => ({
  id: "m",
  repo: "",
  category: "c",
  kind: "text",
  body: "b",
  refPath: "",
  position: 0,
  createdAt: "",
  sentAt: "",
  ...over,
});

describe("readMemoQueues", () => {
  it("rebuilds every list, so a JSON null where an array is expected cannot reach a .map", () => {
    expect(readMemoQueues({ tenantEnabled: true, queues: null })).toEqual({ tenantEnabled: true, queues: [] });
    expect(readMemoQueues({ queues: [null, 5, { categoryId: "" }, { categoryId: "c1" }] })?.queues.map((q) => q.categoryId)).toEqual(["c1"]);
  });
  it("answers null for a failed read, so the caller keeps what it had", () => {
    expect(readMemoQueues(null)).toBeNull();
    expect(readMemoQueues({ error: { code: "x" } })).toBeNull();
    expect(readMemoQueues([])).toBeNull();
  });
  it("defaults a missing worktree to on, like the Control Plane", () => {
    expect(readMemoQueues({ queues: [{ categoryId: "c1" }] })?.queues[0].worktree).toBe(true);
  });
});

describe("readDeliveries", () => {
  it("accepts null as empty and an error body as a failed read", () => {
    expect(readDeliveries(null)).toEqual([]);
    expect(readDeliveries({ error: {} })).toBeNull();
    expect(readDeliveries("x")).toBeNull();
  });
  it("drops rows without an id and a null messageIds", () => {
    const out = readDeliveries([{ id: "d1", messageIds: null }, { state: "open" }]);
    expect(out).toHaveLength(1);
    expect(out![0].messageIds).toEqual([]);
  });
});

describe("words", () => {
  it("names each state by the working term's catalogue key and ignores a plain note", () => {
    expect(memoStateWords("held")?.label).toBe("memo.state_held");
    expect(memoStateWords("pending")?.label).toBe("memo.state_pending");
    expect(memoStateWords("claimed")?.label).toBe("memo.state_claimed");
    expect(memoStateWords("delivered")?.label).toBe("memo.state_delivered");
    expect(memoStateWords("unconfirmed")?.label).toBe("memo.state_unconfirmed");
    expect(memoStateWords("")).toBeNull();
    expect(memoStateWords(undefined)).toBeNull();
    expect(memoStateWords("brand_new")).toBeNull();
  });
  it("says where a message in a queue came from, and nothing for a plain note", () => {
    expect(memoOriginWords({ state: "held", provenance: "workspace", producer: "s1" })).toEqual({ key: "memo.origin_session", name: "s1" });
    expect(memoOriginWords({ state: "held", provenance: "mcp", producer: "pat_1" })).toEqual({ key: "memo.origin_mcp", name: "pat_1" });
    expect(memoOriginWords({ state: "pending", provenance: "member" })).toEqual({ key: "memo.origin_member" });
    expect(memoOriginWords({ state: "held", provenance: "workspace" })).toEqual({ key: "memo.origin_session_unknown" });
    expect(memoOriginWords({ state: "", provenance: "workspace", producer: "s1" })).toBeNull();
  });
  it("reads a delivery's state from state and outcome", () => {
    expect(deliveryStateKey({ state: "claimed", outcome: "" })).toBe("memo.d_state_starting");
    expect(deliveryStateKey({ state: "sending", outcome: "" })).toBe("memo.d_state_starting");
    expect(deliveryStateKey({ state: "open", outcome: "" })).toBe("memo.d_state_running");
    expect(deliveryStateKey({ state: "closed", outcome: "not_sent" })).toBe("memo.d_state_not_sent");
    expect(deliveryStateKey({ state: "closed", outcome: "weird" })).toBeNull();
  });
});

describe("releaseCards", () => {
  it("makes one card per producer from the held messages only, in order", () => {
    const cards = releaseCards([
      memo({ id: "a", state: "held", provenance: "workspace", producer: "s1", contentHash: "h1" }),
      memo({ id: "b", state: "pending", provenance: "workspace", producer: "s1", contentHash: "h2" }),
      memo({ id: "c", state: "held", provenance: "mcp", producer: "pat", contentHash: "h3" }),
      memo({ id: "d", state: "held", provenance: "workspace", producer: "s1", contentHash: "h4" }),
    ]);
    expect(cards.map((c) => c.items.map((m) => m.id))).toEqual([["a", "d"], ["c"]]);
  });
  it("never lists a held message that has no hash to release it by", () => {
    expect(releaseCards([memo({ id: "a", state: "held", provenance: "workspace" })])).toEqual([]);
  });
});

describe("staleIdsOf", () => {
  it("reads error.stale of a memo_release_stale answer only", () => {
    expect(staleIdsOf({ error: { code: "memo_release_stale", stale: ["a", 3, "b"] } })).toEqual(["a", "b"]);
    expect(staleIdsOf({ error: { code: "memo_release_stale", stale: null } })).toEqual([]);
    expect(staleIdsOf({ error: { code: "other", stale: ["a"] } })).toEqual([]);
  });
});

describe("settings form", () => {
  const ok = { ...draftOf(undefined), instruction: "do it" };
  it("starts from the Control Plane's defaults", () => {
    const d = draftOf(undefined);
    expect([d.agentKind, d.worktree, d.maxActive, d.batch, d.spendCapUsd, d.enabled]).toEqual(["claude", true, "2", "1", "0", false]);
  });
  it("needs an instruction only to turn it on", () => {
    expect(draftErrors({ ...draftOf(undefined) })).toEqual({});
    expect(draftErrors({ ...draftOf(undefined), enabled: true }).instruction).toBe("memo.q_err_instruction");
    expect(draftErrors({ ...ok, enabled: true })).toEqual({});
  });
  it("counts the instruction in bytes, as the Control Plane does", () => {
    expect(draftErrors({ ...ok, instruction: "あ".repeat(6000) }).instruction).toBe("memo.q_err_instruction_long");
  });
  it("bounds the numbers", () => {
    expect(draftErrors({ ...ok, maxActive: "0" }).maxActive).toBeDefined();
    expect(draftErrors({ ...ok, maxActive: "7" }).maxActive).toBeDefined();
    expect(draftErrors({ ...ok, maxActive: "6" }).maxActive).toBeUndefined();
    expect(draftErrors({ ...ok, batch: "21" }).batch).toBeDefined();
    expect(draftErrors({ ...ok, batch: "1.5" }).batch).toBeDefined();
    expect(draftErrors({ ...ok, spendCapUsd: "-1" }).spendCapUsd).toBeDefined();
    expect(draftErrors({ ...ok, spendCapUsd: "10001" }).spendCapUsd).toBeDefined();
    expect(draftErrors({ ...ok, spendCapUsd: "2.5" }).spendCapUsd).toBeUndefined();
  });
  it("sends the route back untouched: a PUT replaces the whole row", () => {
    const q: MemoQueue = { ...(readMemoQueues({ queues: [{ categoryId: "c1", route: "r1" }] })!.queues[0]) };
    expect(draftBody(draftOf(q), q.route).route).toBe("r1");
  });
});

describe("pendingCount", () => {
  it("counts the messages a fire could take", () => {
    expect(pendingCount([memo({ state: "pending" }), memo({ state: "held" }), memo({ state: "pending", sentAt: "x" })])).toBe(1);
  });
});
