// The Node.js row of the toolchains tab, holding the same line as the Java row — node had
// the same hole (docs/decisions/0068): nodeOptions is a fixed list and can always offer a
// version that is not installed, yet selecting one installed nothing, resolvedToolchains()
// returned empty and sessions kept the old node, with no error or warning, until a
// Stop -> Start.
//   1. the install button appears only for a version that is not installed yet
//   2. the button POSTs /env/node-install and refetches the list once it finishes
//   3. the option itself reads as "not installed" (finding out after selecting is too late)
//   4. "system" (the image's own node) is never an install target
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
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { EnvTab } from "./EnvTab.tsx";

const toolchains = (installed: string[], node: string) => ({
  node,
  java: "",
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: [],
  java_installed: [],
  node_options: ["system", "20", "22", "24"],
  node_installed: installed,
  go_options: ["system"],
  tz_options: ["Asia/Tokyo"],
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
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  localStorage.clear();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

// Query by the per-row class. The shared layout class (.env-tool-pick) matches both the
// Java and the Node row, so querySelector returns the first one and the test silently
// inspects the wrong row.
const nodeBtn = () => document.querySelector<HTMLButtonElement>(".env-node-pick button");

describe("EnvTab Node.js row", () => {
  it("shows no button when the selected version is already installed", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "22") : {}),
    );
    await mount();
    expect(nodeBtn()).toBeNull();
  });

  it("shows no install button for system (the image's own node)", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains([], "system") : {}),
    );
    await mount();
    expect(nodeBtn()).toBeNull();
  });

  it("shows the install button for a missing version, and the option reads as not installed", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "24") : {}),
    );
    await mount();
    expect(nodeBtn()).not.toBeNull();
    const opts = Array.from(document.querySelectorAll<HTMLOptionElement>(".env-node-pick option"));
    const absent = opts.find((o) => o.value === "24")!;
    const present = opts.find((o) => o.value === "22")!;
    expect(present.textContent).toBe("v22");
    expect(absent.textContent).not.toBe("v24");
  });

  it("starts the install on click, refetches and drops the button", async () => {
    let installed = ["22"];
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(installed, "24") : {}),
    );
    apiJSON.mockImplementation(async () => {
      installed = ["22", "24"];
      return { state: "done", major: "24", node_installed: installed };
    });
    await mount();

    await act(async () => {
      nodeBtn()!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/node-install", "POST", { major: "24" });
    await act(async () => {
      await Promise.resolve();
    });
    expect(nodeBtn()).toBeNull();
  });

  it("locks both button and select while installing, preventing a second download", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "24") : { state: "installing" }),
    );
    apiJSON.mockResolvedValue({ state: "installing", major: "24" });
    await mount();

    await act(async () => {
      nodeBtn()!.click();
    });
    const btn = nodeBtn()!;
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toBe("インストール中…");
    expect(document.querySelector<HTMLSelectElement>(".env-node-pick select")!.disabled).toBe(true);
  });
});
