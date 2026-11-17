// TabbedViews keeps every view mounted and hides the inactive ones. Pinned here: a view that says
// it should not exist has no tab and names no tab, unless it holds unsaved edits, which keep the
// tab so the edits are never left where no tab leads.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { TabbedViews } from "./tabbedViews.tsx";
import { useReportDirty } from "./dirtyScope.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function Edited({ dirty }: { dirty: boolean }) {
  useReportDirty(dirty);
  return <p>edited</p>;
}

async function mount(extraDirty: boolean) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <TabbedViews
        label="views"
        prefix="t"
        views={[
          { key: "a", label: "A", node: <p>a</p> },
          { key: "extra", label: "Extra", shown: false, node: <Edited dirty={extraDirty} /> },
        ]}
      />,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const tabs = () => Array.from(host!.querySelectorAll('[role="tab"]')).map((t) => t.textContent);

describe("TabbedViews", () => {
  it("keeps a view without a tab mounted, hidden, and naming no tab", async () => {
    await mount(false);
    expect(tabs()).toEqual(["A"]);
    const panel = host!.querySelector<HTMLElement>("#t-panel-extra")!;
    expect(panel.hidden).toBe(true);
    expect(panel.textContent).toBe("edited");
    expect(panel.hasAttribute("aria-labelledby")).toBe(false);
    expect(host!.querySelector("#t-panel-a")!.getAttribute("aria-labelledby")).toBe("t-tab-a");
  });

  it("keeps the tab of a view holding unsaved edits even when it says it should not exist", async () => {
    await mount(true);
    expect(tabs()).toEqual(["A", "Extra●"]);
    expect(host!.querySelector("#t-panel-extra")!.getAttribute("aria-labelledby")).toBe("t-tab-extra");
  });
});
