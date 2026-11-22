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

// Sessions a schedule started are not in the repository tree; this view lists them.
describe("SchedulesSection scheduled sessions", () => {
  it("lists live scheduled runs with their spawned sessions, folds finished ones, and leaves others out", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    const s = (name: string, extra: Record<string, unknown>) => ({ name, title: name, kind: "claude", alive: true, ...extra });
    useSessionsStore.setState({
      sessions: [
        s("cron-live", { origin: "schedule", createdAt: "2026-11-02T00:00:00Z" }),
        s("cron-rev", { origin: "session", originSession: "cron-live", createdAt: "2026-11-02T01:00:00Z" }),
        s("cron-old", { origin: "schedule", alive: false, createdAt: "2026-11-01T00:00:00Z" }),
        s("mine", { origin: "user" }),
      ] as never,
    });
    scheduleList.mockResolvedValue([sched]);
    await render();
    const row = (n: string) => host.querySelector(`[data-sched-session="${n}"]`);
    // No schedule id: the group of their own, not under a row.
    expect(host.querySelector("[data-sched-sessions]")?.contains(row("cron-live"))).toBe(true);
    expect(row("cron-live")?.querySelectorAll(".sched-sess-kids li")).toHaveLength(1);
    expect(row("cron-old")).toBeNull();
    expect(row("mine")).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
    expect(row("cron-old")).not.toBeNull();
    useSessionsStore.setState({ sessions: [] });
  });
});

describe("SchedulesSection late schedule list", () => {
  it("keeps a finished run's fold open when the late list moves it under its schedule row", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({
      sessions: [{ name: "cron-old", title: "cron-old", kind: "claude", alive: false, origin: "schedule", scheduleId: "sch_1", createdAt: "2026-11-01T00:00:00Z" }] as never,
    });
    let answer: (v: unknown) => void = () => {};
    scheduleList.mockReturnValue(new Promise((r) => (answer = r)));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await render();
      await act(async () => {
        vi.advanceTimersByTime(5100); // the first-read wait ends: the run shows in the unscheduled group
      });
      const group = () => host.querySelector("[data-sched-sessions]");
      expect(group()).not.toBeNull();
      await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
      expect(group()!.querySelector('[data-sched-session="cron-old"]')).not.toBeNull();
      await act(async () => answer([sched]));
      const under = host.querySelector('[data-sched-sessions-of="sch_1"]');
      expect(group()).toBeNull();
      expect(under!.querySelector('[data-sched-session="cron-old"]')).not.toBeNull();
      expect(under!.querySelector(".sched-sess-ended")!.getAttribute("aria-expanded")).toBe("true");
    } finally {
      useSessionsStore.setState({ sessions: [] });
    }
  });
});

describe("SchedulesSection fold state across groups", () => {
  const ended = (name: string, scheduleId?: string) => ({ name, title: name, kind: "claude", alive: false, origin: "schedule", ...(scheduleId ? { scheduleId } : {}), createdAt: "2026-11-01T00:00:00Z" });
  const setup = async (sessions: unknown[]) => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({ sessions: sessions as never });
    scheduleList.mockResolvedValue([sched]);
    await render();
    return useSessionsStore;
  };
  const ariaOf = (sel: string) => host.querySelector(`${sel} .sched-sess-ended`)?.getAttribute("aria-expanded");

  it("keeps the row's fold closed when the unscheduled fold was closed", async () => {
    const store = await setup([ended("a")]);
    expect(ariaOf("[data-sched-sessions]")).toBe("false");
    store.setState({ sessions: [ended("a", "sch_1")] as never });
    await act(async () => {});
    expect(ariaOf('[data-sched-sessions-of="sch_1"]')).toBe("false");
    store.setState({ sessions: [] });
  });

  it("does not open a row's own finished runs because the unscheduled fold is open", async () => {
    const store = await setup([ended("a"), ended("b", "sch_1")]);
    await act(async () => host.querySelector<HTMLButtonElement>("[data-sched-sessions] .sched-sess-ended")!.click());
    expect(ariaOf('[data-sched-sessions-of="sch_1"]')).toBe("false");
    store.setState({ sessions: [] });
  });

  it("lets the member close the carried fold", async () => {
    const store = await setup([ended("a")]);
    await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
    store.setState({ sessions: [ended("a", "sch_1")] as never });
    await act(async () => {});
    const btn = () => host.querySelector<HTMLButtonElement>('[data-sched-sessions-of="sch_1"] .sched-sess-ended')!;
    expect(btn().getAttribute("aria-expanded")).toBe("true");
    await act(async () => btn().click());
    expect(btn().getAttribute("aria-expanded")).toBe("false");
    store.setState({ sessions: [] });
  });

  it("starts a new unscheduled fold closed after the group emptied", async () => {
    const store = await setup([ended("a")]);
    await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
    store.setState({ sessions: [] });
    await act(async () => {});
    expect(host.querySelector("[data-sched-sessions]")).toBeNull();
    store.setState({ sessions: [ended("c")] as never });
    await act(async () => {});
    expect(ariaOf("[data-sched-sessions]")).toBe("false");
    store.setState({ sessions: [] });
  });
});

describe("SchedulesSection fold state lifetime", () => {
  const ended = (name: string, extra: Record<string, unknown> = {}) => ({ name, title: name, kind: "claude", alive: false, origin: "schedule", createdAt: "2026-11-01T00:00:00Z", ...extra });
  const live = (name: string) => ended(name, { alive: true });
  const aria = () => host.querySelector("[data-sched-sessions] .sched-sess-ended")?.getAttribute("aria-expanded");

  it("keeps the fold open while a live run holds the group through ended -> live -> ended", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({ sessions: [ended("a")] as never });
    scheduleList.mockResolvedValue([sched]);
    await render();
    await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
    useSessionsStore.setState({ sessions: [live("a")] as never });
    await act(async () => {});
    useSessionsStore.setState({ sessions: [ended("a")] as never });
    await act(async () => {});
    expect(aria()).toBe("true");
    useSessionsStore.setState({ sessions: [] });
  });

  it("keeps the fold open after a data update when the section is solo and its stored fold is closed", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    const { SoloSection } = await import("../../ui/Section.tsx");
    localStorage.setItem("af-section-schedules", "false");
    useSessionsStore.setState({ sessions: [ended("a")] as never });
    scheduleList.mockResolvedValue([sched]);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <SoloSection>
              <SchedulesSection />
            </SoloSection>
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await act(async () => {});
    await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
    useSessionsStore.setState({ sessions: [ended("a"), ended("b")] as never });
    await act(async () => {});
    expect(aria()).toBe("true");
    localStorage.removeItem("af-section-schedules");
    useSessionsStore.setState({ sessions: [] });
  });

  it("resets the folds when the section closes and reopens", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({ sessions: [ended("a")] as never });
    scheduleList.mockResolvedValue([sched]);
    await render();
    await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
    const head = () => host.querySelector<HTMLButtonElement>(".ui-section-toggle")!;
    await act(async () => head().click());
    expect(host.querySelector("[data-sched-sessions]")).toBeNull();
    await act(async () => head().click());
    expect(aria()).toBe("false");
    localStorage.removeItem("af-section-schedules");
    useSessionsStore.setState({ sessions: [] });
  });
});

describe("SchedulesSection late list racing a section close", () => {
  it("does not carry a fold into a row when the section closes in the same commit as the late list", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({
      sessions: [{ name: "cron-old", title: "cron-old", kind: "claude", alive: false, origin: "schedule", scheduleId: "sch_1", createdAt: "2026-11-01T00:00:00Z" }] as never,
    });
    let answer: (v: unknown) => void = () => {};
    scheduleList.mockReturnValue(new Promise((r) => (answer = r)));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await render();
      await act(async () => {
        vi.advanceTimersByTime(5100);
      });
      await act(async () => host.querySelector<HTMLButtonElement>(".sched-sess-ended")!.click());
      await act(async () => {
        host.querySelector<HTMLButtonElement>(".ui-section-toggle")!.click(); // close, batched with the answer
        answer([sched]);
      });
      expect(host.querySelector(".sched-list")).toBeNull();
      await act(async () => host.querySelector<HTMLButtonElement>(".ui-section-toggle")!.click());
      expect(host.querySelector('[data-sched-sessions-of="sch_1"] .sched-sess-ended')!.getAttribute("aria-expanded")).toBe("false");
    } finally {
      localStorage.removeItem("af-section-schedules");
      useSessionsStore.setState({ sessions: [] });
    }
  });
});

describe("SchedulesSection scheduled sessions under a working set", () => {
  it("shows a root outside the set as a heading without a state or an open button", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    const { useReposStore } = await import("../repos/store.ts");
    const { createWorkingSet, setActiveWorkingSet, toggleWorkingSetMember, deleteWorkingSet } = await import("../../lib/workingSetsStore.ts");
    const s = (name: string, extra: Record<string, unknown>) => ({ name, title: name, kind: "claude", alive: true, ...extra });
    useReposStore.setState({ repos: [{ name: "a" }, { name: "b" }] as never });
    useSessionsStore.setState({
      sessions: [
        s("cron", { origin: "schedule", repo: "a", state: "question", createdAt: "2026-11-02T00:00:00Z" }),
        s("rev", { origin: "session", originSession: "cron", repo: "b", state: "working", createdAt: "2026-11-02T01:00:00Z" }),
      ] as never,
    });
    scheduleList.mockResolvedValue([sched]);
    const id = createWorkingSet("only B");
    toggleWorkingSetMember(id, "repos", "b");
    setActiveWorkingSet(id);
    try {
      await render();
      const root = host.querySelector('[data-sched-session="cron"]')!;
      expect(root.querySelector(":scope > .sched-sess-anchor")).not.toBeNull();
      expect(root.querySelector(":scope > .sched-sess-main")).toBeNull(); // nothing to open, no waiting dot
      expect(root.querySelectorAll(".sched-sess-kids .sched-sess-main")).toHaveLength(1);
    } finally {
      setActiveWorkingSet("");
      deleteWorkingSet(id);
      useSessionsStore.setState({ sessions: [] });
      useReposStore.setState({ repos: [] });
    }
  });
});

describe("SchedulesSection scheduled sessions under their schedule", () => {
  it("lists a run under the row of the schedule that started it, and the rest in the group", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    const s = (name: string, extra: Record<string, unknown>) => ({ name, title: name, kind: "claude", alive: true, ...extra });
    useSessionsStore.setState({
      sessions: [
        s("mine-run", { origin: "schedule", scheduleId: sched.id, createdAt: "2026-11-02T00:00:00Z" }),
        s("mine-kid", { origin: "session", originSession: "mine-run", createdAt: "2026-11-02T01:00:00Z" }),
        s("gone-run", { origin: "schedule", scheduleId: "deleted", createdAt: "2026-11-02T02:00:00Z" }),
        s("old-run", { origin: "schedule", createdAt: "2026-11-01T00:00:00Z" }),
      ] as never,
    });
    scheduleList.mockResolvedValue([sched]);
    await render();
    const under = host.querySelector(`[data-sched-sessions-of="${sched.id}"]`);
    expect(under?.querySelector('[data-sched-session="mine-run"]')?.querySelectorAll(".sched-sess-kids li")).toHaveLength(1);
    expect(under?.querySelector('[data-sched-session="gone-run"]')).toBeNull();
    // A deleted schedule's run and one without an id keep the group of their own.
    const group = host.querySelector("[data-sched-sessions]");
    expect(group?.querySelector('[data-sched-session="gone-run"]')).not.toBeNull();
    expect(group?.querySelector('[data-sched-session="old-run"]')).not.toBeNull();
    expect(group?.querySelector('[data-sched-session="mine-run"]')).toBeNull();
    useSessionsStore.setState({ sessions: [] });
  });
});

describe("SchedulesSection scheduled sessions before the schedule list has loaded", () => {
  const s = (name: string, extra: Record<string, unknown>) => ({ name, title: name, kind: "claude", alive: true, ...extra });
  const seed = async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({
      sessions: [
        s("mine-run", { origin: "schedule", scheduleId: sched.id, createdAt: "2026-11-02T00:00:00Z" }),
        s("old-run", { origin: "schedule", createdAt: "2026-11-01T00:00:00Z" }),
      ] as never,
    });
    return useSessionsStore;
  };

  it("holds back a run that names a schedule until the list settles, then files it under its row without a group flash", async () => {
    const store = await seed();
    let resolve: (v: unknown) => void = () => {};
    scheduleList.mockReturnValue(new Promise((r) => (resolve = r)));
    await render();
    const group = () => host.querySelector("[data-sched-sessions]");
    expect(host.querySelector('[data-sched-session="mine-run"]')).toBeNull();
    expect(group()?.querySelector('[data-sched-session="old-run"]')).not.toBeNull();
    await act(async () => resolve([sched]));
    await act(async () => {});
    expect(host.querySelector(`[data-sched-sessions-of="${sched.id}"] [data-sched-session="mine-run"]`)).not.toBeNull();
    expect(group()?.querySelector('[data-sched-session="mine-run"]')).toBeNull();
    store.setState({ sessions: [] });
  });

  it("shows the run in the group when the first read fails", async () => {
    const store = await seed();
    scheduleList.mockResolvedValue({ error: { code: "http_502", message: "bad gateway" } });
    await render();
    expect(host.querySelector("[data-sched-sessions]")?.querySelector('[data-sched-session="mine-run"]')).not.toBeNull();
    store.setState({ sessions: [] });
  });

  it("ends the wait when the first read never answers", async () => {
    vi.useFakeTimers();
    try {
      const store = await seed();
      scheduleList.mockReturnValue(new Promise(() => {}));
      await render();
      expect(host.querySelector('[data-sched-session="mine-run"]')).toBeNull();
      await act(async () => {
        vi.advanceTimersByTime(6000);
      });
      expect(host.querySelector("[data-sched-sessions]")?.querySelector('[data-sched-session="mine-run"]')).not.toBeNull();
      store.setState({ sessions: [] });
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not let another tenant's failed read end this tenant's wait", async () => {
    const { useTenantStore } = await import("../../core/store/tenant.ts");
    const store = await seed();
    scheduleList.mockResolvedValue({ error: { code: "http_502", message: "bad gateway" } });
    await act(async () => useTenantStore.setState({ tenant: "A" } as never));
    await render();
    expect(host.querySelector('[data-sched-session="mine-run"]')).not.toBeNull(); // A failed: shown in the group
    scheduleList.mockReturnValue(new Promise(() => {}));
    await act(async () => useTenantStore.setState({ tenant: "B" } as never));
    await act(async () => {});
    expect(host.querySelector('[data-sched-session="mine-run"]')).toBeNull(); // B is still reading
    store.setState({ sessions: [] });
    await act(async () => useTenantStore.setState({ tenant: "" } as never));
  });

  it("waits again for a first read when the member returns to a tenant after another one expired", async () => {
    vi.useFakeTimers();
    const { useTenantStore } = await import("../../core/store/tenant.ts");
    try {
      const store = await seed();
      scheduleList.mockResolvedValue([sched]);
      await act(async () => useTenantStore.setState({ tenant: "A" } as never));
      await render();
      await act(async () => {
        vi.advanceTimersByTime(6000); // A's own wait expires
      });
      scheduleList.mockResolvedValue([{ ...sched, id: "sch_other" }]); // B lists another schedule
      await act(async () => useTenantStore.setState({ tenant: "B" } as never));
      await act(async () => {});
      scheduleList.mockReturnValue(new Promise(() => {}));
      await act(async () => useTenantStore.setState({ tenant: "A" } as never));
      await act(async () => {});
      expect(host.querySelector("[data-sched-sessions]")?.querySelector('[data-sched-session="mine-run"]') ?? null).toBeNull(); // A is reading again
      store.setState({ sessions: [] });
    } finally {
      vi.useRealTimers();
      await act(async () => useTenantStore.setState({ tenant: "" } as never));
    }
  });

  it("waits again when the member returns to a tenant whose first read had failed", async () => {
    const { useTenantStore } = await import("../../core/store/tenant.ts");
    try {
      const store = await seed();
      scheduleList.mockResolvedValue({ error: { code: "http_502", message: "bad gateway" } });
      await act(async () => useTenantStore.setState({ tenant: "A" } as never));
      await render();
      expect(host.querySelector('[data-sched-session="mine-run"]')).not.toBeNull(); // A failed: shown in the group
      scheduleList.mockReturnValue(new Promise(() => {})); // B and the second A never answer
      await act(async () => useTenantStore.setState({ tenant: "B" } as never));
      await act(async () => useTenantStore.setState({ tenant: "A" } as never));
      await act(async () => {});
      expect(host.querySelector("[data-sched-sessions]")?.querySelector('[data-sched-session="mine-run"]') ?? null).toBeNull();
      store.setState({ sessions: [] });
    } finally {
      await act(async () => useTenantStore.setState({ tenant: "" } as never));
    }
  });
});

// Poll ordering on useInterval (#4786): a slow older read must not overwrite a newer one, and an
// answer slower than the period must still show.
describe("SchedulesSection poll guards", () => {
  const defer = () => {
    let resolve!: (v: unknown) => void;
    const p = new Promise((r) => (resolve = r));
    return { p, resolve };
  };

  it("keeps the newest read when the slow initial read lands after a poll", async () => {
    vi.useFakeTimers();
    const first = defer();
    const second = defer();
    scheduleList.mockReturnValueOnce(first.p).mockReturnValueOnce(second.p);
    await render();
    await act(async () => void vi.advanceTimersByTime(15000));
    expect(scheduleList).toHaveBeenCalledTimes(2);
    await act(async () => second.resolve([{ ...sched, id: "sch_new", spec_label: "newer" }]));
    await act(async () => first.resolve([{ ...sched, id: "sch_old", spec_label: "older" }]));
    expect(text()).toContain("newer");
    expect(text()).not.toContain("older");
  });

  it("still shows an answer that takes longer than the poll period", async () => {
    vi.useFakeTimers();
    const first = defer();
    scheduleList.mockReturnValueOnce(first.p).mockReturnValue(new Promise(() => {}));
    await render();
    await act(async () => void vi.advanceTimersByTime(15000));
    expect(scheduleList).toHaveBeenCalledTimes(2); // the tick fired while the first read is still out
    await act(async () => first.resolve([sched]));
    expect(text()).toContain("morning review");
  });

  it("keeps a newer failure on screen when the older read succeeds afterwards", async () => {
    vi.useFakeTimers();
    const first = defer();
    scheduleList.mockReturnValueOnce(first.p).mockResolvedValueOnce({ error: { code: "http_502", message: "bad gateway" } });
    await render();
    await act(async () => void vi.advanceTimersByTime(15000));
    expect(failed()).toBe(true);
    await act(async () => first.resolve([sched]));
    expect(failed()).toBe(true);
  });
});
