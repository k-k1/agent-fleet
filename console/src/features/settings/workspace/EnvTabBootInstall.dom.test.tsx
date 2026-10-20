// Settings › Toolchains names why a CLI is missing when the start-time install failed (full
// home above all), instead of a bare "—" that reads as "this kind is not offered".
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../sessions/store.ts", () => ({
  useSessionsStore: (sel: (s: unknown) => unknown) => sel({ sessions: [] }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { EnvTab } from "./EnvTab.tsx";

const toolchains = {
  node: "system", java: "", go: "system", timezone: "Asia/Tokyo",
  java_available: [], java_installed: [], node_options: ["system"], node_installed: [],
  go_options: ["system"], tz_options: ["Asia/Tokyo"],
};
const bin = (v: string) => ({ path: "/home/dev/.local/bin/x", version: v, raw: v });
const tools = [
  { name: "claude", pin: "2.1.303", effective: null, baked: null, userLocal: null, overridden: false,
    bootInstall: { status: "failed", reason: "no_space" } },
  { name: "codex", pin: "0.163.0", effective: bin("0.150.0"), baked: null, userLocal: bin("0.150.0"), overridden: false,
    bootInstall: { status: "failed", reason: "network" } },
  { name: "copilot", pin: "1.0.98", effective: bin("1.0.98"), baked: null, userLocal: bin("1.0.98"), overridden: false,
    bootInstall: { status: "ok" } },
  { name: "rtk", pin: "1.2.3", effective: null, baked: null, userLocal: null, overridden: false },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  api.mockReset();
  api.mockImplementation((path: string) => {
    if (path === "api/env/toolchains") return Promise.resolve(toolchains);
    if (path === "api/env/ws-settings") return Promise.resolve({ agentUpdate: false, allowAgentUpdate: false });
    return Promise.resolve({ tools, checkedAt: new Date().toISOString() });
  });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnvTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rowOf = (name: string) =>
  [...document.querySelectorAll(".tool-ver tbody tr")].find((r) => r.querySelector(".tool-ver-name")?.textContent === name)!;

describe("EnvTab boot-install outcome", () => {
  it("names the reason where the CLI is missing", async () => {
    await mount();
    expect(rowOf("claude").textContent).toContain("インストール失敗: home ディスク満杯");
  });
  it("also says so when an older copy is still there", async () => {
    await mount();
    const r = rowOf("codex").textContent!;
    expect(r).toContain("0.150.0");
    expect(r).toContain("インストール失敗: ネットワークエラー");
  });
  it("is silent for ok and for a tool with no record", async () => {
    await mount();
    expect(rowOf("copilot").textContent).not.toContain("インストール失敗");
    expect(rowOf("rtk").textContent).not.toContain("インストール失敗");
  });
});
