// Pure-function tests for the engine indicator wire (ADR 0084). The DOM half
// (EnginesPill.dom.test.tsx) covers rendering; this file covers the decisions that do not
// need a browser: what survives readEngines, which row wins the headline, and the
// queue-count honesty guard.
import { describe, expect, it } from "vitest";
import {
  groupByRole,
  inUse,
  lastUsedSecs,
  pickHeadRow,
  queueIsCertain,
  readEngines,
  secsUntil,
  showsColdHint,
  stateWord,
  stopSecs,
  totalQueue,
  type EngineMemberRow,
} from "./wire.ts";

const row = (patch: Partial<EngineMemberRow>): EngineMemberRow => ({ key: "image", api: "images", ...patch });

describe("readEngines", () => {
  it("keeps a well-formed row (positive control for the normalizer below)", () => {
    const rows = readEngines({ engines: [{ key: "image", api: "images", state: "running", warm: true }] });
    expect(rows).toEqual([{ key: "image", api: "images", state: "running", warm: true }]);
  });

  it("drops a row missing key or api rather than crashing the pill", () => {
    expect(readEngines({ engines: [{ api: "images" }, { key: "x" }, { key: "y", api: "bogus" }] })).toEqual([]);
  });

  it("returns null (not []) on a CP error envelope or garbage, so the caller keeps stale rows", () => {
    expect(readEngines({ error: { code: "unavailable" } })).toBeNull();
    expect(readEngines(null)).toBeNull();
    expect(readEngines({ engines: "nope" })).toBeNull();
  });

  it("keeps the warm model and its label (positive control for the absence below)", () => {
    const rows = readEngines({
      engines: [{ key: "llm", api: "chat", warm: true, warm_model: "qwen3.8-27b-ud-iq4_xs", warm_model_label: "Qwen3.8 27B IQ4_XS" }],
    });
    expect(rows![0].warm_model).toBe("qwen3.8-27b-ud-iq4_xs");
    expect(rows![0].warm_model_label).toBe("Qwen3.8 27B IQ4_XS");
  });

  it("never invents a field the wire omitted", () => {
    const [r] = readEngines({ engines: [{ key: "llm", api: "chat" }] })!;
    expect(r.state).toBeUndefined();
    expect(r.warm).toBeUndefined();
    expect(r.warm_model).toBeUndefined();
    expect(r.warm_model_label).toBeUndefined();
    expect(r.stop_eta).toBeUndefined();
    expect(r.idle_secs).toBeUndefined();
    expect(r.lifecycle).toBeUndefined();
    expect(r.queue).toBeUndefined();
  });
});

describe("groupByRole", () => {
  it("keeps ADR-fixed role order (chat then images) regardless of wire order", () => {
    const rows = [row({ api: "images", key: "image" }), row({ api: "chat", key: "llm" })];
    expect([...groupByRole(rows).keys()]).toEqual(["chat", "images"]);
  });

  it("omits a role with zero rows — decision 5's single rule for all four hide conditions", () => {
    expect(groupByRole([row({ api: "images" })]).has("chat")).toBe(false);
  });
});

describe("pickHeadRow (decision 11 — the best state wins the headline)", () => {
  it("a warm row wins over a running one that is not warm", () => {
    const warm = row({ key: "a", state: "running", warm: true });
    const plain = row({ key: "b", state: "running" });
    expect(pickHeadRow([plain, warm])).toBe(warm);
  });

  it("running beats starting beats stopping beats stopped", () => {
    const stopped = row({ key: "a", state: "stopped" });
    const starting = row({ key: "b", state: "starting" });
    const running = row({ key: "c", state: "running" });
    expect(pickHeadRow([stopped, starting, running])).toBe(running);
  });

  it("a warm row in use wins over a warm row nobody is using", () => {
    const busy = row({ key: "a", state: "running", warm: true, queue: { count: 1 } });
    const idle = row({ key: "b", state: "running", warm: true });
    expect(pickHeadRow([idle, busy])).toBe(busy);
  });

  it("a lifecycle row never outranks a self-managed row that is actually running", () => {
    const external = row({ key: "lan", lifecycle: "external", warm: true, state: "running" });
    const managed = row({ key: "self", state: "running" });
    expect(pickHeadRow([external, managed])).toBe(managed);
  });

  it("a lone lifecycle row is still its own head row", () => {
    const external = row({ key: "lan", lifecycle: "external" });
    expect(pickHeadRow([external])).toBe(external);
  });
});

describe("stateWord (decision 4 — lifecycle wins, then warm, then the raw state)", () => {
  it("an external row reads as available even when warm/state ride along", () => {
    expect(stateWord(row({ lifecycle: "external", warm: true, state: "running" }))).toBe("available");
  });

  it("warm reads as ready regardless of the raw state word", () => {
    expect(stateWord(row({ state: "starting", warm: true }))).toBe("ready");
  });

  it("a warm row the CP is holding a request for reads as in use", () => {
    expect(stateWord(row({ state: "running", warm: true, queue: { count: 1 } }))).toBe("in_use");
  });

  it("a warm row with a certain 0 is ready, not in use (the same row, one count apart)", () => {
    expect(stateWord(row({ state: "running", warm: true, queue: { count: 0 } }))).toBe("ready");
  });

  it("an external row in use says so — the count is this deployment's own, unlike its state", () => {
    expect(stateWord(row({ lifecycle: "external", queue: { count: 2 } }))).toBe("in_use");
  });

  it("a cold row somebody is waiting for stays stopped: the queue line says who waits, not the word", () => {
    expect(stateWord(row({ state: "stopped", queue: { count: 1 } }))).toBe("stopped");
  });

  it("inUse ignores a count the counter cannot vouch for", () => {
    expect(inUse(row({ queue: { count: 0, counted_secs: 0 } }))).toBe(false);
    expect(inUse(row({ queue: { count: 1, counted_secs: 0 } }))).toBe(true);
  });

  it.each([
    ["running", "running"],
    ["starting", "starting"],
    ["stopping", "stopping"],
    ["stopped", "stopped"],
  ] as const)("state=%s -> %s", (state, want) => {
    expect(stateWord(row({ state }))).toBe(want);
  });
});

describe("showsColdHint (decision 5 — stopped is shown, not hidden)", () => {
  it("a genuinely stopped, self-managed row gets the hint", () => {
    expect(showsColdHint(row({ state: "stopped" }))).toBe(true);
  });
  it("warm suppresses it even if state is somehow stale", () => {
    expect(showsColdHint(row({ state: "stopped", warm: true }))).toBe(false);
  });
  it("an external row never gets a hint about a cold start this deployment cannot vouch for", () => {
    expect(showsColdHint(row({ lifecycle: "external", state: undefined }))).toBe(false);
  });
});

describe("queueIsCertain / totalQueue (decision 6 ⚠️ — no confident 0 from a young counter)", () => {
  it("a positive count is trusted even when the counter just started (positive control)", () => {
    expect(queueIsCertain({ count: 3, counted_secs: 0 })).toBe(true);
  });
  it("a 0 next to a freshly-replaced counter is held back", () => {
    expect(queueIsCertain({ count: 0, counted_secs: 0 })).toBe(false);
  });
  it("a 0 with no counted_secs at all (no partial marker) is trusted", () => {
    expect(queueIsCertain({ count: 0 })).toBe(true);
  });

  it("totalQueue sums only certain rows, and is undefined when nothing counts", () => {
    const a = row({ key: "a", queue: { count: 2 } });
    const b = row({ key: "b", queue: { count: 0, counted_secs: 0 } }); // uncertain 0, excluded
    const c = row({ key: "c", queue: { count: 5, counted_secs: 30 } });
    expect(totalQueue([a, b, c])).toBe(7);
    expect(totalQueue([row({ key: "d" })])).toBeUndefined();
  });
});

describe("stopSecs (decision 4 — an external/remote row never gets a countdown)", () => {
  it("a self-managed row with stop_eta is a positive control", () => {
    const at = "2026-09-14T00:01:00Z";
    const now = Date.parse("2026-09-14T00:00:00Z");
    expect(stopSecs(row({ stop_eta: at }), now)).toBe(60);
  });
  it("an external row's stop_eta (even if the wire sent one by mistake) is ignored", () => {
    const at = "2026-09-14T00:01:00Z";
    const now = Date.parse("2026-09-14T00:00:00Z");
    expect(stopSecs(row({ lifecycle: "external", stop_eta: at }), now)).toBeNull();
  });
});

describe("lastUsedSecs (derived from stop_eta and idle_secs — decision 2, nothing new on the wire)", () => {
  const now = Date.parse("2026-09-14T00:00:00Z");

  it("an engine stopping in 28 minutes on a 30-minute window was used 2 minutes ago", () => {
    const r = row({ idle_secs: 1800, stop_eta: "2026-09-14T00:28:00Z" });
    expect(lastUsedSecs(r, now)).toBe(120);
  });

  it("is null without a stop_eta (a pinned-on engine has none) rather than 0", () => {
    expect(lastUsedSecs(row({ idle_secs: 1800 }), now)).toBeNull();
  });

  it("is null without an idle window, and for a row this deployment does not manage", () => {
    expect(lastUsedSecs(row({ stop_eta: "2026-09-14T00:28:00Z" }), now)).toBeNull();
    expect(lastUsedSecs(row({ lifecycle: "remote", idle_secs: 1800, stop_eta: "2026-09-14T00:28:00Z" }), now)).toBeNull();
  });

  it("is null, not negative, when the two values came from different ticks", () => {
    expect(lastUsedSecs(row({ idle_secs: 1800, stop_eta: "2026-09-14T00:31:00Z" }), now)).toBeNull();
  });
});

describe("secsUntil", () => {
  it("a positive control: a future instant yields a positive count", () => {
    expect(secsUntil("2026-09-14T00:01:00Z", Date.parse("2026-09-14T00:00:00Z"))).toBe(60);
  });
  it("absent or unparseable is null, never 0", () => {
    expect(secsUntil(undefined, Date.now())).toBeNull();
    expect(secsUntil("not-a-date", Date.now())).toBeNull();
  });
});
