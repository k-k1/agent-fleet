// The CloudWatch / AWS MCP resource region is a RegionSelect (#1506). Picking a profile sets
// the region from it, and must re-decide "Other" even when the value does not change.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const profiles = [
  { id: "p1", label: "east", region: "us-east-1" },
  { id: "p2", label: "bare", region: "" },
];

vi.mock("../../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path === "api/connections") return { cloudwatch: { connected: false }, aws: { connected: false } };
    if (path === "api/ssm/profiles") return profiles;
    return {};
  }),
  apiJSON: vi.fn(async () => ({ error: { code: "conn_api_key_required", message: "enter an API key" } })),
  errText: (e: { code?: string; message?: string }) => (e.code === "conn_api_key_required" ? "LOCALIZED" : (e.message ?? "")),
  raw: vi.fn(async () => ({ ok: true, status: 204 })),
  getTenant: () => "",
  setTenant: () => {},
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: async () => {} }),
  wsStartBusy: () => false,
}));
const toasts: string[] = [];
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));

const { OpsTab } = await import("./OpsTab.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  host.remove();
});

async function set(el: HTMLInputElement | HTMLSelectElement, value: string): Promise<void> {
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}

describe("OpsTab CloudWatch region", () => {
  it("leaves Other when a profile is picked, even one whose region equals the typed code", async () => {
    await act(async () => root!.render(<OpsTab />));
    await act(async () => {});
    // CloudWatch renders the first region picker; its profile select is the row's first select.
    const region = () => host.querySelector<HTMLElement>(".region-select")!;
    const regionSel = () => region().querySelector("select")!;
    const profileSel = () => region().parentElement!.querySelector<HTMLSelectElement>("select")!;

    await set(regionSel(), regionSel().options[regionSel().options.length - 1].value);
    await set(region().querySelector("input")!, "us-east-1");
    await set(profileSel(), "p1");
    expect(region().querySelector("input")).toBeNull();
    expect(regionSel().value).toBe("us-east-1");

    await set(regionSel(), regionSel().options[regionSel().options.length - 1].value);
    await set(region().querySelector("input")!, "");
    await set(profileSel(), "p2");
    expect(region().querySelector("input")).toBeNull();
    expect(regionSel().value).toBe("");
  });
});

describe("OpsTab connect failure", () => {
  it("shows the localized text for a known error code, not the Agent's raw English message", async () => {
    await act(async () => root!.render(<OpsTab />));
    await act(async () => {});
    const key = host.querySelector<HTMLInputElement>('input[type="password"]')!;
    await set(key, "abc");
    const btn = Array.from(host.querySelectorAll<HTMLButtonElement>("button")).find((b) => !b.disabled && /connect|接続/i.test(b.textContent ?? ""))!;
    await act(async () => btn.click());
    expect(toasts.join(" ")).toContain("LOCALIZED");
    expect(toasts.join(" ")).not.toContain("enter an API key");
  });
});
