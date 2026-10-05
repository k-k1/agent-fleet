// The Agent memory page while the workspace is not running (#1735). The history and the import
// need the Agent, so the page shows the start prompt instead; the Agent Fleet memory switch is a
// ui-pref the member sets for the next start, and this page is its only home, so it stays.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => false }));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: "stopped", start: () => {} }),
  wsStartBusy: () => false,
}));

const { MemoryTab } = await import("./MemoryTab.tsx");
const { setSettings, settingsDefaults, getSettings } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  setSettings(settingsDefaults());
});

describe("MemoryTab while the workspace is stopped", () => {
  it("still offers the Agent Fleet memory switch, both ways, without calling the Agent", async () => {
    setSettings(settingsDefaults());
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root!.render(<MemoryTab />));

    expect(host.textContent).toContain(t("mem.ws_required_title"));
    const row = Array.from(host.querySelectorAll(".ds-row")).find(
      (r) => r.querySelector(".ds-label")?.textContent === t("mem.af_switch"),
    );
    expect(row).toBeTruthy();
    expect(getSettings().agentMemory).toBe(false);
    const [on, off] = Array.from(row!.querySelectorAll<HTMLButtonElement>(".seg-btn"));
    await act(async () => on.click());
    expect(getSettings().agentMemory).toBe(true);
    await act(async () => off.click());
    expect(getSettings().agentMemory).toBe(false);
    expect(api).not.toHaveBeenCalled();
  });
});
