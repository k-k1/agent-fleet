// The deployment branding panel (control-plane/brand.go).
//
// Two things are worth pinning. The palette comes from the SERVER — a Console with its own
// copy of the hexes would draw a swatch that does not match the favicon it promises. And
// saving has to reach the browser chrome immediately (title, favicon, the top bar chip),
// because the administrator is looking at that chrome while they pick the colour; being
// told to reload is exactly what "it did not work" looks like.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { BrandAdminView } from "./adminBrand.tsx";
import { useBrandStore } from "../../../lib/brand.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const status = (over: Record<string, unknown> = {}) => ({
  color: "teal",
  hex: "#149ba7",
  label: "",
  name: "Agent Fleet",
  source: "default",
  env: { color: "teal", label: "" },
  presets: [
    { name: "teal", hex: "#149ba7" },
    { name: "violet", hex: "#7c4dff" },
    { name: "orange", hex: "#e07a1f" },
  ],
  max_label: 16,
  ...over,
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<BrandAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const swatches = () => Array.from(host!.querySelectorAll<HTMLButtonElement>(".swatch"));
const labelInput = () => host!.querySelector<HTMLInputElement>("#brand-label")!;
const button = (text: string) =>
  Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === text);

// React tracks the input's value on the node, so assigning `.value` directly is swallowed:
// the change has to go through the native setter for onChange to fire.
const type = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const click = async (el: HTMLElement | undefined) => {
  await act(async () => {
    el?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  document.head.innerHTML =
    '<meta name="theme-color" content="#149ba7"><link rel="icon" href="brand/icon-192.png">';
  document.title = "Agent Fleet — Console";
  useBrandStore.setState({ label: "", color: "#149ba7", name: "Agent Fleet", ink: "#000" });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("BrandAdminView", () => {
  it("draws one swatch per server-sent preset, in the server's colours", async () => {
    api.mockResolvedValue(status());
    await mount();
    const s = swatches();
    expect(s).toHaveLength(3);
    // rgb(), because the DOM normalises the inline hex.
    expect(s[1].style.background).toBe("rgb(124, 77, 255)");
    expect(s[0].className).toContain("active"); // the colour in force
  });

  it("saves the colour and label, and moves the tab and the top bar with it", async () => {
    api.mockResolvedValue(status());
    apiJSON.mockResolvedValue(
      status({ color: "violet", hex: "#7c4dff", label: "dev", name: "[dev] Agent Fleet", source: "admin" }),
    );
    await mount();

    await click(swatches()[1]);
    await type(labelInput(), "dev");
    await click(button("保存") ?? button("Save"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/brand", "PUT", { color: "violet", label: "dev" });
    expect(document.title).toBe("[dev] Agent Fleet — Console");
    expect(document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')!.content).toBe("#7c4dff");
    // A no-store favicon is still the one the browser already painted: the href has to move.
    expect(document.querySelector<HTMLLinkElement>('link[rel="icon"]')!.getAttribute("href")).toContain("?b=");
    // What the top bar chip reads (it subscribes to this store).
    expect(useBrandStore.getState()).toMatchObject({ label: "dev", color: "#7c4dff" });
  });

  it("offers the reset only when this screen's setting is the one in force", async () => {
    api.mockResolvedValue(status());
    await mount();
    const reset = () => button("環境変数に戻す") ?? button("Follow the environment");
    expect(reset()).toBeUndefined();

    act(() => root!.unmount());
    host!.remove();
    api.mockResolvedValue(status({ source: "admin", label: "dev", name: "[dev] Agent Fleet" }));
    await mount();
    expect(reset()).toBeDefined();

    apiJSON.mockResolvedValue(status());
    await click(reset());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/brand", "DELETE");
    expect(useBrandStore.getState().label).toBe("");
  });

  it("keeps save disabled until something actually changed", async () => {
    api.mockResolvedValue(status({ source: "admin", color: "violet", hex: "#7c4dff", label: "dev" }));
    await mount();
    const save = () => (button("保存") ?? button("Save")) as HTMLButtonElement;
    expect(save().disabled).toBe(true);
    await click(swatches()[2]);
    expect(save().disabled).toBe(false);
  });
});
