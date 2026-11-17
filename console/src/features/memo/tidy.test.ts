// The pure side of tidying before a run (ADR 0137 section 8): the settings, the reading of what the
// Control Plane answers, the marks on a message, the words for a run and for an undo that is refused.
import { describe, expect, it } from "vitest";
import { t } from "../../lib/i18n/index.ts";
import type { Memo } from "../../types/memo.ts";
import {
  QUEUE_TIDY_DESTINATIONS_MAX,
  draftBody,
  draftErrors,
  draftOf,
  isSuperseded,
  readMemoQueues,
  readTidyRuns,
  releasedByTidy,
  tidyBlockedGroups,
  tidyCountMarks,
  tidyMarks,
  tidyReasonKey,
  tidyStateKey,
  undoRefusal,
  waitingWords,
} from "./queues.ts";

const queue = (o: Record<string, unknown>) => readMemoQueues({ queues: [{ categoryId: "c1", ...o }] })!.queues[0];

describe("the tidy settings", () => {
  it("are off with no destinations for a row that never had them", () => {
    const q = queue({});
    expect([q.tidy, q.tidyDestinations, q.noTidyAt]).toEqual([false, [], ""]);
    const d = draftOf(undefined);
    expect([d.tidy, d.tidyDestinations]).toEqual([false, []]);
  });
  it("are read from the row, a null list included", () => {
    const q = queue({ tidy: true, tidyDestinations: ["a", 3, "", "b"], noTidyAt: "2026-11-17T00:00:00Z" });
    expect([q.tidy, q.tidyDestinations, q.noTidyAt]).toEqual([true, ["a", "b"], "2026-11-17T00:00:00Z"]);
    expect(queue({ tidy: true, tidyDestinations: null }).tidyDestinations).toEqual([]);
  });
  it("are always sent with the form's value, the destinations too while the switch is off", () => {
    const d = { ...draftOf(undefined), instruction: "x", tidy: false, tidyDestinations: ["a"] };
    expect(draftBody(d, "r")).toMatchObject({ tidy: false, tidyDestinations: ["a"] });
    expect(draftBody({ ...d, tidy: true }, "r")).toMatchObject({ tidy: true, tidyDestinations: ["a"] });
  });
  it("allow at most the Control Plane's number of destinations", () => {
    const ids = (n: number) => Array.from({ length: n }, (_, i) => "c" + i);
    const d = { ...draftOf(undefined), instruction: "x" };
    expect(draftErrors({ ...d, tidyDestinations: ids(QUEUE_TIDY_DESTINATIONS_MAX) }).tidyDestinations).toBeUndefined();
    expect(draftErrors({ ...d, tidyDestinations: ids(QUEUE_TIDY_DESTINATIONS_MAX + 1) }).tidyDestinations).toBe("memo.q_err_tidy_destinations");
  });
});

describe("the waiting reasons of a tidy", () => {
  const at = (s: string) => s;
  const kind = (s: string) => s;
  it("reads tidying and no_tidy, which an older Console would have dropped", () => {
    expect(queue({ waiting: { reason: "tidying" } }).waiting?.reason).toBe("tidying");
    expect(queue({ waiting: { reason: "no_tidy" } }).waiting?.reason).toBe("no_tidy");
    expect(queue({ waiting: { reason: "something_new" } }).waiting).toBeUndefined();
  });
  it("says tidying, and leaves no_tidy to its notice so that it is not said twice", () => {
    expect(waitingWords(queue({ waiting: { reason: "tidying" } }), at, kind)?.key).toBe("memo.waiting_tidying");
    expect(waitingWords(queue({ waiting: { reason: "no_tidy" } }), at, kind)).toBeNull();
  });
});

describe("the marks of a tidy on a message", () => {
  const m = (o: Partial<Memo>): Memo => ({ id: "m", repo: "", category: "c", kind: "text", body: "", refPath: "", position: 0, createdAt: "", sentAt: "", ...o });
  it("says how many notes a derived message was made from and whose they were", () => {
    expect(tidyMarks(m({ derivedFrom: ["a", "b", "c"], sourceProvenance: "member" }))).toMatchObject([{ key: "memo.tidy_derived_member", params: { count: 3 } }]);
    expect(tidyMarks(m({ derivedFrom: ["a", "b"], sourceProvenance: "workspace", producer: "w1" }))).toMatchObject([
      { key: "memo.tidy_derived_session", params: { count: 2, name: "w1" } },
    ]);
    expect(tidyMarks(m({ derivedFrom: ["a", "b"], sourceProvenance: "mcp" }))[0].key).toBe("memo.tidy_derived_mcp_unknown");
    expect(tidyMarks(m({ derivedFrom: ["a", "b"] }))[0].key).toBe("memo.tidy_derived");
  });
  it("names the category a message was moved from, and still says it when the name is not known", () => {
    expect(tidyMarks(m({ tidyFrom: { id: "x", repo: "r", name: "inbox" } }))).toMatchObject([{ key: "memo.tidy_moved_from", params: { name: "r / inbox" } }]);
    expect(tidyMarks(m({ tidyFrom: { id: "x", repo: "", name: "inbox" } }))[0].params).toEqual({ name: "inbox" });
    expect(tidyMarks(m({ tidyFrom: { id: "x", repo: "", name: "" } }))[0].key).toBe("memo.tidy_moved_unknown");
  });
  it("says nothing of a plain message", () => {
    expect(tidyMarks(m({}))).toEqual([]);
    expect(tidyMarks(m({ derivedFrom: [] }))).toEqual([]);
  });
  it("marks a pending message a run released, and a replaced one as read-only", () => {
    expect(releasedByTidy({ state: "pending", releasedBy: "tidy" })).toBe(true);
    expect(releasedByTidy({ state: "held", releasedBy: "tidy" })).toBe(false);
    expect(releasedByTidy({ state: "pending", releasedBy: "setting" })).toBe(false);
    expect(isSuperseded({ state: "superseded" })).toBe(true);
    expect(isSuperseded({ state: "delivered" })).toBe(false);
  });
});

describe("the run history", () => {
  it("rebuilds every list, drops a run without an id and answers null for a failed read", () => {
    const w = readTidyRuns({ runs: [{ id: "r1", state: "applied", merged: 2, sourceIds: null, blocked: [{ id: "a", why: "sent" }, 5] }, { state: "x" }], usedToday: 3, dailyCap: 10 })!;
    expect(w.runs).toHaveLength(1);
    expect(w.runs[0]).toMatchObject({ id: "r1", merged: 2, sourceIds: [], blocked: [{ id: "a", why: "sent" }], undoable: false });
    expect([w.usedToday, w.dailyCap]).toEqual([3, 10]);
    expect(readTidyRuns({ runs: null })?.runs).toEqual([]);
    expect(readTidyRuns({ error: { code: "x" } })).toBeNull();
    expect(readTidyRuns(null)).toBeNull();
  });
  it("has a member's word for each state and for each reason a run can end with", () => {
    for (const s of ["running", "applied", "kept", "dropped", "skipped", "failed", "undone"]) {
      const k = tidyStateKey(s);
      expect(k, s).not.toBeNull();
      expect(t(k!), s).not.toBe(k);
    }
    for (const reason of ["stale_input", "no_queue", "takeover", "no_marker", "agent_unreachable", "wake_failed", "no_workspace", "timeout", "call_failed", "agent_error", "bad_shape", "text_growth", "bad_destination", "already_moved"]) {
      const k = tidyReasonKey({ state: "dropped", reason });
      expect(k, reason).not.toBeNull();
      expect(t(k!), reason).not.toBe(k);
    }
  });
  it("says nothing for an unknown reason and for a run that did change something", () => {
    expect(tidyReasonKey({ state: "failed", reason: "brand_new" })).toBeNull();
    expect(tidyReasonKey({ state: "applied", reason: "" })).toBeNull();
    expect(tidyReasonKey({ state: "undone", reason: "stale_input" })).toBeNull();
  });
  it("counts only what a run did, and says how many of the moved ones wait for approval", () => {
    expect(tidyCountMarks({ merged: 2, split: 0, moved: 0, held: 0 }).map((c) => c.key)).toEqual(["memo.tidy_n_merged"]);
    expect(tidyCountMarks({ merged: 0, split: 1, moved: 3, held: 1 })).toMatchObject([{ key: "memo.tidy_n_split" }, { key: "memo.tidy_n_moved_held", params: { count: 3, held: 1 } }]);
  });
});

describe("an undo the Control Plane refused", () => {
  const refused = { error: { code: "memo_tidy_not_undoable", blocked: [{ id: "a", why: "claimed" }, { id: "b", why: "claimed" }, { id: "c", why: "later_run" }] } };
  it("reads the blocked messages from error.blocked, and only for that code", () => {
    expect(undoRefusal(refused)).toEqual([{ id: "a", why: "claimed" }, { id: "b", why: "claimed" }, { id: "c", why: "later_run" }]);
    expect(undoRefusal({ error: { code: "not_found" } })).toBeNull();
    expect(undoRefusal({ run: {} })).toBeNull();
    expect(undoRefusal({ error: { code: "memo_tidy_not_undoable" } })).toEqual([]);
  });
  it("groups them by reason, and reads a reason it does not know as changed", () => {
    expect(tidyBlockedGroups(undoRefusal(refused)!)).toEqual([
      { why: "claimed", key: "memo.tidy_block_claimed", count: 2 },
      { why: "later_run", key: "memo.tidy_block_later_run", count: 1 },
    ]);
    expect(tidyBlockedGroups([{ id: "a", why: "brand_new" }])[0].key).toBe("memo.tidy_block_changed");
  });
  it("tells the member to undo the newer run first when a later run used a result", () => {
    expect(t("memo.tidy_block_later_run", { count: 1 })).toMatch(/新しい整理から先に/);
  });
});
