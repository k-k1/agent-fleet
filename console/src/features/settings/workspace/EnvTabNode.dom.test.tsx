// The Node.js row of the toolchains tab, holding the same line as the Java row — node had
// the same hole (docs/decisions/0068): nodeOptions is a fixed list and can always offer a
// version that is not installed, yet selecting one installed nothing, resolvedToolchains()
// returned empty and sessions kept the old node, with no error or warning, until a
// Stop -> Start.
//   1. the install button appears only for a version that is not installed yet
//   2. the button POSTs /env/node-install and refetches the list once it finishes
//   3. the option itself reads as "not installed" (finding out after selecting is too late)
//   4. "system" (the image's own node) is never an install target
//   5. the button names the version it would install when the agent can tell
//   6. an installed version shows its exact patch and offers an update only when the
//      agent says a newer one exists; another major's reply is never taken as ours
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
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { EnvTab } from "./EnvTab.tsx";

const toolchains = (installed: string[], node: string, versions: Record<string, string> = {}) => ({
  node,
  java: "",
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: [],
  java_installed: [],
  node_options: ["system", "20", "22", "24"],
  node_installed: installed,
  node_versions: versions,
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
  toast.mockReset();
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

  it("names the version Install would fetch, and falls back to plain Install offline", async () => {
    for (const [latest, label] of [
      [() => Promise.resolve({ major: "24", current: "", latest: "v24.11.0", update_available: false }), "v24.11.0 をインストール"],
      [() => Promise.reject(new Error("node_latest_unavailable")), "インストール"],
    ] as const) {
      api.mockImplementation((path: string) => {
        if (path === "api/env/toolchains") return Promise.resolve(toolchains(["22"], "24"));
        if (path === "api/env/node-latest?major=24") return latest();
        return Promise.resolve({});
      });
      await mount();
      await act(async () => {
        await Promise.resolve();
      });
      expect(document.querySelector<HTMLButtonElement>(".env-node-pick button")!.textContent).toBe(label);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("shows the resolved patch and offers an update when a newer one exists", async () => {
    let versions: Record<string, string> = { "22": "v22.23.4" };
    api.mockImplementation((path: string) => {
      if (path === "api/env/toolchains") return Promise.resolve(toolchains(["22"], "22", versions));
      if (path === "api/env/node-latest?major=22") {
        const cur = versions["22"];
        return Promise.resolve({ major: "22", current: cur, latest: "v22.23.5", update_available: cur !== "v22.23.5" });
      }
      return Promise.resolve({});
    });
    apiJSON.mockImplementation(async () => {
      versions = { "22": "v22.23.5" };
      return { state: "done", major: "22", node_installed: ["22"], node_versions: versions };
    });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.querySelector(".env-node-version")!.textContent).toBe("v22.23.4");
    expect(nodeBtn()!.textContent).toBe("v22.23.5 に更新");

    await act(async () => {
      nodeBtn()!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/node-install", "POST", { major: "22" });
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.querySelector(".env-node-version")!.textContent).toBe("v22.23.5");
    expect(nodeBtn()).toBeNull();
  });

  it("offers no update when current or offline, and none for system", async () => {
    for (const [node, latest] of [
      ["22", () => Promise.resolve({ major: "22", current: "v22.23.5", latest: "v22.23.5", update_available: false })],
      ["22", () => Promise.reject(new Error("node_latest_unavailable"))],
      ["system", () => Promise.resolve({ major: "22", current: "v22.23.4", latest: "v22.23.5", update_available: true })],
    ] as const) {
      api.mockImplementation((path: string) => {
        if (path === "api/env/toolchains") return Promise.resolve(toolchains(["22"], node, { "22": "v22.23.5" }));
        if (path.startsWith("api/env/node-latest")) return latest();
        return Promise.resolve({});
      });
      await mount();
      await act(async () => {
        await Promise.resolve();
      });
      expect(nodeBtn()).toBeNull();
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("does not take another major's install for its own, running, finished or failed", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/env/toolchains") return Promise.resolve(toolchains(["22"], "22", { "22": "v22.23.4" }));
      if (path.startsWith("api/env/node-latest"))
        return Promise.resolve({ major: "22", current: "v22.23.4", latest: "v22.23.5", update_available: true });
      return Promise.resolve({});
    });
    for (const [state, error] of [
      ["installing", ""],
      ["done", ""],
      ["error", "download failed"],
    ]) {
      apiJSON.mockReset();
      toast.mockReset();
      apiJSON.mockResolvedValue({ state, major: "24", error });
      await mount();
      await act(async () => {
        await Promise.resolve();
      });
      await act(async () => {
        nodeBtn()!.click();
      });
      expect(toast).toHaveBeenCalledWith(expect.stringContaining("v24"));
      expect(nodeBtn()!.disabled).toBe(false);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("reports its own install failure with the installer's message", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "24") : {}),
    );
    apiJSON.mockResolvedValue({ state: "error", major: "24", error: "download failed" });
    await mount();
    await act(async () => {
      nodeBtn()!.click();
    });
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("download failed"));
  });
});
