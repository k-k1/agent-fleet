// The Auto-run view: one row per run under its queue's category, the run's own sessions one
// level below, finished runs folded, and a click opening the session.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const opened = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (n: string) => opened(n) }));
vi.mock("../memo/api.ts", () => ({
  memoQueueList: vi.fn(async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" }] })),
  memoQueueDeliveries: vi.fn(async () => [{ id: "d1", session: "run-a" }]),
}));

const { AutoRunSection } = await import("./AutoRunSection.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
const sess = (name: string, extra: Partial<Session>): Session => ({ name, title: name, kind: "claude", alive: true, ...extra });

const mount = async () => {
  await act(async () => root.render(<AutoRunSection />));
  await act(async () => {
    await Promise.resolve();
  });
};

beforeEach(() => {
  opened.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  useSessionsStore.setState({
    sessions: [
      sess("run-a", {
        origin: "queue",
        createdAt: new Date(Date.now() - 12 * 60_000).toISOString(),
        links: [{ provider: "github", key: "o/r#12", role: "started", title: "Fix it" }],
        pr: { number: 30, state: "open", url: "u" },
      }),
      sess("rev-a", { origin: "session", originSession: "run-a" }),
      sess("run-old", { origin: "queue", alive: false, createdAt: "2026-11-01T00:00:00Z" }),
      sess("hand", { origin: "user" }),
    ],
  });
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("AutoRunSection", () => {
  it("shows a run row under its category with the item, PR and elapsed time", async () => {
    await mount();
    expect(host.querySelector("[data-autorun-category]")!.getAttribute("data-autorun-category")).toBe("agent-fleet / issue-rounds");
    const row = host.querySelector('[data-autorun-run="run-a"]')!;
    expect(row.getAttribute("data-autorun-state")).toBe("running");
    expect(row.querySelector(".autorun-run-main .autorun-title")!.textContent).toBe("#12 Fix it");
    expect(row.querySelector(".autorun-pr")!.textContent).toContain("#30");
    expect(row.querySelector(".autorun-elapsed")!.textContent).toBe("12m");
  });

  it("opens the run's session, and a session it started, on click", async () => {
    await mount();
    await act(async () => host.querySelector<HTMLButtonElement>('[data-autorun-run="run-a"] .autorun-run-main')!.click());
    expect(opened).toHaveBeenLastCalledWith("run-a");
    await act(async () => host.querySelector<HTMLButtonElement>('[data-autorun-run="run-a"] .autorun-kid')!.click());
    expect(opened).toHaveBeenLastCalledWith("rev-a");
  });

  it("folds ended runs into Ended N and never lists the member's own sessions", async () => {
    await mount();
    expect(host.querySelector('[data-autorun-run="run-old"]')).toBeNull();
    expect(host.querySelector('[data-autorun-run="hand"]')).toBeNull();
    const head = host.querySelector<HTMLButtonElement>(".autorun-ended-head")!;
    expect(head.textContent).toContain("1");
    await act(async () => head.click());
    expect(host.querySelector('[data-autorun-run="run-old"]')!.getAttribute("data-autorun-state")).toBe("ended");
  });

  it("says so when there is no run", async () => {
    useSessionsStore.setState({ sessions: [sess("hand", { origin: "user" })] });
    await mount();
    expect(host.querySelector(".pane-empty")).not.toBeNull();
  });
});
