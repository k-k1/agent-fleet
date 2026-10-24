// The "Installed versions" list of the toolchains tab. What matters:
//   1. it shows what the agent lists, and an Uninstall button only where the agent says
//      the tree may go (never the workspace image's copy, never a refused one)
//   2. nothing is deleted without confirmation
//   3. a confirmed removal DELETEs that major, shows the new list and refetches the tab
//   4. a refusal from the agent is reported, not swallowed
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
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../sessions/store.ts", () => ({
  useSessionsStore: (sel: (s: unknown) => unknown) => sel({ sessions: [] }),
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
const confirm = vi.fn();
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => confirm }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { EnvTab } from "./EnvTab.tsx";

const toolchains = {
  node: "22",
  java: "21",
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: ["17", "21"],
  java_installed: ["17", "21"],
  java_versions: { "17": "17.0.21+9", "21": "21.0.13+9" },
  node_options: ["system", "22", "24"],
  node_installed: ["22", "24"],
  node_versions: { "22": "v22.23.4", "24": "v24.1.0" },
  go_options: ["system"],
  tz_options: ["Asia/Tokyo"],
};

const GiB = 1073741824;
const listing = () => ({
  java: [
    { major: "17", version: "17.0.21+9", source: "home", bytes: GiB / 4, in_use: false, removable: true },
    { major: "21", version: "21.0.13+9", source: "home", bytes: GiB / 3, in_use: true, removable: true },
    { major: "21", version: "21.0.11+10", source: "system", bytes: 0, in_use: false, removable: false },
  ],
  node: [
    { major: "22", version: "v22.23.4", source: "home", bytes: GiB / 5, in_use: true, removable: false, reason: "selected" },
    { major: "24", version: "v24.1.0", source: "home", bytes: GiB / 5, in_use: false, removable: true },
  ],
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

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
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  toast.mockReset();
  confirm.mockReset();
  localStorage.clear();
  api.mockImplementation((path: string) => {
    if (path === "api/env/toolchains") return Promise.resolve(toolchains);
    if (path === "api/env/toolchain-installs") return Promise.resolve(listing());
    return Promise.resolve({});
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const rows = () => Array.from(document.querySelectorAll<HTMLTableRowElement>(".env-installed tbody tr"));
const rowOf = (tool: string, version: string) =>
  rows().find((r) => r.cells[0].textContent === tool && r.cells[1].textContent!.startsWith(version))!;
const btnOf = (tool: string, version: string) => rowOf(tool, version).querySelector("button");

describe("EnvTab installed versions", () => {
  it("lists every tree and offers Uninstall only where the agent allows it", async () => {
    await mount();
    expect(rows()).toHaveLength(5);
    expect(btnOf("Temurin 17", "17.0.21+9")).not.toBeNull();
    // The image's copy has no button and no size.
    expect(btnOf("Temurin 21", "21.0.11+10")).toBeNull();
    expect(rowOf("Temurin 21", "21.0.11+10").cells[2].textContent).toBe("ワークスペースのイメージ");
    // Selected Node major: refused, with the reason.
    expect(btnOf("Node.js v22", "v22.23.4")).toBeNull();
    expect(rowOf("Node.js v22", "v22.23.4").cells[4].textContent).toContain("選択中");
    expect(rowOf("Temurin 21", "21.0.13+9").cells[1].textContent).toContain("使用中");
  });

  it("deletes nothing when the confirmation is declined", async () => {
    confirm.mockResolvedValue(false);
    await mount();
    await act(async () => {
      btnOf("Node.js v24", "v24.1.0")!.click();
    });
    expect(confirm).toHaveBeenCalled();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("DELETEs the major after confirmation, shows the new list and refetches the tab", async () => {
    confirm.mockResolvedValue(true);
    const after = listing();
    after.node = after.node.filter((e) => e.major !== "24");
    apiJSON.mockResolvedValue(after);
    await mount();
    const before = api.mock.calls.filter((c) => c[0] === "api/env/toolchains").length;
    await act(async () => {
      btnOf("Node.js v24", "v24.1.0")!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/node/24", "DELETE");
    expect(rows().some((r) => r.cells[0].textContent === "Node.js v24")).toBe(false);
    expect(api.mock.calls.filter((c) => c[0] === "api/env/toolchains").length).toBeGreaterThan(before);
  });

  it("reports the agent's refusal", async () => {
    confirm.mockResolvedValue(true);
    apiJSON.mockResolvedValue({ error: { code: "selected", message: "Temurin 17 is the selected JDK" } });
    await mount();
    await act(async () => {
      btnOf("Temurin 17", "17.0.21+9")!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/jdk/17", "DELETE");
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Temurin 17 is the selected JDK"));
    expect(btnOf("Temurin 17", "17.0.21+9")).not.toBeNull();
  });

  it("shows nothing when the listing fails", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/env/toolchains") return Promise.resolve(toolchains);
      if (path === "api/env/toolchain-installs") return Promise.reject(new Error("offline"));
      return Promise.resolve({});
    });
    await mount();
    expect(document.querySelector(".env-installed")).toBeNull();
  });
});
