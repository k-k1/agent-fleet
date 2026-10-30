// The direction lines at the top of the MCP servers and MCP tokens tabs link to each other.
// The link calls openSettings(), whose follow-effect only fires when the requested section
// CHANGES in the store — so after the member moved with the rail, the store must already say
// where they are, or the link back to the section they started on does nothing.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { DirectionNote } from "./parts/DirectionNote.tsx";

vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("./mcp/McpTab.tsx", () => ({
  McpTab: () => <DirectionNote text="mcp.direction" linkLabel="mcp.direction_link" target="tokens" />,
}));
vi.mock("./connect/TokensTab.tsx", () => ({
  TokensTab: () => <DirectionNote text="tokens.direction" linkLabel="tokens.direction_link" target="mcp" />,
}));

const { SettingsDialog } = await import("./SettingsDialog.tsx");
const { useSettingsUI } = await import("./store.ts");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function open(section: string) {
  act(() => useSettingsUI.getState().openSettings(section));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<SettingsDialog />));
}
const active = () => document.querySelector(".settings-rail-item.active")?.textContent;
const railItem = (label: string) =>
  [...document.querySelectorAll<HTMLElement>(".settings-rail-item")].find((e) => e.textContent === label)!;
const note = () => document.querySelector<HTMLElement>("[data-testid=direction-note]")!;
const layout = () => document.querySelector(".settings-layout")!;

afterEach(() => {
  act(() => root?.unmount());
  act(() => useSettingsUI.getState().closeSettings());
  host?.remove();
  root = host = null;
});

describe.each([
  ["mcp", "set.tab_mcp", "tokens", "set.tab_tokens"],
  ["tokens", "set.tab_tokens", "mcp", "set.tab_mcp"],
] as const)("cross-link between %s and %s", (start, startLabel, other, otherLabel) => {
  it("returns to the starting tab after the rail moved to the other one", async () => {
    await open(start);
    expect(active()).toBe(t(startLabel));
    // Rail → the other tab, then the other tab's direction link → back to the start.
    await act(async () => railItem(t(otherLabel)).click());
    expect(active()).toBe(t(otherLabel));
    await act(async () => note().querySelector("button")!.click());
    expect(active()).toBe(t(startLabel));
    expect(useSettingsUI.getState().settingsSection).toBe(start);
    // On a phone the jump drills in, so mobile back (entered=false) still returns to the list.
    expect(layout().classList.contains("entered")).toBe(true);
    expect(other).not.toBe(start);
  });
});
