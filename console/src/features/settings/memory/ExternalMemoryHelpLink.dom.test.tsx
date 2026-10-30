// The "How to connect" link next to the externalMemory control lands on the MCP tokens tab (#2329).
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ExternalMemorySwitch } from "./memoryChanges.tsx";
import { useSettingsUI } from "../store.ts";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSettingsUI.setState({ settingsOpen: false, settingsSection: "memory" });
});

describe("ExternalMemoryHelpLink", () => {
  it("is shown with the external switch and opens the MCP tokens tab", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => root!.render(<ExternalMemorySwitch />));

    const link = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === t("mem.ext_help"));
    expect(link).toBeTruthy();
    await act(async () => link!.click());
    expect(useSettingsUI.getState().settingsSection).toBe("tokens");
    expect(useSettingsUI.getState().settingsOpen).toBe(true);
  });
});
