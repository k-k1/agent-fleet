// The shared sessions live in their own activity-bar view (ADR 0140, note of 2026-11-21): the
// section lists them with nothing else around it, and says so when nothing is shared.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: vi.fn(() => Promise.resolve({})),
}));

const { SharedSessionsSection } = await import("./SharedSessionsSection.tsx");
const { useSharedSessionsStore } = await import("./store.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("SharedSessionsSection in its own view", () => {
  it("lists the shared sessions", async () => {
    useSharedSessionsStore.setState({
      sessions: [{ id: "s1", ownerUserKey: "o-x-com", ownerEmail: "o@x.com", name: "s1", kind: "claude", state: "stopped", permission: "ro", workspaceState: "running", repo: "proj", workingCopyId: "wc", title: "調査" }],
      refresh: () => Promise.resolve(),
    });
    await act(async () => root.render(<SharedSessionsSection />));
    expect(host.textContent).toContain("調査");
  });

  it("says nothing is shared instead of rendering a blank pane", async () => {
    useSharedSessionsStore.setState({ sessions: [], refresh: () => Promise.resolve() });
    await act(async () => root.render(<SharedSessionsSection />));
    expect(host.querySelector(".pane-empty")).not.toBeNull();
  });

});
