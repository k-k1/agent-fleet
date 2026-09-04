// The Java row of the toolchains tab. What matters is that selecting a version never
// leaves the member with nothing happening:
//   1. the install button appears only for a major that is not installed yet
//   2. the button POSTs /env/jdk-install, polls to completion, then refetches the list
//   3. the option itself reads as "not installed" (finding out after selecting is too late)
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

const toolchains = (installed: string[], java: string) => ({
  node: "system",
  java,
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: ["8", "17", "21"],
  java_installed: installed,
  node_options: ["system", "22"],
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

const javaBtn = () => document.querySelector<HTMLButtonElement>(".env-java-pick button");

describe("EnvTab Java row", () => {
  it("shows no button when the selected major is already installed", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["21"], "21") : {}),
    );
    await mount();
    expect(javaBtn()).toBeNull();
  });

  it("shows the install button and its note for a major that is not installed", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["21"], "17") : {}),
    );
    await mount();
    expect(javaBtn()).not.toBeNull();
    // The option itself has to read as "not installed".
    const opts = Array.from(document.querySelectorAll<HTMLOptionElement>(".env-java-pick option"));
    const absent = opts.find((o) => o.value === "17")!;
    const present = opts.find((o) => o.value === "21")!;
    expect(absent.textContent).not.toBe(present.textContent);
    expect(present.textContent).toBe("Temurin 21");
  });

  it("starts the install on click, refetches the toolchains and drops the button", async () => {
    let installed = ["21"];
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(installed, "17") : {}),
    );
    // The path where done comes back immediately (already present); polling itself is the
    // same usePolling as kiro.
    apiJSON.mockImplementation(async () => {
      installed = ["17", "21"];
      return { state: "done", major: "17", java_installed: installed };
    });
    await mount();

    await act(async () => {
      javaBtn()!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/jdk-install", "POST", { major: "17" });
    await act(async () => {
      await Promise.resolve();
    });
    // After the refetch 17 is installed, so the button goes away.
    expect(javaBtn()).toBeNull();
  });

  it("disables the button while installing, so nobody can start a second download", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["21"], "17") : { state: "installing" }),
    );
    apiJSON.mockResolvedValue({ state: "installing", major: "17" });
    await mount();

    await act(async () => {
      javaBtn()!.click();
    });
    const btn = javaBtn()!;
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toBe("インストール中…");
    // The select is locked too: switching major mid-install would make it unreadable which
    // one is being installed.
    expect(document.querySelector<HTMLSelectElement>(".env-java-pick select")!.disabled).toBe(true);
  });
});
