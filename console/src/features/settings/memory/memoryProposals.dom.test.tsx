// The memory consolidator's screens (ADR 0130 decisions 6 and 7). Pinned here: the switch is off by
// default and writes the ui-pref; the status shows the last run, the next possible run and the
// delayed topics with causes; the list filters; advice-only proposals have no Approve; Approve asks
// first, goes through the signed relay path with the proposal id in the query, and the stale and
// expired refusals read as sentences; nothing is decided without a click.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { ProposalsPanel, ConsolidationSettings, parseGoDuration, isAdviceOnly, matchesFilter, canRetry } from "./memoryProposals.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { getSettings, setSetting } from "../../../lib/settings.ts";
import { setLocale, t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const base = {
  topicId: "t",
  scope: "user",
  created: "2026-11-09T01:00:00Z",
  expires: "2099-11-23T01:00:00Z",
  observationIds: ["o1", "o2"],
  targetRevisions: { branches: 3 },
  evidence: [{ id: "e1", source: "feedback", memory: "branches", rule: "stale", note: "trunk is main now" }],
};
const patchItem = { op: "patch", name: "branches", revision: 3, reason: "trunk renamed", edits: [{ old: "develop", new: "main" }] };
const proposals = [
  { ...base, id: "a".repeat(20), memory: "branches", status: "pending", items: [patchItem] },
  { ...base, id: "b".repeat(20), memory: "tests", status: "pending", items: [{ op: "note", name: "tests", advice: "merge", targets: ["tests", "ci"], reason: "same rule twice" }] },
  { ...base, id: "c".repeat(20), memory: "old", status: "rejected", items: [patchItem] },
  { ...base, id: "d".repeat(20), memory: "gone", status: "expired", items: [patchItem] },
];
const viewOf = (p: (typeof proposals)[number], stale = "") => ({
  ...p,
  itemViews: p.items.map((i) => ({ ...i, diff: i.op === "patch" ? "-develop\n+main" : undefined, stale: i.op === "patch" ? stale : undefined })),
});
const status = {
  enabled: true,
  config: { idleAfter: "15m0s", minInterval: "3h0m0s", proposalTTL: "336h0m0s" },
  lastRun: { id: "r", started: "2026-11-09T01:00:00Z", finished: "2026-11-09T01:01:00Z", status: "cut", ripe: 2, queued: [], delayed: [] },
  trigger: { lastRun: "2099-01-01T00:00:00Z" },
  counts: { pending: 2 },
  waiting: [{ topic: "tp", scope: "user", memory: "docs", cause: "budget" }],
};

let stale = "";
function route(path: string) {
  if (path === "api/agents/memory-consolidation") return Promise.resolve(status);
  if (path === "api/agents/memory-consolidation/proposals") return Promise.resolve({ proposals });
  const m = /proposal\?id=(\w+)/.exec(path);
  if (m) return Promise.resolve(viewOf(proposals.find((p) => p.id === m[1])!, stale));
  return Promise.resolve({});
}

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>{node}</ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}
const buttons = () => Array.from(document.body.querySelectorAll<HTMLButtonElement>("button"));
const byText = (s: string) => buttons().find((b) => b.textContent === s);
const click = async (b: HTMLButtonElement | undefined) => {
  expect(b).toBeTruthy();
  await act(async () => b!.click());
  await flush();
};
const confirmButton = () => document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:last-child");
const rows = () => Array.from(host!.querySelectorAll(".mem-list .mem-snap"));

beforeEach(() => {
  setLocale("en");
  stale = "";
  api.mockImplementation(route);
  apiJSON.mockResolvedValue({});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
  setSetting("memoryConsolidation", false);
});

describe("the pure helpers", () => {
  it("reads Go durations, advice-only, the filters and retry", () => {
    expect(parseGoDuration("3h0m0s")).toBe(3 * 3600e3);
    expect(parseGoDuration("1m30.5s")).toBe(90500);
    expect(parseGoDuration("")).toBe(0);
    expect(isAdviceOnly({ items: [{ op: "note", name: "x" }] })).toBe(true);
    expect(isAdviceOnly({ items: [{ op: "note", name: "x" }, { op: "patch", name: "y" }] })).toBe(false);
    expect(matchesFilter("refused", "decided")).toBe(true);
    expect(matchesFilter("pending", "decided")).toBe(false);
    expect(canRetry("pending")).toBe(false);
    expect(canRetry("expired")).toBe(true);
  });
});

describe("ConsolidationSettings", () => {
  it("is off by default and the switch writes the ui-pref", async () => {
    expect(getSettings().memoryConsolidation).toBe(false);
    await mount(<ConsolidationSettings running />);
    expect(host!.querySelector(".mem-cs-status")).toBeNull();
    await click(buttons().find((b) => b.textContent === t("common.on")));
    expect(getSettings().memoryConsolidation).toBe(true);
  });

  it("shows the last run, the next possible run and the delayed topics with their cause", async () => {
    setSetting("memoryConsolidation", true);
    await mount(<ConsolidationSettings running />);
    const text = host!.querySelector(".mem-cs-status")!.textContent!;
    expect(text).toContain(t("mem.cs_run_cut"));
    expect(text).toContain("docs");
    expect(text).toContain(t("mem.cs_cause_budget"));
    expect(text).toContain(t("mem.cs_pending_count", { n: 2 }));
    // The last run is far in the future in this fixture, so the next run is a dated lower bound.
    expect(text).toContain(t("mem.cs_next_rule", { idle: "15 min" }));
  });
});

describe("the manual run", () => {
  const RUN = "api/agents/memory-consolidation/run";
  const runStatus = (run: object, extra: object = {}) => ({
    ...status,
    config: { ...status.config, tokenBudget: 60000 },
    run,
    ...extra,
  });
  const runButton = () => byText(t("mem.cs_run_button"));

  it("asks first, says it spends tokens and states the budget, then posts once", async () => {
    setSetting("memoryConsolidation", true);
    api.mockImplementation((p: string) => (p === "api/agents/memory-consolidation" ? Promise.resolve(runStatus({ running: false, done: 0, total: 0 })) : route(p)));
    let release: (v: object) => void = () => {};
    apiJSON.mockImplementation(() => new Promise((r) => (release = r)));
    await mount(<ConsolidationSettings running />);
    // Cancel: nothing is sent.
    await click(runButton());
    const body = document.body.textContent!;
    expect(body).toContain(t("mem.cs_run_body"));
    expect(body).toContain(t("mem.cs_run_budget", { n: (60000).toLocaleString() }));
    await click(buttons().find((b) => b.textContent === t("common.cancel")));
    expect(apiJSON).not.toHaveBeenCalled();
    // Confirm: one POST, and a second click while it is in flight sends nothing more.
    await click(runButton());
    await click(confirmButton()!);
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON).toHaveBeenCalledWith(RUN, "POST", {});
    expect(runButton()!.disabled).toBe(true);
    await click(runButton());
    expect(document.body.querySelector(".ui-confirm-actions")).toBeNull();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    await act(async () => release({ started: true }));
    await flush();
  });

  it("sends all=true only when the option is ticked", async () => {
    setSetting("memoryConsolidation", true);
    api.mockImplementation((p: string) => (p === "api/agents/memory-consolidation" ? Promise.resolve(runStatus({ running: false, done: 0, total: 0 })) : route(p)));
    await mount(<ConsolidationSettings running />);
    await act(async () => host!.querySelector<HTMLInputElement>(".mem-cs-run input")!.click());
    await click(runButton());
    expect(document.body.textContent).toContain(t("mem.cs_run_body_all"));
    await click(confirmButton()!);
    expect(apiJSON).toHaveBeenCalledWith(RUN, "POST", { all: true });
  });

  it("is disabled and shows progress while running, and polls until it ends", async () => {
    vi.useFakeTimers();
    try {
      setSetting("memoryConsolidation", true);
      let run: object = { running: true, manual: true, done: 1, total: 3 };
      api.mockImplementation((p: string) => (p === "api/agents/memory-consolidation" ? Promise.resolve(runStatus(run)) : route(p)));
      await mount(<ConsolidationSettings running />);
      expect(runButton()!.disabled).toBe(true);
      expect(host!.querySelector(".mem-cs-status")!.textContent).toContain(t("mem.cs_running_progress", { n: 2, m: 3 }));
      const calls = api.mock.calls.length;
      run = { running: false, done: 0, total: 0 };
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3100);
      });
      expect(api.mock.calls.length).toBeGreaterThan(calls);
      expect(runButton()!.disabled).toBe(false);
      // Polling stops once nothing runs.
      const after = api.mock.calls.length;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(api.mock.calls.length).toBe(after);
    } finally {
      vi.useRealTimers();
    }
  });

  it("words the 409 refusals", async () => {
    setSetting("memoryConsolidation", true);
    api.mockImplementation((p: string) => (p === "api/agents/memory-consolidation" ? Promise.resolve(runStatus({ running: false, done: 0, total: 0 })) : route(p)));
    for (const code of ["run_in_flight", "disabled"]) {
      apiJSON.mockResolvedValueOnce({ error: { code, message: "raw" } });
      await mount(<ConsolidationSettings running />);
      await click(runButton());
      await click(confirmButton()!);
      expect(document.body.textContent).toContain(t(code === "disabled" ? "mem.cs_err_disabled" : "mem.cs_err_run_in_flight"));
      act(() => root?.unmount());
      host?.remove();
      document.body.innerHTML = "";
    }
  });
});

describe("ProposalsPanel", () => {
  it("lists pending proposals first and filters decided and expired ones", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    expect(rows()).toHaveLength(2);
    await click(buttons().find((b) => b.textContent === t("mem.cs_filter_decided", { n: 1 })));
    expect(rows()).toHaveLength(1);
    expect(rows()[0].textContent).toContain("old");
    await click(buttons().find((b) => b.textContent === t("mem.cs_filter_expired", { n: 1 })));
    expect(rows()[0].textContent).toContain("gone");
    await click(buttons().find((b) => b.textContent === t("mem.cs_filter_all", { n: 4 })));
    expect(rows()).toHaveLength(4);
  });

  it("shows the target revision, the diff and the evidence of a proposal", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    const text = host!.querySelector(".mem-cs-detail")!.textContent!;
    expect(text).toContain("branches@3");
    expect(text).toContain("trunk renamed");
    expect(text).toContain("trunk is main now");
    expect(host!.querySelector(".diff")).toBeTruthy();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("shows advice-only proposals as advice, with no Approve", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    await click(rows()[1] as HTMLButtonElement);
    expect(byText(t("mem.cs_approve"))).toBeUndefined();
    expect(byText(t("mem.cs_reject"))).toBeTruthy();
    expect(host!.querySelector(".mem-cs-detail")!.textContent).toContain(t("mem.cs_advice_only"));
  });

  it("approves only after the member confirms, through the id-in-query relay path", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    await click(byText(t("mem.cs_approve")));
    expect(apiJSON).not.toHaveBeenCalled();
    await click(confirmButton() ?? undefined);
    const id = "a".repeat(20);
    expect(apiJSON).toHaveBeenCalledWith(`api/agents/memory-consolidation/proposals/approve?proposal=${id}`, "POST", { id });
  });

  it("cancelling the confirm approves nothing", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    await click(byText(t("mem.cs_approve")));
    await click(document.body.querySelector<HTMLButtonElement>(".ui-confirm-actions button:first-child") ?? undefined);
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("says a stale revision in words, not as a generic error", async () => {
    apiJSON.mockResolvedValue({ error: { code: "stale", message: "stale: the memory is not as it was" } });
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    await click(byText(t("mem.cs_approve")));
    await click(confirmButton() ?? undefined);
    expect(document.body.textContent).toContain(t("mem.cs_err_stale"));
    expect(document.body.textContent).not.toContain("stale: the memory is not as it was");
  });

  it("says an expired or decided proposal in words", async () => {
    apiJSON.mockResolvedValue({ error: { code: "not_pending", message: "the proposal was decided or has expired" } });
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    await click(byText(t("mem.cs_approve")));
    await click(confirmButton() ?? undefined);
    expect(document.body.textContent).toContain(t("mem.cs_err_not_pending"));
  });

  it("offers no Approve for a proposal whose revision is already known to be stale", async () => {
    stale = "the memory is gone or withheld";
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    expect(byText(t("mem.cs_approve"))?.disabled).toBe(true);
    expect(host!.textContent).toContain(t("mem.cs_err_stale"));
  });

  it("retries a rejected or expired proposal and has no approve for it", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    await click(buttons().find((b) => b.textContent === t("mem.cs_filter_expired", { n: 1 })));
    expect(byText(t("mem.cs_approve"))).toBeUndefined();
    expect(host!.querySelector(".mem-cs-detail")!.textContent).toContain(t("mem.cs_err_not_pending_expired"));
    await click(byText(t("mem.cs_retry")));
    const id = "d".repeat(20);
    expect(apiJSON).toHaveBeenCalledWith(`api/agents/memory-consolidation/proposals/retry?proposal=${id}`, "POST", { id });
  });

  it("sends one request for a double-clicked Reject", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    const b = byText(t("mem.cs_reject"))!;
    await act(async () => {
      b.click();
      b.click();
    });
    await flush();
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("offers Look again for a partly applied proposal", () => {
    expect(canRetry("partial")).toBe(true);
    expect(canRetry("approved")).toBe(false);
  });

  it("has no bulk action", async () => {
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    expect(buttons().filter((b) => /all/i.test(b.textContent ?? "") && /approve/i.test(b.textContent ?? ""))).toHaveLength(0);
  });
});
