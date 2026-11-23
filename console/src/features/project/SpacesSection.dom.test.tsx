// The Sessions view's Spaces section (ADR 0142 decision 9): absent while no session runs in a
// space, grouped by space once one does, and named from GET /api/spaces.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
let spacesAnswer: () => unknown = () => LIST;
const api = vi.fn(async (url: string): Promise<unknown> => (url.startsWith("api/spaces") ? spacesAnswer() : {}));
let tenant = "";

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (url: string) => api(url),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => tenant,
  getUser: () => "",
}));

const { SpacesSection } = await import("./SpacesSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useProjectFilter } = await import("./filter.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const flush = () =>
  act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

// The section asks from a timer (0 ms the first time): let it run.
const tick = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 5));
  });

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <SpacesSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
  if (!vi.isFakeTimers()) await tick();
}

const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, ...extra });
const spaceCalls = () => api.mock.calls.filter(([u]) => u === "api/spaces").length;
const inSpace = (name: string, folder: string) => sess(name, { repo: folder, dir: "/home/dev/spaces/" + folder, space: folder });
const heads = () => [...host.querySelectorAll(".space-group-name")].map((e) => e.textContent);

beforeEach(() => {
  localStorage.clear();
  api.mockClear();
  spacesAnswer = () => LIST;
  tenant = "";
  useSpacesStore.getState().clear();
  useProjectFilter.setState({ q: "" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("Spaces section", () => {
  it("is absent while no session runs in a space, and asks for nothing", async () => {
    useSessionsStore.setState({ sessions: [sess("a", { repo: "app" }), sess("b")] });
    await render();
    expect(host.querySelector(".ui-section")).toBeNull();
    expect(api).not.toHaveBeenCalledWith("api/spaces");
  });

  it("groups space sessions under the space's display name", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("s1", { repo: "q3-abcdef", dir: "/home/dev/spaces/q3-abcdef", space: "q3-abcdef" }),
        sess("s2", { repo: "q3-abcdef", dir: "/home/dev/spaces/q3-abcdef", space: "q3-abcdef" }),
        sess("s3", { repo: "unnamed-zz", dir: "/home/dev/spaces/unnamed-zz", space: "unnamed-zz" }),
        sess("r1", { repo: "app" }),
      ],
    });
    await render();
    expect(api).toHaveBeenCalledWith("api/spaces");
    // A space the list does not name shows its folder.
    expect(heads().sort()).toEqual(["Q3 report", "unnamed-zz"]);
    expect(host.querySelectorAll("[role=group] [role=treeitem]").length).toBe(3);
  });

  it("asks again after a transient failure, and shows the names once it answers", async () => {
    vi.useFakeTimers();
    try {
      spacesAnswer = () => ({ error: { code: "http_502", status: 502 } });
      useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
      await render();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(heads()).toEqual(["q3-abcdef"]);
      expect(spaceCalls()).toBe(1);
      spacesAnswer = () => LIST;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      await flush();
      expect(spaceCalls()).toBe(2);
      expect(heads()).toEqual(["Q3 report"]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("asks again for another workspace, even for a folder the old list named", async () => {
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
    await render();
    expect(heads()).toEqual(["Q3 report"]);
    const before = spaceCalls();
    // The other tenant's Agent knows the same folder by another name.
    tenant = "other";
    spacesAnswer = () => ({ ...LIST, spaces: [{ ...LIST.spaces[0], name: "Other tenant's" }] });
    await act(async () => {
      useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef"), inSpace("s2", "q3-abcdef")] });
    });
    await tick();
    await flush();
    expect(spaceCalls()).toBeGreaterThan(before);
    expect(heads()).toEqual(["Other tenant's"]);
  });

  it("matches the search box against the space's name", async () => {
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef"), inSpace("s2", "zz-abcdef")] });
    await render();
    await act(async () => {
      useProjectFilter.setState({ q: "q3 rep" });
    });
    expect(heads()).toEqual(["Q3 report"]);
    expect(host.querySelectorAll("[role=treeitem] [role=group] > *").length).toBe(1);
  });

  it("names the tree and makes each space its parent node", async () => {
    useSessionsStore.setState({ sessions: [inSpace("s1", "q3-abcdef")] });
    await render();
    const tree = host.querySelector("[role=tree]");
    expect(tree?.getAttribute("aria-label")).toBeTruthy();
    const node = tree?.querySelector(":scope > [role=treeitem]");
    expect(node?.getAttribute("aria-label")).toBe("Q3 report");
    expect(node?.querySelector("[role=group] [role=treeitem]")).not.toBeNull();
  });
});
