// The Sessions view's Spaces section (ADR 0142 decision 9): absent while no session runs in a
// space, grouped by space once one does, and named from GET /api/spaces.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const api = vi.fn(async (url: string): Promise<unknown> =>
  url.startsWith("api/spaces")
    ? { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] }
    : {},
);

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (url: string) => api(url),
  isTransientErr: () => false,
  getTenant: () => "",
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
}

const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, ...extra });
const heads = () => [...host.querySelectorAll(".space-group-name")].map((e) => e.textContent);

beforeEach(() => {
  localStorage.clear();
  api.mockClear();
  useSpacesStore.setState({ spaces: [] });
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
    expect(host.querySelectorAll("[role=treeitem]").length).toBe(3);
  });
});
