// The tokens tab says what a token reaches, including the AF memory tools (#2329), and its link
// opens Settings > Agent memory > Settings. The text is static, so it is there even though no Agent answers.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn(async () => ({ tokens: [], ceiling: "write" }));
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => (api as (...a: unknown[]) => unknown)(...args),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));

const { TokensTab } = await import("./TokensTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSettingsUI.setState({ settingsOpen: false, settingsSection: "tokens", settingsView: null });
});

describe("TokensTab what-this-token-can-do section", () => {
  it("lists the memory tools and opens Agent memory from its link", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root!.render(<TokensTab />));

    const text = host.textContent ?? "";
    for (const tool of ["memory_projects", "memory_index", "memory_search", "memory_read", "memory_save", "memory_forget"]) {
      expect(text).toContain(tool);
    }
    expect(text).toContain(t("tokens.can_stopped"));
    // A write token with “Read only” is refused by the Agent, so the write line must name the setting.
    expect(t("tokens.can_memory_write")).toMatch(/Read and write|読み書き/);

    const link = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === t("tokens.can_memory_setting_link"));
    expect(link).toBeTruthy();
    await act(async () => link!.click());
    expect(useSettingsUI.getState().settingsSection).toBe("memory");
    expect(useSettingsUI.getState().settingsView).toBe("settings");
    expect(useSettingsUI.getState().settingsOpen).toBe(true);
  });
});
