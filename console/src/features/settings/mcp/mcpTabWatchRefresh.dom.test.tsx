// While the server form is open, the tab re-reads the registry (on window focus and on a timer),
// so the Watch source checkbox enables when a tenant admin turns the switch on, without losing
// what the member already typed, and without leaking its timer or listener after unmount.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let watchSources = false;
const apiMock = vi.fn(async () => ({ servers: [], watchSources }));
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...a: unknown[]) => (apiMock as any)(...a),
  apiJSON: () => new Promise(() => {}),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

const { McpTab } = await import("./McpTab.tsx");
const { useWorkspaceStore } = await import("../../../core/store/workspace.ts");
const { setLocale } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  setLocale("en");
  vi.useFakeTimers();
  watchSources = false;
  apiMock.mockClear();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.useRealTimers();
});

const flush = () => act(async () => {});
const box = () =>
  [...host!.querySelectorAll<HTMLLabelElement>(".mcp-check")].find((l) => l.textContent === "Watch source")!.querySelector("input")!;

async function mountOpenForm() {
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<McpTab />));
  await flush();
  await act(async () => host!.querySelector<HTMLButtonElement>(".ssm-add-toggle")!.click());
  await flush();
}

describe("McpTab watch switch refresh", () => {
  it("enables the checkbox on window focus after the switch flips, keeping typed input", async () => {
    await mountOpenForm();
    expect(box().disabled).toBe(true);
    const name = host!.querySelector<HTMLInputElement>(".mcp-newform input[type=text], .mcp-newform input:not([type])")!;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setter.call(name, "typed");
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    watchSources = true;
    await act(async () => window.dispatchEvent(new Event("focus")));
    await flush();
    expect(box().disabled).toBe(false);
    expect(host!.querySelector<HTMLInputElement>(".mcp-newform input[type=text], .mcp-newform input:not([type])")!.value).toBe("typed");
  });

  it("enables the checkbox on the interval", async () => {
    await mountOpenForm();
    expect(box().disabled).toBe(true);
    watchSources = true;
    await act(async () => vi.advanceTimersByTime(30_000));
    await flush();
    expect(box().disabled).toBe(false);
  });

  it("stops polling and listening after unmount", async () => {
    await mountOpenForm();
    act(() => root!.unmount());
    root = null;
    apiMock.mockClear();
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
      vi.advanceTimersByTime(120_000);
    });
    expect(apiMock).not.toHaveBeenCalled();
  });
});
