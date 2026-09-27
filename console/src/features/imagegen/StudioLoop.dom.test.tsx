// The studio pane's loop on a phone (lane L4), rendered whole: finished pictures announce
// themselves when the results are out of sight, the results tab counts them until it is
// opened, and the lightbox walks the results column and carries the picture verbs.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { HistoryItem, Job, JobsResponse } from "./wire.ts";

const jobsNow: { jobs: Job[] } = { jobs: [] };
const historyNow: { items: HistoryItem[] } = { items: [] };
vi.mock("./api.ts", async (orig) => ({
  ...(await orig<typeof import("./api.ts")>()),
  imagegenStatus: async () => ({ enabled: true, ready: true, providers: [] }),
  imagegenJobs: async (): Promise<JobsResponse> => ({ jobs: jobsNow.jobs, groups: [] }) as JobsResponse,
  listStudios: async () => ({ studios: [] }),
  imagegenHistory: async () => ({ items: historyNow.items }),
  imageProperties: async (path: string) =>
    path === "generated/console/old.png" ? { source: "sidecar", seed: 4242 } : { source: "none" },
  getStudio: async () => ({ id: "s1", title: "", draft: {}, locks: [], session: "", agent_trial: true, created_at: "2026-09-27T10:00:00Z", updated_at: "1" }),
  studioDraftLog: async () => ({ entries: [] }),
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

let host: HTMLDivElement;
let root: Root;
const realRO = globalThis.ResizeObserver;

const running: Job = { id: "g1", state: "running", studio: "s1" };
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
  await act(async () => {}); // the first status / jobs reads
};

const refresh = async () => {
  const b = [...host.querySelectorAll<HTMLButtonElement>(".view-head button")].find((x) =>
    /更新|Refresh/i.test(x.getAttribute("aria-label") || x.title || ""),
  )!;
  await act(async () => b.click());
  await act(async () => {});
};
const tab = (k: number) => host.querySelectorAll<HTMLButtonElement>(".igen-tab")[k];
const toastText = () => document.querySelector(".igen-done-toast")?.textContent || "";

beforeEach(() => {
  useWorkspaceStore.setState({ state: "running" });
  jobsNow.jobs = [running];
  historyNow.items = [];
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  globalThis.ResizeObserver = realRO;
});

describe("the studio's loop on a narrow pane", () => {
  it("announces finished pictures and badges the results tab until it is opened", async () => {
    narrowPane(true);
    await mount();
    expect(host.querySelector(".igen-draftbar")).not.toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
    jobsNow.jobs = [done(3)];
    await refresh();
    expect(toastText()).toMatch(/3/);
    expect(tab(2).querySelector(".igen-tab-badge")?.textContent).toBe("+3");
    // "View" goes to the results tab: the badge and the notice both go away.
    await act(async () => document.querySelector<HTMLButtonElement>(".igen-done-toast button")!.click());
    expect(tab(2).getAttribute("aria-selected")).toBe("true");
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
    expect(document.querySelector(".igen-done-toast")).toBeNull();
  });

  it("says nothing when the results are already on screen", async () => {
    narrowPane(false); // three columns: the results column is visible
    await mount();
    jobsNow.jobs = [done(2)];
    await refresh();
    expect(document.querySelector(".igen-done-toast")).toBeNull();
  });

  it("announces on a wide pane that is not the active one", async () => {
    narrowPane(false);
    await mount({ active: false });
    jobsNow.jobs = [done(2)];
    await refresh();
    expect(toastText()).toMatch(/2/);
  });

  it("does not announce another studio's pictures", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [{ ...done(2), studio: "other" }];
    await refresh();
    expect(document.querySelector(".igen-done-toast")).toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
  });

  it("does not announce what was already finished when the pane opened", async () => {
    narrowPane(true);
    jobsNow.jobs = [done(4)];
    await mount();
    await refresh();
    expect(document.querySelector(".igen-done-toast")).toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
  });
});

describe("the ready notice (review 1)", () => {
  it("is withdrawn when the pane switches to another studio, so View never drives a dead pane", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(2)];
    await refresh();
    expect(toastText()).toMatch(/2/);
    await act(async () => root.render(view("s2", true)));
    await act(async () => {});
    expect(document.querySelector(".igen-done-toast")).toBeNull();
  });

  it("is withdrawn when the member opens the results tab by hand", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(2)];
    await refresh();
    expect(toastText()).toMatch(/2/);
    await act(async () => tab(2).click());
    expect(document.querySelector(".igen-done-toast")).toBeNull();
  });

  it("stands at the top of the screen, clear of the draft bar and the composer", async () => {
    narrowPane(true);
    await mount();
    jobsNow.jobs = [done(1)];
    await refresh();
    expect(document.querySelector(".igen-done-toast")!.closest(".ui-toasts")!.classList.contains("ui-toasts-top")).toBe(true);
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
    await refresh();
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
