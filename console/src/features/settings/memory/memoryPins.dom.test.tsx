// The pin list of AF memory (#1703): it lists every memory with its use count, and the button
// sends the pin for the right scope and project (the project id also in the query, for the audit).
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

import { MemoryPinsPanel } from "./memoryPins.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const onChanged = vi.fn();

const entries = [
  { name: "rules", scope: "user", description: "d", updated: "2026-10-01T00:00:00Z", pinned: true, uses: 7 },
  {
    name: "build-cmd",
    scope: "project",
    project: { id: "app-0123456789ab", root: "/r/app", vcs: "git", display: "app" },
    description: "d",
    updated: "2026-10-02T00:00:00Z",
    pinned: false,
    uses: 2,
  },
];

const flush = () =>
  act(async () => {
    for (let i = 0; i < 4; i++) await Promise.resolve();
  });

beforeEach(async () => {
  api.mockResolvedValue({ entries });
  apiJSON.mockResolvedValue({ name: "x" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <MemoryPinsPanel reload={0} onChanged={onChanged} />
      </ToastProvider>,
    );
  });
  await flush();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  onChanged.mockReset();
  document.body.innerHTML = "";
});

describe("MemoryPinsPanel", () => {
  it("lists every memory with its pin state and use count", () => {
    const rows = Array.from(host!.querySelectorAll(".mem-pin-row"));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("rules");
    expect(rows[0].textContent).toContain("7");
    expect(rows[0].querySelector("button")!.getAttribute("aria-pressed")).toBe("true");
    expect(rows[1].querySelector("button")!.getAttribute("aria-pressed")).toBe("false");
  });

  it("pins a project memory with its project id in the body and the query", async () => {
    const btn = host!.querySelectorAll<HTMLButtonElement>(".mem-pin-row button")[1];
    await act(async () => btn.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/pin?project=app-0123456789ab", "POST", {
      scope: "project",
      project: "app-0123456789ab",
      name: "build-cmd",
      pinned: true,
    });
    expect(onChanged).toHaveBeenCalled();
  });

  it("unpins a user-scope memory without a project", async () => {
    const btn = host!.querySelectorAll<HTMLButtonElement>(".mem-pin-row button")[0];
    await act(async () => btn.click());
    await flush();
    expect(apiJSON).toHaveBeenCalledWith("api/agents/memory/entries/pin", "POST", {
      scope: "user",
      project: "",
      name: "rules",
      pinned: false,
    });
  });
});
