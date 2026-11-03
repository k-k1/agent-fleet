// A hub pick while a memo is being restored (#3289) waits for the picked repo's draft to be
// set aside before the dialog opens. That wait must not reopen a hub the person already
// closed, nor save the same draft twice on a second click.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
vi.mock("./useRepoRail.ts", () => ({ useRepoRailContext: () => ({ launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
const repo = { name: "app", path: "/home/dev/repos/app", branch: "main" };
vi.mock("./StartModal.tsx", () => ({
  StartModal: (p: { onPickRepo: (r: unknown) => void; onClose: () => void }) => (
    <>
      <button type="button" data-testid="pick" onClick={() => p.onPickRepo(repo)}>
        pick
      </button>
      <button type="button" data-testid="close" onClick={p.onClose}>
        close
      </button>
    </>
  ),
}));
vi.mock("./LaunchModal.tsx", () => ({ LaunchModal: () => <div data-testid="launch-modal" /> }));
let release!: (v: { ok: true; saved?: boolean }) => void;
const prepare = vi.fn(() => new Promise((r) => (release = r)));
vi.mock("../memo/launchStash.ts", () => ({ stashStoredDraft: () => prepare() }));

const { StartHost } = await import("./StartHost.tsx");
const { useLaunchSeed, useLaunchTarget } = await import("./store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};
const press = async (id: string) => {
  await act(async () => host.querySelector<HTMLButtonElement>(`[data-testid=${id}]`)!.click());
  await settle();
};
const launchOpen = () => !!host.querySelector("[data-testid=launch-modal]");

beforeEach(async () => {
  prepare.mockClear();
  useLaunchSeed.getState().set("メモの本文");
  useLaunchSeed.getState().setMemoId("m1");
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <StartHost />
      </ToastProvider>,
    );
  });
  await act(async () => useSessionsStore.getState().openStart());
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
});

describe("hub pick while restoring a memo", () => {
  it("opens the dialog once the draft is set aside, saving it only once", async () => {
    await press("pick");
    await press("pick");
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(launchOpen()).toBe(false);
    await act(async () => release({ ok: true }));
    await settle();
    expect(launchOpen()).toBe(true);
  });

  it("does not reopen anything when the hub was closed while saving", async () => {
    await press("pick");
    await press("close");
    await act(async () => release({ ok: true }));
    await settle();
    expect(launchOpen()).toBe(false);
    expect(useLaunchTarget.getState().target).toBeNull();
  });

  // Closed, then the same memo restored again: the seed looks the same, only the hub's
  // generation tells the first, abandoned save from the live one.
  it("ignores a save from a hub that was closed even when the same memo is restored again", async () => {
    await press("pick");
    const first = release;
    await press("close");
    useLaunchSeed.getState().set("メモの本文");
    useLaunchSeed.getState().setMemoId("m1");
    await act(async () => useSessionsStore.getState().openStart());
    await act(async () => first({ ok: true }));
    await settle();
    expect(launchOpen()).toBe(false);
  });
});
