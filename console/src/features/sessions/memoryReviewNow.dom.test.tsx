// reviewSessionNow (ADR 0117): asks the Agent to start, then watches the session's review status
// until the manual review ends, and says how it ended. The status carries counts and states only.
import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const { reviewSessionNow } = await import("./memoryReviewNow.ts");
const { t } = await import("../../lib/i18n/index.ts");

const notify = vi.fn();
const noSleep = async () => {};

beforeEach(() => {
  apiMock.mockReset();
  apiJSONMock.mockReset();
  notify.mockReset();
});

const status = (session: object) => ({ mode: "propose", enabled: true, pending: 0, session });
const manual = (at: string, outcome: string) => ({ at, outcome });

describe("reviewSessionNow", () => {
  it("asks for this session, with the session in the query for the audit ledger too", async () => {
    apiMock.mockResolvedValue(status({ running: false }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("sk 1/x", notify, noSleep);
    expect(apiJSONMock).toHaveBeenCalledWith("api/agents/memory-review/run?session=sk%201%2Fx", "POST", { session: "sk 1/x" });
  });

  it("says how a finished review ended: proposals queued", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false, manual: manual("T0", "done") })) // before
      .mockResolvedValueOnce(status({ running: true, manual: manual("T0", "done") })) // still running
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T1", "done"), windows: 1, queued: 2 } }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenNthCalledWith(1, t("srow.memory_review_started"), { kind: "info" });
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_queued", { n: 2 }), { kind: "success" });
  });

  it("does not take the previous review's outcome for this one", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T0", "done"), queued: 5 } }))
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T0", "done"), queued: 5 } })) // not ended yet
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T1", "done"), windows: 1, queued: 0 } }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_none"), { kind: "info" });
  });

  // The totals are over every window the review worked through, not the last run's: a first window
  // that queued a proposal followed by an empty one is not "nothing worth keeping".
  it("reports the total over several windows, and keeps what was queued before a refusal", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T1", "done"), windows: 2, queued: 1 } }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_queued", { n: 1 }), { kind: "success" });
    expect(notify).not.toHaveBeenCalledWith(t("srow.memory_review_none"), expect.anything());

    notify.mockReset();
    apiMock.mockReset();
    apiMock
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T1", "daily_cap"), windows: 1, queued: 2 } }));
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenCalledWith(t("srow.memory_review_queued", { n: 2 }), { kind: "success" });
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_daily_cap"));

    notify.mockReset();
    apiMock.mockReset();
    apiMock
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T1", "done"), windows: 2, queued: 1, failed: 1 } }));
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenCalledWith(t("srow.memory_review_queued", { n: 1 }), { kind: "success" });
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_failed"), { kind: "warn" });
  });

  it("answers a review that found nothing new, without a run record changing", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false, manual: manual("T0", "done") }))
      .mockResolvedValueOnce(status({ running: false, manual: manual("T1", "empty") }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_empty"), { kind: "info" });
    expect(apiMock).toHaveBeenCalledTimes(2);
  });

  it("answers an unreadable conversation", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false, manual: manual("T1", "unreadable") }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_unreadable"), { kind: "warn" });
  });

  it("says a review whose windows got nothing usable failed", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false, manual: { ...manual("T1", "done"), windows: 1, failed: 1 } }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_failed"), { kind: "warn" });
  });

  it("names the reason a review stopped", async () => {
    apiMock
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false, manual: manual("T1", "no_backend") }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_no_backend"));
  });

  it("explains the Agent's refusal to start without a deny-all backend", async () => {
    apiMock.mockResolvedValue(status({ running: false }));
    apiJSONMock.mockResolvedValue({ error: { code: "memory_review_no_backend" } });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenLastCalledWith(t("srow.memory_review_no_backend"));
    expect(apiMock).toHaveBeenCalledTimes(1);
  });

  it("explains a refusal to start and polls nothing", async () => {
    apiMock.mockResolvedValue(status({ running: false }));
    for (const [code, key] of [
      ["memory_review_off", "srow.memory_review_off"],
      ["memory_review_busy", "srow.memory_review_busy"],
    ] as const) {
      notify.mockReset();
      apiMock.mockClear();
      apiJSONMock.mockResolvedValue({ error: { code, message: "x" } });
      await reviewSessionNow("a", notify, noSleep);
      expect(notify).toHaveBeenCalledTimes(1);
      expect(notify).toHaveBeenCalledWith(t(key));
      expect(apiMock).toHaveBeenCalledTimes(1); // only the status read before starting
    }
  });

  it("gives up watching after a while without claiming a result", async () => {
    apiMock.mockResolvedValue(status({ running: true }));
    apiJSONMock.mockResolvedValue({ started: true });
    await reviewSessionNow("a", notify, noSleep);
    expect(notify).toHaveBeenCalledTimes(2); // "started", then that it is still running
    expect(notify).toHaveBeenNthCalledWith(2, t("srow.memory_review_still_running"), { kind: "info" });
  });
});
