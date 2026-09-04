// The standalone preview-subdomain tab. What matters is never offering a setting that does
// nothing on a deployment that issues no subdomain:
//   1. usePreviewAvailable is false when previewDomain is empty (so the rail hides it)
//   2. it is null until the answer arrives (so no entry appears and then vanishes)
//   3. reached directly anyway, the tab says "not available" instead of rendering blank
//   4. where subdomains are issued the settings appear, and the published ports are saved
//      on blur
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));

const wsSettings = (extra: Record<string, unknown> = {}) => ({
  agentUpdate: false,
  allowAgentUpdate: false,
  previewDomain: "example.invalid",
  previewPorts: [3000, 8080],
  previewUrls: { "8080": "https://slug-8080.example.invalid", "3000": "https://slug-3000.example.invalid" },
  previewMaxPorts: 8,
  ...extra,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

// The availability answer is cached at module scope (so settings do not refetch on every
// open), so each test re-imports the module; reusing it would make every later test read
// the first one's answer.
async function freshModule() {
  vi.resetModules();
  return await import("./PreviewTab.tsx");
}

async function mount(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("usePreviewAvailable", () => {
  const probe = (usePreviewAvailable: () => boolean | null) => {
    return function Probe() {
      const v = usePreviewAvailable();
      return <span id="v">{v === null ? "pending" : String(v)}</span>;
    };
  };
  const read = () => document.querySelector("#v")!.textContent;

  it("is true when previewDomain is set", async () => {
    api.mockResolvedValue(wsSettings());
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("true");
  });

  it("is false when previewDomain is empty, so the rail hides the tab", async () => {
    api.mockResolvedValue(wsSettings({ previewDomain: "" }));
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("false");
  });

  it("is null until the answer arrives, so nothing appears and then vanishes", async () => {
    api.mockReturnValue(new Promise(() => {})); // never resolves
    const { usePreviewAvailable } = await freshModule();
    const P = probe(usePreviewAvailable);
    await mount(<P />);
    expect(read()).toBe("pending");
  });
});

describe("PreviewTab", () => {
  it("says not available, rather than showing settings, where nothing is issued", async () => {
    api.mockResolvedValue(wsSettings({ previewDomain: "" }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    expect(document.querySelector(".ds-group")).toBeNull();
    expect(document.querySelector(".pad")!.textContent).toContain("発行されません");
  });

  it("lists the current URLs in port order where subdomains are issued", async () => {
    api.mockResolvedValue(wsSettings());
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const urls = Array.from(document.querySelectorAll<HTMLAnchorElement>(".pv-current-url")).map((a) => a.textContent);
    expect(urls).toEqual(["slug-3000.example.invalid", "slug-8080.example.invalid"]);
  });

  it("saves the published ports on blur, not on every keystroke", async () => {
    api.mockResolvedValue(wsSettings());
    apiJSON.mockResolvedValue(wsSettings({ previewPorts: [3000, 5173] }));
    const { PreviewTab } = await freshModule();
    await mount(<PreviewTab />);
    const input = document.querySelector<HTMLInputElement>(".ds-select")!;
    expect(input.value).toBe("3000, 8080");
    await act(async () => {
      // Set the value on a React-controlled input by calling the value setter directly.
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "3000, 5173");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(apiJSON).not.toHaveBeenCalled(); // half-typed "3000, 5" must not be saved
    await act(async () => {
      // React onBlur rides on the native focusout; a non-bubbling blur never reaches it.
      input.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/ws-settings", "PUT", { previewPorts: [3000, 5173] });
  });
});
