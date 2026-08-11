import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserAttachTargetSwitcher } from "./BrowserAttachTargetSwitcher.tsx";
import type { BrowserAttachmentSiblingTarget } from "./attachmentController.ts";

vi.mock("../../lib/i18n/index.ts", () => ({ useT: () => (key: string) => key }));

describe("BrowserAttachTargetSwitcher", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(() => {
    act(() => root.unmount());
    host.remove();
  });

  const targets: BrowserAttachmentSiblingTarget[] = [
    { targetId: "target-1", title: "Episode 1", url: "https://example.invalid/one", current: true },
    { targetId: "target-2", title: "Episode 2", url: "https://example.invalid/two", current: false },
  ];

  it("loads sibling targets on click, hides the current one, and lets another be selected", async () => {
    const listSiblingTargets = vi.fn().mockResolvedValue(targets);
    const onSelect = vi.fn().mockResolvedValue(undefined);
    act(() => {
      root.render(<BrowserAttachTargetSwitcher listSiblingTargets={listSiblingTargets} onSelect={onSelect} />);
    });

    const button = host.querySelector<HTMLButtonElement>(".browser-attach-switch-btn")!;
    await act(async () => {
      button.click();
      await Promise.resolve();
    });
    expect(listSiblingTargets).toHaveBeenCalledTimes(1);

    // The current target is what the toolbar already shows — the picker only
    // lists what a click could actually switch to.
    const items = Array.from(document.querySelectorAll<HTMLButtonElement>(".browser-attach-switch-item"));
    expect(items).toHaveLength(1);
    expect(items[0].textContent).toContain("Episode 2");

    await act(async () => {
      items[0].click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onSelect).toHaveBeenCalledWith("target-2");
    // A successful switch closes the menu.
    expect(document.querySelector(".browser-attach-switch-menu")).toBeNull();
  });

  it("keeps the menu open so the user can try another tab when a switch fails", async () => {
    const listSiblingTargets = vi.fn().mockResolvedValue(targets);
    const onSelect = vi.fn().mockRejectedValue(new Error("boom"));
    act(() => {
      root.render(<BrowserAttachTargetSwitcher listSiblingTargets={listSiblingTargets} onSelect={onSelect} />);
    });
    host.querySelector<HTMLButtonElement>(".browser-attach-switch-btn")!.click();
    await act(async () => { await Promise.resolve(); });

    const item = document.querySelector<HTMLButtonElement>(".browser-attach-switch-item")!;
    await act(async () => {
      item.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onSelect).toHaveBeenCalledWith("target-2");
    expect(document.querySelector(".browser-attach-switch-menu")).not.toBeNull();
  });

  it("shows an empty-state caption when there are no other tabs", async () => {
    const listSiblingTargets = vi.fn().mockResolvedValue([targets[0]]);
    act(() => {
      root.render(<BrowserAttachTargetSwitcher listSiblingTargets={listSiblingTargets} onSelect={vi.fn()} />);
    });
    host.querySelector<HTMLButtonElement>(".browser-attach-switch-btn")!.click();
    await act(async () => { await Promise.resolve(); });
    expect(document.querySelectorAll(".browser-attach-switch-item")).toHaveLength(0);
    expect(document.querySelector(".ui-menu-caption")?.textContent).toBe("browser.attach.switch_tab_empty");
  });
});
