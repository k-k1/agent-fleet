// The reset button lives inside the "Shortcut assignments" heading (#1480) so the heading's
// underline spans the row. A screen reader moving by headings must still hear only the title,
// not "… Reset all to defaults" appended to it.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
import { KeysTab } from "./KeysTab.tsx";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("KeysTab — assignment heading", () => {
  it("names the heading by its title only", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<KeysTab />);
    });
    const h = Array.from(host.querySelectorAll("h4.kb-head")).find((el) => el.querySelector("button"))!;
    const label = document.getElementById(h.getAttribute("aria-labelledby") || "");
    expect(label?.textContent).toBe(t("keys.kt.assignTitle"));
    expect(label?.contains(h.querySelector("button"))).toBe(false);
  });
});
