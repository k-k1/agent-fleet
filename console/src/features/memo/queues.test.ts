// The pure side of memo auto-run (ADR 0137): what the Control Plane's answers are turned into
// before anything renders them, and the words and rules the screen is built from.
import { describe, expect, it } from "vitest";
import type { Memo, MemoQueue } from "../../types/memo.ts";
import {
  afterChoices,
  afterItemMessage,
  afterItemWords,
  deliveryStateKey,
  draftBody,
  draftErrors,
  draftOf,
  confirmShown,
  firstTurnWords,
  heldNames,
  heldWords,
  memoOriginWords,
  memoStateWords,
  pendingCount,
  readDeliveries,
  readMemoQueues,
  waitingWords,
  shownStatus,
  trackShown,
  releaseCards,
  runModeOf,
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
  it("keeps the accept-from-own-sessions switch off unless the queue says true", () => {
    expect(draftOf(undefined).acceptOwnSessions).toBe(false);
    expect(draftBody(draftOf(undefined), "").acceptOwnSessions).toBe(false);
    expect(readMemoQueues({ queues: [{ categoryId: "c1" }] })?.queues[0].acceptOwnSessions).toBe(false);
    expect(readMemoQueues({ queues: [{ categoryId: "c1", acceptOwnSessions: "yes" }] })?.queues[0].acceptOwnSessions).toBe(false);
    const q = readMemoQueues({ queues: [{ categoryId: "c1", acceptOwnSessions: true }] })!.queues[0];
    expect(draftOf(q).acceptOwnSessions).toBe(true);
    expect(draftBody(draftOf(q), "").acceptOwnSessions).toBe(true);
  });
  it("says the three facts of the switch in both languages", async () => {
    const { memo: en } = await import("../../lib/i18n/locales/en/memo.ts");
    const { memo: ja } = await import("../../lib/i18n/locales/ja/memo.ts");
    expect(en["memo.q_accept_own_hint"]).toMatch(/started yourself start without your approval/);
    expect(en["memo.q_accept_own_hint"]).toMatch(/auto-run, a schedule or another session started still wait for your approval/);
    expect(en["memo.q_accept_own_hint"]).toMatch(/pass itself off as a session you started/);
    expect(ja["memo.q_accept_own_hint"]).toMatch(/承認を待たずに実行待ちになります/);
    expect(ja["memo.q_accept_own_hint"]).toMatch(/自動実行・スケジュール・ほかのセッションが起動したセッションのメモは、これまでどおり承認を待ちます/);
    expect(ja["memo.q_accept_own_hint"]).toMatch(/なりすまして/);
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

describe("when it runs", () => {
  const ok = { ...draftOf(undefined), instruction: "do it" };
  it("reads the three ways from the settings, and an older Control Plane's row as any time with the defaults", () => {
    const q = (o: Record<string, unknown>) => readMemoQueues({ queues: [{ categoryId: "c1", ...o }] })!.queues[0];
    expect(runModeOf(q({}))).toBe("always");
    expect(runModeOf(q({ openCron: "0 9 * * *", openMinutes: 30 }))).toBe("window");
    expect(runModeOf(q({ openAfter: 3 }))).toBe("threshold");
    const old = q({});
    expect([old.maxTaskHours, old.maxFiresPerDay, old.paused, old.noReportAt]).toEqual([12, 20, false, ""]);
  });
  it("checks only the fields of the chosen way", () => {
    expect(draftErrors({ ...ok, runMode: "always", openCron: "junk", openAfter: "x" })).toEqual({});
    expect(draftErrors({ ...ok, runMode: "window", openCron: "" }).openCron).toBe("memo.q_err_cron");
    expect(draftErrors({ ...ok, runMode: "window", openCron: "0 9 * * 1-5", openMinutes: "1441" }).openMinutes).toBe("memo.q_err_window");
    expect(draftErrors({ ...ok, runMode: "window", openCron: "0 9 * * *", openMinutes: "60", tz: "Mars/Base" }).tz).toBe("memo.q_err_tz");
    expect(draftErrors({ ...ok, runMode: "window", openCron: "0 9 * * *", openMinutes: "60", tz: "" })).toEqual({});
    expect(draftErrors({ ...ok, runMode: "threshold", openAfter: "0" }).openAfter).toBe("memo.q_err_open_after");
    expect(draftErrors({ ...ok, runMode: "threshold", openAfter: "100" })).toEqual({});
  });
  it("bounds the runs per day and the overdue hours", () => {
    expect(draftErrors({ ...ok, maxFiresPerDay: "0" }).maxFiresPerDay).toBeDefined();
    expect(draftErrors({ ...ok, maxFiresPerDay: "200" }).maxFiresPerDay).toBeUndefined();
    expect(draftErrors({ ...ok, maxTaskHours: "73" }).maxTaskHours).toBeDefined();
    expect(draftErrors({ ...ok, maxTaskHours: "72" }).maxTaskHours).toBeUndefined();
  });
  it("sends the unchosen ways as their off values and never a read-only field", () => {
    const w = draftBody({ ...ok, runMode: "window", openCron: " 0  9 * * * ", openMinutes: "90", tz: "UTC", openAfter: "7" }, "r");
    expect(w).toMatchObject({ openCron: "0 9 * * *", openMinutes: 90, tz: "UTC", openAfter: 0 });
    const th = draftBody({ ...ok, runMode: "threshold", openCron: "0 9 * * *", openAfter: "7" }, "r");
    expect(th).toMatchObject({ openCron: "", openMinutes: 0, tz: "", openAfter: 7 });
    expect(Object.keys(w).sort()).toEqual(
      ["acceptOwnSessions", "agentKind", "batch", "effort", "enabled", "instruction", "maxActive", "maxFiresPerDay", "maxTaskHours", "model", "openAfter", "openCron", "openMinutes", "route", "spendCapUsd", "tidy", "tidyDestinations", "tz", "worktree"],
    );
  });
});

describe("why a run is still open and how it went", () => {
  it("words each reason, an unknown one as unknown, and nothing for a run that is not open", () => {
    expect(heldWords({ state: "open", heldBy: "person" })).toBe("memo.d_held_person");
    expect(heldWords({ state: "open", heldBy: "from-the-future" })).toBe("memo.d_held_unknown");
    expect(heldWords({ state: "open", heldBy: "" })).toBeNull();
    expect(heldWords({ state: "closed", heldBy: "machine" })).toBeNull();
    expect(heldNames({ state: "open", heldBy: "wakers", heldWakers: [{ id: "w1", source: "github", timer: false, builtin: false }, { id: "t1", source: "", timer: true, builtin: false }] })).toBe("w1 (github), t1");
    const blank = readDeliveries([{ id: "d", heldBy: "wakers", heldWakers: [{ id: "  ", source: "x" }, { id: " w1 ", source: " github " }] }]);
    expect(blank?.[0].heldWakers).toEqual([{ id: "w1", source: "github", timer: false, builtin: false }]);
    expect(heldNames({ state: "open", heldBy: "person", heldWakers: [{ id: "w1", source: "x", timer: false, builtin: false }] })).toBe("");
    expect(heldNames({ state: "closed", heldBy: "wakers", heldWakers: [{ id: "w1", source: "x", timer: false, builtin: false }] })).toBe("");
  });
  it("words the end of the first turn", () => {
    expect(firstTurnWords({ firstTurn: null })).toBeNull();
    expect(firstTurnWords({ firstTurn: { outcome: "turn-aborted", ms: 1, endedAt: "" } })).toBe("memo.d_turn_aborted");
    expect(firstTurnWords({ firstTurn: { outcome: "", ms: 1, endedAt: "" } })).toBe("memo.d_turn_ended");
  });
  it("reads the new delivery fields, and a null first turn", () => {
    const [d] = readDeliveries([{ id: "d", heldBy: "limit", overdue: true, auto: true, retire: "released", firstTurn: { outcome: "clean", ms: 5 } }])!;
    expect([d.heldBy, d.overdue, d.auto, d.retire, d.firstTurn?.outcome]).toEqual(["limit", true, true, "released", "clean"]);
    expect(readDeliveries([{ id: "d", firstTurn: null }])![0].firstTurn).toBeNull();
  });
});

describe("notes that changed under an open card", () => {
  const held = (id: string, h: string) => memo({ id, state: "held", contentHash: h });
  const key = (id: string) => "t\x00" + id;
  const scope = (k: string) => k.startsWith("t\x00");
  const step = (prev: ReturnType<typeof trackShown>, notes: Memo[]) => trackShown(prev, notes, key, scope);
  const status = (recs: ReturnType<typeof trackShown>, notes: Memo[]) => shownStatus(recs, notes, key);

  it("latches a change, so going back to the first hash does not clear it", () => {
    let r = step(new Map(), [held("1", "h1")]);
    expect(status(r, [held("1", "h1")]).edited.size).toBe(0);
    r = step(r, [held("1", "h2")]);
    r = step(r, [held("1", "h1")]);
    const s = status(r, [held("1", "h1")]);
    expect([...s.edited]).toEqual(["1"]);
    expect([...s.awaiting]).toEqual(["1"]);
  });
  it("drops a confirmation when the hash moves again, even back to the confirmed one", () => {
    let r = step(new Map(), [held("1", "h1")]);
    r = step(r, [held("1", "h2")]);
    r = confirmShown(r, [held("1", "h2")], key);
    expect(status(r, [held("1", "h2")]).awaiting.size).toBe(0);
    r = step(r, [held("1", "h3")]);
    r = step(r, [held("1", "h2")]);
    expect([...status(r, [held("1", "h2")]).awaiting]).toEqual(["1"]);
  });
  it("is idempotent for one list", () => {
    const a = step(step(new Map(), [held("1", "h1")]), [held("1", "h1")]);
    expect(status(a, [held("1", "h1")]).edited.size).toBe(0);
  });
  it("keeps a record while the note is pending or not listed as held, and forgets it when it is gone or consumed", () => {
    let r = step(new Map(), [held("1", "h1")]);
    r = step(r, [memo({ id: "1", state: "pending" })]);
    expect(r.size).toBe(1);
    r = step(r, [held("1", "h9")]);
    expect([...status(r, [held("1", "h9")]).awaiting]).toEqual(["1"]);
    expect(step(r, [memo({ id: "1", state: "delivered" })]).size).toBe(0);
    expect(step(r, []).size).toBe(0);
  });
  it("forgets only what its own tenant owns", () => {
    const other = new Map([["u\x00x", { first: "a", last: "a", changed: true, ack: "" }]]);
    expect(step(other, []).has("u\x00x")).toBe(true);
  });
});

describe("pendingCount", () => {
  it("counts the messages a fire could take", () => {
    expect(pendingCount([memo({ state: "pending" }), memo({ state: "held" }), memo({ state: "pending", sentAt: "x" })])).toBe(1);
  });
});

describe("waiting (why nothing starts)", () => {
  const read = (waiting: unknown) => readMemoQueues({ queues: [{ categoryId: "c1", waiting }] })!.queues[0];
  const at = (iso: string) => `<${iso}>`;
  const kind = (k: string) => `kind:${k}`;

  it("is absent from an older Control Plane and from a queue that can run", () => {
    expect(read(undefined).waiting).toBeUndefined();
    expect(read(null).waiting).toBeUndefined();
    expect(waitingWords(read(undefined), at, kind)).toBeNull();
  });

  it("drops a reason it does not know", () => {
    expect(read({ reason: "from_the_future" }).waiting).toBeUndefined();
    expect(read({ reason: 5 }).waiting).toBeUndefined();
  });

  it("keeps the time and the detail", () => {
    expect(read({ reason: "below_threshold", detail: { needed: 5, waiting: 2 } }).waiting).toMatchObject({
      reason: "below_threshold",
      detail: { needed: 5, waiting: 2 },
    });
    expect(read({ reason: "daily_cap", until: "2026-11-16T09:30:00Z" }).waiting?.until).toBe("2026-11-16T09:30:00Z");
  });

  it("leaves the pause and the workspace to restart to their own notices", () => {
    expect(waitingWords(read({ reason: "paused" }), at, kind)).toBeNull();
    expect(waitingWords(read({ reason: "no_report" }), at, kind)).toBeNull();
  });

  it("uses the line with a time only when a usable one is known", () => {
    expect(waitingWords(read({ reason: "daily_cap", until: "2026-11-16T09:30:00Z" }), at, kind)).toEqual({
      key: "memo.waiting_cap_at",
      params: { at: "<2026-11-16T09:30:00Z>" },
    });
    expect(waitingWords(read({ reason: "daily_cap", until: "garbage" }), at, kind)?.key).toBe("memo.waiting_cap");
    expect(waitingWords(read({ reason: "usage_limit", detail: { agentKind: "codex" } }), at, kind)?.params).toEqual({ agent: "kind:codex" });
  });
});

describe("notes that wait for others", () => {
  const m = (id: string, over: Partial<Memo> = {}): Memo => ({
    id, repo: "app", category: "run", kind: "text", body: "job " + id, refPath: "", position: 0, createdAt: "2026-11-19T00:00:00Z", sentAt: "",
    state: "pending", ...over,
  });
  it("offers the notes before it in the category's order, never itself, a later one or a replaced one", () => {
    const memos = [
      m("c", { position: 2 }),
      m("a", { position: 0, reference: { provider: "github", key: "acme/app#1", url: "u" } }),
      m("b", { position: 1, state: "superseded" }),
      m("self", { position: 3 }),
      m("later", { position: 1, createdAt: "2026-11-19T00:00:01Z" }),
      m("other", { position: 0, category: "elsewhere" }),
      m("plain", { position: 0, state: "" }),
    ];
    expect(afterChoices(memos, memos.find((x) => x.id === "self")!)).toEqual([
      { id: "a", label: "acme/app#1" },
      { id: "c", label: "job c" },
    ]);
  });
  it("words each standing, and falls back to the state for a reason it does not know", () => {
    expect(afterItemWords({ state: "satisfied" })).toBe("memo.after_r_done");
    expect(afterItemWords({ state: "waiting", reason: "first_turn" })).toBe("memo.after_r_first_turn");
    expect(afterItemWords({ state: "failed", reason: "from_the_future" })).toBe("memo.after_r_failed");
    expect(afterItemWords({ state: "waiting", reason: "from_the_future" })).toBe("memo.after_r_waiting");
  });
  it("names the pull requests a note waits for, and only the ones not merged yet", () => {
    const pr = (key: string, state: string) => ({ provider: "github", key, state });
    expect(afterItemMessage({ state: "waiting", reason: "pr", prs: [pr("o/r#7", "merged"), pr("o/r#12", "open"), pr("o/r#13", "")] })).toEqual({
      key: "memo.after_r_pr_merge",
      params: { prs: "#12, #13" },
    });
    // Not read yet: no PR to name.
    expect(afterItemMessage({ state: "waiting", reason: "pr" })).toEqual({ key: "memo.after_r_pr" });
    expect(afterItemMessage({ state: "failed", reason: "pr_closed", prs: [pr("o/r#7", "merged"), pr("o/r#9", "closed")] })).toEqual({
      key: "memo.after_r_pr_closed_named",
      params: { prs: "#9" },
    });
    expect(afterItemMessage({ state: "failed", reason: "session_gone" })).toEqual({ key: "memo.after_r_session_gone" });
    expect(afterItemMessage({ state: "failed", reason: "no_pr" })).toEqual({ key: "memo.after_r_no_pr" });
  });
  it("says why nothing starts when every waiting note waits for another, and counts them in a threshold", () => {
    const at = (s: string) => s;
    const kind = (k: string) => k;
    const read = (waiting: unknown) => readMemoQueues({ tenantEnabled: true, queues: [{ categoryId: "c", waiting }] })!.queues[0];
    expect(waitingWords(read({ reason: "prerequisites", detail: { blocked: 3 } }), at, kind)).toEqual({
      key: "memo.waiting_prerequisites",
      params: { count: 3 },
    });
    expect(waitingWords(read({ reason: "below_threshold", detail: { needed: 3, waiting: 1, blocked: 2 } }), at, kind)).toEqual({
      key: "memo.waiting_below_threshold_blocked",
      params: { needed: 3, count: 1, blocked: 2 },
    });
    expect(waitingWords(read({ reason: "below_threshold", detail: { needed: 3, waiting: 1 } }), at, kind)?.key).toBe("memo.waiting_below_threshold");
  });
});
