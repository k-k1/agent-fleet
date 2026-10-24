// The direction note must show even while the Workspace is stopped (or the registry is still
// loading), since that is when a member who wanted Outside access most needs the way there.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: () => new Promise(() => {}),
  apiJSON: () => new Promise(() => {}),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

const { McpTab } = await import("./McpTab.tsx");
const { useWorkspaceStore } = await import("../../../core/store/workspace.ts");
const { useSettingsUI } = await import("../store.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
async function mount(state: string) {
  useWorkspaceStore.setState({ state } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<McpTab />));
}
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

describe("McpTab direction note", () => {
  for (const [state, what] of [["stopped", "stopped"], ["running", "loading"]] as const) {
    it(`shows the note and link while ${what}`, async () => {
      await mount(state);
      const note = document.querySelector<HTMLElement>("[data-testid=direction-note]");
      expect(note).not.toBeNull();
      expect(note!.textContent).toContain(t("mcp.direction_link"));
      await act(async () => note!.querySelector("button")!.click());
      expect(useSettingsUI.getState().settingsSection).toBe("tokens");
    });
  }
});
