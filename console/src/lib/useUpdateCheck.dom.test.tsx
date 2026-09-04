// Wording contract for the update toast. It is the only thing a user has to decide whether
// tapping is safe, so the two facts it carries must stay distinct:
//   * updating (reloading) does not stop running sessions - always say it;
//   * the backend moved too (the CP-detected `stale` flag) - only then say a workspace
//     stop -> start is needed, at a time of the user's choosing.
// Printing the second unconditionally would claim "restart required" for a Console-only
// update and cry wolf, devaluing the WS-bar badge along with it.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { UpdateToast } = await import("./useUpdateCheck.tsx");
const { useWorkspaceStore } = await import("../core/store/workspace.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(<UpdateToast server={{ time: "2026-07-29T00:00:00Z", sha: "abc1234" }} />);
  });
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
});

describe("update toast", () => {
  it("always says sessions keep running, and offers the update button", async () => {
    act(() => useWorkspaceStore.setState({ state: "running", stale: false }));
    await render();
    expect(host.textContent).toContain("セッションは止まりません");
    expect(host.textContent).not.toContain("停止→起動");
    expect(host.querySelector(".update-toast-btn")?.textContent).toBe("更新");
  });

  it("adds the stop→start note only when the backend also moved", async () => {
    act(() => useWorkspaceStore.setState({ state: "running", stale: true }));
    await render();
    expect(host.textContent).toContain("セッションは止まりません");
    expect(host.textContent).toContain("停止→起動");
  });

  it("picks the note up live, so a toast opened before the poll still tells the truth", async () => {
    act(() => useWorkspaceStore.setState({ state: "running", stale: false }));
    await render();
    expect(host.textContent).not.toContain("停止→起動");
    await act(async () => useWorkspaceStore.setState({ stale: true }));
    expect(host.textContent).toContain("停止→起動");
  });
});
