// The studio pane's loop on a phone (lane L4), rendered whole: finished pictures announce
// themselves when the results are out of sight, the results tab counts them until it is
// opened, and the lightbox walks the results column and carries the picture verbs.
//
// Jobs reach the pane the way they do on a phone: through the studio's own polling (a new press
// in its log, whoever pressed) or the tab coming back into view — never through the header's
// refresh button, which a narrow pane does not show. No job is live at the start, so the
// 2 s job poller cannot be what delivers them.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { DraftLogEntry, HistoryItem, Job, JobsResponse, StudioWire } from "./wire.ts";

// `jobs` is what the test says the queue holds; `served` is what the Agent answers. While a press
// is `held` (its line written, the enqueue not done yet — a cold engine) the Agent still answers
// the list from before.
const jobsNow: { jobs: Job[]; served: Job[]; held: boolean } = { jobs: [], served: [], held: false };
const historyNow: { items: HistoryItem[] } = { items: [] };
const baseStudio = { id: "s1", title: "", draft: {}, locks: [], session: "sess-1", agent_trial: true, created_at: "2026-09-27T10:00:00Z" };
const studioNow: { s: StudioWire } = { s: { ...baseStudio, updated_at: "1", recent_log: [] } as unknown as StudioWire };
vi.mock("./api.ts", async (orig) => ({
  ...(await orig<typeof import("./api.ts")>()),
  imagegenStatus: async () => ({ enabled: true, ready: true, providers: [] }),
  imagegenJobs: async (): Promise<JobsResponse> => {
    if (!jobsNow.held) jobsNow.served = jobsNow.jobs;
    return { jobs: jobsNow.served, groups: [] } as JobsResponse;
  },
  listStudios: async () => ({ studios: [] }),
  imagegenHistory: async () => ({ items: historyNow.items }),
  imageProperties: async (path: string) =>
    path === "generated/console/old.png" ? { source: "sidecar", seed: 4242 } : { source: "none" },
  getStudio: async () => studioNow.s,
  studioDraftLog: async () => ({ entries: [] }),
}));
// The conversation column's mirror has its own network and its own tests; here it is a stub.
vi.mock("../mirror/MirrorView.tsx", () => ({
  MirrorView: (p: { aboveComposer?: React.ReactNode }) => <div className="mirror-stub">{p.aboveComposer}</div>,
}));
vi.mock("../repos/useRepoRail.ts", () => ({
  useRepoRailContext: () => ({ launchKinds: ["claude"], connsSettling: false }),
}));
vi.mock("../../ui/ModelPicker.tsx", () => ({
  ModelPicker: () => <select data-testid="model" />,
  EffortPicker: () => <select data-testid="effort" />,
}));

import { ImagegenView } from "./ImagegenView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { Session } from "../../types/session.ts";

let host: HTMLDivElement;
let root: Root;
const realRO = globalThis.ResizeObserver;

const done = (n: number): Job => ({
  id: "g1",
  state: "done",
  studio: "s1",
  files: Array.from({ length: n }, (_, i) => ({ path: `generated/console/p${i}.png`, seed: 100 + i })),
});

/** jsdom lays nothing out: with a ResizeObserver the pane reads width 0, i.e. narrow. */
const narrowPane = (on: boolean) => {
  globalThis.ResizeObserver = on
    ? (class {
        observe() {}
        disconnect() {}
      } as unknown as typeof ResizeObserver)
    : (undefined as unknown as typeof ResizeObserver);
};

// Keyed by the studio, as Pane.tsx mounts it: switching studios in a pane is a remount.
const view = (studioId: string, active: boolean) => (
  <ToastProvider>
    <ConfirmProvider>
      <ImagegenView key={studioId} paneId="p1" studioId={studioId} active={active} />
    </ConfirmProvider>
  </ToastProvider>
);
const mount = async (props: { active?: boolean } = {}) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(view("s1", props.active ?? true)));
  await tick(100); // the first status / jobs / studio reads
};

const tick = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

/** Someone pressed — the agent's run_image_trial, or another device — in the Agent's order
 *  (studio_press.go): ① the press line, ③ the enqueue, ④ the press_result line. Neither line
 *  moves the studio's updated_at, and on a cold engine ③ is slow, so the pane's poll sees ①
 *  while the queue does not have the jobs yet; they appear with ④, a poll later. */
let pressSeq = 0;
const logLine = (e: Record<string, unknown>) => {
  pressSeq++;
  const entry = { seq: pressSeq, at: "2026-09-27T10:00:00Z", ...e } as unknown as DraftLogEntry;
  studioNow.s = { ...studioNow.s, recent_log: [...(studioNow.s.recent_log || []), entry] };
};
const press = async () => {
  const version = "v" + (pressSeq + 1);
  jobsNow.held = true;
  logLine({ kind: "press", author: "agent", mode: "agent_trial", version });
  await tick(2100);
  await tick(50);
  jobsNow.held = false;
  logLine({ kind: "press_result", version, state: "ok", jobs: jobsNow.jobs.map((j) => j.id) });
  await tick(2100);
  await tick(50);
};
const tab = (k: number) => host.querySelectorAll<HTMLButtonElement>(".igen-tab")[k];
const toastText = () => document.querySelector(".igen-done-note")?.textContent || "";

beforeEach(() => {
  vi.useFakeTimers();
  useWorkspaceStore.setState({ state: "running" });
  jobsNow.jobs = [];
  jobsNow.served = [];
  jobsNow.held = false;
  historyNow.items = [];
  pressSeq = 0;
  useSessionsStore.setState({ loaded: true, sessions: [] });
  studioNow.s = { ...baseStudio, updated_at: "1", recent_log: [] } as unknown as StudioWire;
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  globalThis.ResizeObserver = realRO;
  vi.useRealTimers();
});

describe("the studio's loop on a narrow pane", () => {
  it("announces finished pictures and badges the results tab until it is opened", async () => {
    narrowPane(true);
    await mount();
    expect(host.querySelector(".igen-draftbar")).not.toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
    jobsNow.jobs = [done(3)];
    await press();
    expect(toastText()).toMatch(/3/);
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+3");
    // "View" goes to the results tab: the badge and the notice both go away.
    await act(async () => document.querySelector<HTMLButtonElement>(".igen-done-note button")!.click());
    expect(tab(2).getAttribute("aria-selected")).toBe("true");
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
    expect(document.querySelector(".igen-done-note")).toBeNull();
  });

  it("says nothing when the results are already on screen", async () => {
    narrowPane(false); // three columns: the results column is visible
    await mount();
    jobsNow.jobs = [done(2)];
    await press();
    expect(document.querySelector(".igen-done-note")).toBeNull();
  });

  it("announces on a wide pane that is not the active one", async () => {
    narrowPane(false);
    await mount({ active: false });
    jobsNow.jobs = [done(2)];
    await press();
    expect(toastText()).toMatch(/2/);
  });

  it("does not announce another studio's pictures", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [{ ...done(2), studio: "other" }];
    await press();
    expect(document.querySelector(".igen-done-note")).toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
  });

  it("does not announce what was already finished when the pane opened", async () => {
    narrowPane(true);
    jobsNow.jobs = [done(4)];
    await mount();
    await press();
    expect(document.querySelector(".igen-done-note")).toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
  });
});

describe("jobs nobody pressed in this pane (review 4)", () => {
  it("brings the agent's trial to the conversation tab: thumbnail, badge and notice", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [{ id: "t1", state: "running", trial: true, studio: "s1" }];
    await press();
    // Now a job is live: the ordinary 2 s poller takes over until it finishes.
    jobsNow.jobs = [{ id: "t1", state: "done", trial: true, studio: "s1", files: [{ path: "generated/console/t1.png", seed: 7 }] }];
    await tick(2100);
    await tick(50);
    expect(host.querySelector(".igen-draftbar-thumb")).not.toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+1");
    expect(toastText()).toMatch(/1/);
  });

  it("a read between the press line and the enqueue finds nothing; the press_result line reads again", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [{ id: "t1", state: "done", trial: true, studio: "s1", files: [{ path: "generated/console/t1.png", seed: 7 }] }];
    // ① only: the pane's poll takes the press line in while the cold engine has not enqueued.
    jobsNow.held = true;
    logLine({ kind: "press", author: "agent", mode: "agent_trial", version: "v1" });
    await tick(2100);
    await tick(50);
    expect(host.querySelector(".igen-draftbar-thumb")).toBeNull();
    // ③ then ④: the jobs exist and the result line says so.
    jobsNow.held = false;
    logLine({ kind: "press_result", version: "v1", state: "ok", jobs: ["t1"] });
    await tick(2100);
    await tick(50);
    expect(host.querySelector(".igen-draftbar-thumb")).not.toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+1");
  });

  it("reads the jobs when the tab comes back into view, even with no studio polling", async () => {
    narrowPane(true);
    studioNow.s = { ...studioNow.s, session: "" } as StudioWire; // no session: the studio poll is off
    await mount();
    jobsNow.jobs = [done(2)];
    await tick(5000);
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
    await act(async () => document.dispatchEvent(new Event("visibilitychange")));
    await tick(50);
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+2");
  });
});

describe("the first tab (review 4)", () => {
  it("opens on the conversation while the agent's first prompt is still being delivered", async () => {
    narrowPane(true);
    useSessionsStore.setState({ loaded: true, sessions: [{ name: "sess-1", kind: "claude", alive: true, initialPromptState: "pending" } as Session] });
    await mount();
    expect(tab(0).getAttribute("aria-selected")).toBe("true");
  });

  it("opens on the settings otherwise, and never takes the tab back from the member", async () => {
    narrowPane(true);
    useSessionsStore.setState({ loaded: true, sessions: [{ name: "sess-1", kind: "claude", alive: true, initialPromptState: "delivered" } as Session] });
    await mount();
    expect(tab(1).getAttribute("aria-selected")).toBe("true");
    await act(async () => tab(2).click());
    useSessionsStore.setState({ sessions: [{ name: "sess-1", kind: "claude", alive: true, initialPromptState: "pending" } as Session] });
    await tick(50);
    expect(tab(2).getAttribute("aria-selected")).toBe("true");
  });
});

describe("the ready notice (review 1)", () => {
  it("is withdrawn when the pane switches to another studio, so View never drives a dead pane", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(2)];
    await press();
    expect(toastText()).toMatch(/2/);
    await act(async () => root.render(view("s2", true)));
    await act(async () => {});
    expect(document.querySelector(".igen-done-note")).toBeNull();
  });

  it("is withdrawn when the member opens the results tab by hand", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(2)];
    await press();
    expect(toastText()).toMatch(/2/);
    await act(async () => tab(2).click());
    expect(document.querySelector(".igen-done-note")).toBeNull();
  });

  it("is the pane's own line under the tab strip, not a floating toast over the app's controls", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(1)];
    await press();
    const note = document.querySelector(".igen-done-note")!;
    expect(note.closest(".ui-toasts")).toBeNull();
    // In the flow between the tabs and the columns: it pushes them down, it covers nothing.
    expect(note.previousElementSibling?.classList.contains("igen-tabs")).toBe(true);
    expect(note.nextElementSibling?.classList.contains("igen-body")).toBe(true);
  });

  it("goes away on its own after a while", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(1)];
    await press();
    expect(document.querySelector(".igen-done-note")).not.toBeNull();
    await tick(8100);
    expect(document.querySelector(".igen-done-note")).toBeNull();
    // The badge stays: the pictures are still unseen.
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+1");
  });

  it("counts every trial that finished between two reads, not only the one in the slot", async () => {
    narrowPane(true);
    jobsNow.jobs = [
      { id: "t1", state: "running", trial: true, studio: "s1" },
      { id: "t2", state: "running", trial: true, studio: "s1" },
    ];
    await mount();
    jobsNow.jobs = [
      { id: "t1", state: "done", trial: true, studio: "s1", files: [{ path: "generated/console/t1.png", seed: 1 }] },
      { id: "t2", state: "done", trial: true, studio: "s1", files: [{ path: "generated/console/t2.png", seed: 2 }] },
    ];
    await press();
    expect(toastText()).toMatch(/2/);
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+2");
  });
});

describe("the lightbox from the history", () => {
  it("offers the seeded trial for a past picture, with the seed read from its properties", async () => {
    narrowPane(false);
    jobsNow.jobs = [];
    historyNow.items = [{ path: "generated/console/old.png", studio: "s1", version: "v1", created_at: "2026-09-27T10:00:00Z" }];
    await mount();
    const thumb = host.querySelector<HTMLButtonElement>(".igen-history .igen-thumb")!;
    await act(async () => thumb.click());
    await act(async () => {});
    const verbs = [...document.querySelectorAll(".mirror-lightbox-actions button")].map((b) => b.textContent || "");
    expect(verbs.length).toBe(3);
    expect(verbs.some((t) => /seed/.test(t))).toBe(true);
    // It runs the CURRENT draft at that seed, not the old picture's settings: the label says so.
    expect(verbs.some((t) => /今の下書き|current draft/.test(t))).toBe(true);
  });

  it("hides the seeded trial only when the picture's seed cannot be read", async () => {
    narrowPane(false);
    jobsNow.jobs = [];
    historyNow.items = [{ path: "generated/console/unknown.png", studio: "s1", created_at: "2026-09-27T10:00:00Z" }];
    await mount();
    await act(async () => host.querySelector<HTMLButtonElement>(".igen-history .igen-thumb")!.click());
    await act(async () => {});
    expect(document.querySelectorAll(".mirror-lightbox-actions button").length).toBe(2);
  });
});

describe("the lightbox from the results", () => {
  it("walks the results column with ←/→ and carries the studio's picture verbs", async () => {
    narrowPane(false);
    jobsNow.jobs = [done(3)];
    await mount();
    const thumbs = [...host.querySelectorAll<HTMLButtonElement>(".igen-results .igen-thumb")];
    expect(thumbs.length).toBe(3);
    await act(async () => thumbs[1].click());
    const pos = () => document.querySelector(".mirror-lightbox-pos")?.textContent || "";
    expect(pos()).toMatch(/2\s*\/\s*3/);
    await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" })));
    expect(pos()).toMatch(/3\s*\/\s*3/);
    expect(document.querySelector<HTMLButtonElement>(".mirror-lightbox-next")!.disabled).toBe(true);
    expect(document.querySelector(".mirror-lightbox-actions")).not.toBeNull();
  });
});
