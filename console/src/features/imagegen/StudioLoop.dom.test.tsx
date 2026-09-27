// The studio pane's loop on a phone (lane L4), rendered whole: finished pictures announce
// themselves when the results are out of sight, the results tab counts them until it is
// opened, and the lightbox walks the results column and carries the picture verbs only in a
// studio.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Job, JobsResponse } from "./wire.ts";

const jobsNow: { jobs: Job[] } = { jobs: [] };
vi.mock("./api.ts", async (orig) => ({
  ...(await orig<typeof import("./api.ts")>()),
  imagegenStatus: async () => ({ enabled: true, ready: true, providers: [] }),
  imagegenJobs: async (): Promise<JobsResponse> => ({ jobs: jobsNow.jobs, groups: [] }) as JobsResponse,
  listStudios: async () => ({ studios: [] }),
  imagegenHistory: async () => ({ items: [] }),
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

const running: Job = { id: "g1", state: "running" };
const done = (n: number): Job => ({
  id: "g1",
  state: "done",
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

const mount = async (props: { active?: boolean } = {}) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <ImagegenView paneId="p1" active={props.active ?? true} />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
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

  it("does not announce what was already finished when the pane opened", async () => {
    narrowPane(true);
    jobsNow.jobs = [done(4)];
    await mount();
    await refresh();
    expect(document.querySelector(".igen-done-toast")).toBeNull();
    expect(tab(2).querySelector(".igen-tab-badge")).toBeNull();
  });
});

describe("the lightbox from the results", () => {
  it("walks the results column with ←/→, and shows no studio verbs without a studio", async () => {
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
    // The studio-less pane has no picture verbs (no studio to restore into).
    expect(document.querySelector(".mirror-lightbox-actions")).toBeNull();
  });
});
