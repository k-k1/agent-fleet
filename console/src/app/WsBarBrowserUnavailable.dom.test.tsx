// The WS bar's preview popover on a workspace whose runtime offers no browser features
// (features/browser/availability.ts, ADR 0106). "Open in pane" stays visible but disabled
// with the reason on its tooltip and in the hint, the lightweight preview keeps working, and
// no attachment list is requested from an Agent that would only refuse it. On every other
// runtime the popover is unchanged.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn((..._args: unknown[]) => Promise.resolve({}));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));
vi.mock("./usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

import { WsBar } from "./WsBar.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { t } from "../lib/i18n/index.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function openPopover(browserUnavailable: string) {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  useWorkspaceStore.setState({ state: "running", browserUnavailable });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <WsBar />
        </ConfirmProvider>
      </ToastProvider>,
    ));
  const btn = host.querySelector<HTMLButtonElement>(".ws-preview-btn")!;
  await act(async () => btn.click());
  const port = host.querySelector<HTMLInputElement>(".preview-port")!;
  // React tracks the input's value through its own setter; set it the way a keystroke does.
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(port, "3000");
    port.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const buttonByText = (text: string) =>
  [...host!.querySelectorAll<HTMLButtonElement>(".ws-preview-pop button")].find((b) => b.textContent === text)!;
const attachmentCalls = () => api.mock.calls.filter((c) => String(c[0]).startsWith("api/browser/")).length;

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockClear();
  useWorkspaceStore.setState({ browserUnavailable: "" });
});

describe("WS bar preview popover", () => {
  it("disables Open in pane with the reason where the runtime has no browser features", async () => {
    await openPopover("kubernetes");
    const pane = buttonByText(t("wsbar.preview.open_pane"));
    expect(pane.disabled).toBe(true);
    expect(pane.title).toBe(t("browser.unavailable.short", { runtime: "kubernetes" }));
    expect(buttonByText(t("wsbar.preview.open_light")).disabled).toBe(false);
    expect(host!.querySelector(".pv-hint")!.textContent).toBe(
      t("wsbar.preview.hint_unavailable", { runtime: "kubernetes" }),
    );
    expect(attachmentCalls()).toBe(0);
  });

  it("is unchanged on a runtime with browser features", async () => {
    await openPopover("");
    const pane = buttonByText(t("wsbar.preview.open_pane"));
    expect(pane.disabled).toBe(false);
    expect(pane.title).toBe("");
    expect(host!.querySelector(".pv-hint")!.textContent).toBe(t("wsbar.preview.hint"));
    expect(attachmentCalls()).toBe(1);
  });
});
