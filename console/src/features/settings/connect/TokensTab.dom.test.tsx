// The tokens tab says what a token reaches, including the AF memory tools (#2329), and its link
// opens Settings > Agent memory > Settings. The text is static, so it is there even though no Agent answers.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn(async (): Promise<{ tokens: Array<Record<string, string>>; ceiling: string }> => ({ tokens: [], ceiling: "write" }));
const apiJSON = vi.fn(async (): Promise<{ token: string; endpoint?: string }> => ({ token: "" }));
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  apiJSON: (...args: unknown[]) => (apiJSON as (...a: unknown[]) => unknown)(...args),
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
    // A tenant Block withholds the tools from /mcp's list; the tab must say so rather than promise them.
    expect(text).toContain(t("tokens.can_memory_setting_post"));
    expect(t("tokens.can_memory_setting_post")).toMatch(/Block|ブロック/);
    // A write token with “Read only” is refused by the Agent, so the write line must name the setting.
    expect(t("tokens.can_memory_write")).toMatch(/Read and write|読み書き/);

    const link = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === t("tokens.can_memory_setting_link"));
    expect(link).toBeTruthy();
    await act(async () => link!.click());
    expect(useSettingsUI.getState().settingsSection).toBe("memory");
    expect(useSettingsUI.getState().settingsView).toBe("settings");
    expect(useSettingsUI.getState().settingsOpen).toBe(true);
  });

  // A PAT carries the member's full /mcp authority, so the tab says not to hand it to a session —
  // beside the issue form and again in the shown-once panel.
  it("warns against giving the token to a session, at the form and in the issued panel", async () => {
    api.mockImplementation(async () => ({ tokens: [], ceiling: "write" }));
    const warn = t("tokens.session_warning");
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root!.render(<TokensTab />));
    const count = () => (host!.textContent ?? "").split(warn).length - 1;
    expect(warn).toBeTruthy();
    // The warning must say the reach depends on the chosen scope (a read token cannot delete).
    expect(warn).toMatch(/scope|スコープ/);
    expect(count()).toBe(1);

    apiJSON.mockResolvedValueOnce({ token: "afp_secret" });
    const issue = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === t("tokens.issue"));
    await act(async () => issue!.click());
    expect(host.querySelector(".pat-issued")?.textContent).toContain(warn);
    expect(count()).toBe(2);
  });

  // #2764: a memory-only token is minted for /mcp/memory, its snippet points there, the
  // admin tier is not offered for it, and the list names each token's endpoint.
  it("issues a memory-only token for /mcp/memory and labels the endpoint", async () => {
    api.mockImplementation(async () => ({
      tokens: [{ id: "p1", name: "claude-ai", scope: "write", endpoint: "memory" }, { id: "p2", name: "desktop", scope: "read", endpoint: "mcp" }],
      ceiling: "admin:dangerous",
    }));
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root!.render(<TokensTab />));
    const selects = () => Array.from(host!.querySelectorAll("select"));
    const endpointSelect = selects().find((s) => Array.from(s.options).some((o) => o.value === "memory"))!;
    expect(endpointSelect).toBeTruthy();
    const scopeOptions = () => Array.from(selects().find((s) => Array.from(s.options).some((o) => o.value === "read"))!.options).map((o) => o.value);
    expect(scopeOptions()).toContain("admin:dangerous");
    await act(async () => {
      endpointSelect.value = "memory";
      endpointSelect.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(scopeOptions()).not.toContain("admin:dangerous");

    const rows = Array.from(host.querySelectorAll(".pat-table tbody tr:first-child")).map((r) => r.textContent ?? "");
    expect(rows[0]).toContain(t("tokens.endpoint_short_memory"));
    expect(rows[1]).toContain(t("tokens.endpoint_short_mcp"));

    apiJSON.mockResolvedValueOnce({ token: "afp_mem", endpoint: "memory" });
    const issue = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === t("tokens.issue"));
    await act(async () => issue!.click());
    expect(apiJSON).toHaveBeenLastCalledWith("api/pat", "POST", expect.objectContaining({ endpoint: "memory" }));
    const snippet = host.querySelector(".pat-tmpl")?.textContent ?? "";
    expect(snippet).toContain("/mcp/memory");
    expect(snippet).toContain("agent-fleet-memory");
  });
});
