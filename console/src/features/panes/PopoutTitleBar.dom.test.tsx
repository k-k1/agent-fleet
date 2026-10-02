// Expand is held back until boot restores the pop-out's layout: expanded first, boot loads the
// full console's profile and the popped pane saved under the split one never comes back.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useLayoutStore } from "../../layout/store.ts";
import { PopoutTitleBar } from "./PopoutTitleBar.tsx";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const expandButton = (): HTMLButtonElement => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<PopoutTitleBar />));
  return host.querySelector("button.ui-iconbtn") as HTMLButtonElement;
};

describe("PopoutTitleBar expand", () => {
  it("is disabled until the layout is restored", () => {
    useLayoutStore.setState({ hydrated: false });
    const btn = expandButton();
    expect(btn.disabled).toBe(true);
    act(() => useLayoutStore.setState({ hydrated: true }));
    expect(btn.disabled).toBe(false);
  });
});
