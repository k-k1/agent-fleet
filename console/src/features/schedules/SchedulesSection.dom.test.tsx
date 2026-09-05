// Render test for the schedules rail section's LOAD-FAILURE branch. api() resolves a CP
// error as {error} rather than throwing, and the section used to fold that into an empty
// array — so a 401/5xx rendered as "no scheduled runs yet" and the schedule the user
// had just created looked deleted. This fixes the three states apart: real empty, load
// failed (rows kept), and recovery.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const scheduleList = vi.fn();
const scheduleRuns = vi.fn(async () => ({ runs: [] }));
vi.mock("./api.ts", () => ({
  scheduleList: (...a: unknown[]) => scheduleList(...a),
  scheduleRuns: (...a: unknown[]) => scheduleRuns(...(a as [])),
  schedulePause: vi.fn(),
  scheduleResume: vi.fn(),
  scheduleRunNow: vi.fn(),
  scheduleDelete: vi.fn(),
  scheduleUpdate: vi.fn(),
}));
// The conversation list is only needed to resolve working-set slugs — keep it off the network.
vi.mock("../chat/api.ts", () => ({ chatList: vi.fn(async () => ({ conversations: [] })) }));

const { SchedulesSection } = await import("./SchedulesSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <SchedulesSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const text = () => host.textContent || "";
const rows = () => host.querySelectorAll(".sched-row").length;
const failed = () => !!host.querySelector(".sched-load-err");

beforeEach(() => {
  scheduleList.mockReset();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.useRealTimers();
});

const sched = { id: "sch_1", spec_kind: "cron", spec: "0 9 * * *", spec_label: "morning review", enabled: true };

describe("SchedulesSection load failures", () => {
  it("shows the empty message only for a genuinely empty list", async () => {
    scheduleList.mockResolvedValue([]);
    await render();
    expect(failed()).toBe(false);
    expect(text()).toContain(t("sched.empty"));
  });

  it("never renders a CP error as 'no schedules'", async () => {
    scheduleList.mockResolvedValue({ error: { code: "unauthenticated", message: "no gateway identity" } });
    await render();
    expect(failed()).toBe(true);
    expect(text()).not.toContain(t("sched.empty"));
  });

  it("keeps the rows it already had when a later poll fails", async () => {
    vi.useFakeTimers();
    scheduleList.mockResolvedValue([sched]);
    await render();
    expect(rows()).toBe(1);
    expect(failed()).toBe(false);

    scheduleList.mockResolvedValue({ error: { code: "http_502", message: "bad gateway" } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000); // the next poll
    });
    expect(failed()).toBe(true);
    expect(rows()).toBe(1); // the rows on screen a moment ago are kept

    scheduleList.mockResolvedValue([sched]);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000);
    });
    expect(failed()).toBe(false); // once it recovers the banner just goes away
    expect(rows()).toBe(1);
  });

  it("retries on demand from the failure row", async () => {
    scheduleList.mockResolvedValue({ error: { code: "http_502", message: "bad gateway" } });
    await render();
    expect(scheduleList).toHaveBeenCalledTimes(1);

    scheduleList.mockResolvedValue([sched]);
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".sched-retry")?.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(scheduleList).toHaveBeenCalledTimes(2);
    expect(failed()).toBe(false);
    expect(rows()).toBe(1);
  });
});

// The route in from the notification centre (docs/log/38). A failed scheduled run shows up
// only in a notification, and WHY it did not run is only in that row's run history — so
// reveal must open the section and the history, not merely select the row.
describe("SchedulesSection reveal", () => {
  it("opens the section even when collapsed and loads that row's run history", async () => {
    localStorage.setItem("af-section-schedules", "0"); // the user had it collapsed
    scheduleList.mockResolvedValue([sched]);
    scheduleRuns.mockClear();
    await render();
    expect(host.querySelector(".sched-list")).toBeNull(); // collapsed = no body rendered

    const { useSchedulesStore } = await import("./store.ts");
    await act(async () => {
      useSchedulesStore.getState().revealSchedule("sch_1");
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(host.querySelector(".sched-list")).not.toBeNull();
    expect(scheduleRuns).toHaveBeenCalledWith("sch_1");
    expect(host.querySelector('[data-sched-id="sch_1"]')).not.toBeNull();
    localStorage.removeItem("af-section-schedules");
  });
});
