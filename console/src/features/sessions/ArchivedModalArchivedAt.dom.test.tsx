// The shelf row shows when the session was archived, from the row's archivedAt; a row without
// the stamp shows nothing extra.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, api: (...a: unknown[]) => api(...a) };
});

const { ArchivedModal } = await import("./ArchivedModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("./store.ts");

let root: Root | null = null;

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
  api.mockReset();
  useSessionsStore.setState({ sessions: [] });
});

describe("ArchivedModal archivedAt", () => {
  it("shows the archive date only for a stamped row", async () => {
    api.mockResolvedValue({
      sessions: [
        { name: "stamped", kind: "claude", title: "stamped", dir: "/r/a", archivedAt: "2026-11-05T09:30:00Z" },
        { name: "plain", kind: "claude", title: "plain", dir: "/r/b" },
      ],
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <ArchivedModal />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    const subs = new Map(
      [...document.querySelectorAll(".arch-row")].map((r) => [
        r.querySelector(".arch-name")!.textContent,
        r.querySelector(".arch-sub")!.textContent ?? "",
      ]),
    );
    expect(subs.get("stamped")).toMatch(/11\/5|Nov/);
    expect(subs.get("plain")).not.toMatch(/11\/5|Nov/);
  });
});
