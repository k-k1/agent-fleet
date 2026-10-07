// The member's Danger zone (Recreate / Clean home). Two contracts:
//   - it offers the two buttons only where the deployment's runtime can reach the home
//     (whoami.home_wipe). Elsewhere the CP refuses both, and a button whose every press is
//     refused is worse than no button — a remembered section that lands here gets the reason.
//   - a refusal stopped nothing, so the member's panes stay; only an operation that actually
//     tore the workspace down resets them.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const recreate = vi.fn();
const cleanHome = vi.fn();
const resetToTerminal = vi.fn();
const toast = vi.fn();
let whoami: { home_wipe?: boolean } | null = null;

vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: { getState: () => ({ recreate, cleanHome }) },
}));
vi.mock("../../../core/store/tenant.ts", () => ({
  useTenantStore: (sel: (s: { whoami: typeof whoami }) => unknown) => sel({ whoami }),
}));
vi.mock("../../../layout/store.ts", () => ({ useLayoutStore: { getState: () => ({ resetToTerminal }) } }));
vi.mock("../../sessions/store.ts", () => ({ useSessionsStore: { getState: () => ({ refresh: () => Promise.resolve() }) } }));
vi.mock("../../repos/store.ts", () => ({ useReposStore: { getState: () => ({ refresh: () => Promise.resolve() }) } }));
vi.mock("../../files/store.ts", () => ({ useFilesStore: { getState: () => ({ bump: () => {} }) } }));
vi.mock("../../editor/dirtyRegistry.ts", () => ({ confirmDirtyNavigation: () => Promise.resolve(true) }));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { DangerTab } from "./DangerTab.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<DangerTab />));
}

const buttonWith = (text: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) => (b.textContent || "").trim() === text);

async function recreateAndConfirm() {
  await act(async () => buttonWith("作り直す")!.click());
  // The dialog's confirm button carries the same label as the row's button; the last one is
  // the dialog's.
  const confirms = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).filter(
    (b) => (b.textContent || "").trim() === "作り直す",
  );
  await act(async () => confirms[confirms.length - 1].click());
}

beforeEach(() => {
  recreate.mockReset();
  cleanHome.mockReset();
  resetToTerminal.mockReset();
  toast.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("DangerTab", () => {
  it("offers nothing where the runtime cannot reach the home, and says why", async () => {
    whoami = { home_wipe: false };
    await mount();
    expect(buttonWith("作り直す")).toBeUndefined();
    expect(buttonWith("掃除")).toBeUndefined();
    expect(document.body.textContent).toContain("この配備ではワークスペースの作り直しとホームの掃除を使えません");
  });

  it("treats a whoami that has not arrived as not available", async () => {
    whoami = null;
    await mount();
    expect(buttonWith("作り直す")).toBeUndefined();
  });

  it("keeps the panes when the CP refused before stopping anything", async () => {
    whoami = { home_wipe: true };
    recreate.mockResolvedValue({ message: "この配備では使えない操作です", untouched: true });
    await mount();
    await recreateAndConfirm();
    expect(recreate).toHaveBeenCalledWith(true);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("この配備では使えない操作です"));
    expect(resetToTerminal).not.toHaveBeenCalled();
  });

  it("resets the panes once the workspace was actually torn down", async () => {
    whoami = { home_wipe: true };
    recreate.mockResolvedValue(null);
    await mount();
    await recreateAndConfirm();
    expect(resetToTerminal).toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("resets the panes after a failure that came after the stop", async () => {
    whoami = { home_wipe: true };
    recreate.mockResolvedValue({ message: "remove home/repos: permission denied", untouched: false });
    await mount();
    await recreateAndConfirm();
    expect(toast).toHaveBeenCalled();
    expect(resetToTerminal).toHaveBeenCalled();
  });
});
