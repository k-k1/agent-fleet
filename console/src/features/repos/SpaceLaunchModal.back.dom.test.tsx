// The space dialog's back key while its create is out (#5232), on the real history. Its own file:
// the shared back-close stack counts the popstates its own cleanups cause, and an earlier test's
// leftovers would swallow one of these presses.
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
vi.mock("./useRepoRail.ts", () => ({
  useRepoRailContext: () => ({
    access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} },
    launchKinds: ["claude"],
    connsSettling: false,
    claudeExpired: false,
  }),
}));

let spacesAnswer: unknown = { spaces: [] };
const apiGet = vi.fn(async (path: string): Promise<unknown> => (path === "api/spaces" ? spacesAnswer : []));
const apiPost = vi.fn(async (..._a: unknown[]): Promise<unknown> => ({}));
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: (path: string) => apiGet(path),
  apiJSON: (...a: unknown[]) => apiPost(...a),
  pasteImage: vi.fn(),
  repoPromptTemplates: vi.fn(async () => ({ groups: [] })),
  repoSkills: vi.fn(async () => ({ skills: [] })),
  isTransientErr: () => false,
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
}));

const { SpaceLaunchModal } = await import("./SpaceLaunchModal.tsx");
const { Modal } = await import("../../ui/Modal.tsx");
const { useLaunchSeed, useReposStore } = await import("./store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useUIStateStore } = await import("../../core/store/uiState.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
const onClose = vi.fn();
const onLaunched = vi.fn();
const underClose = vi.fn();

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};
const click = async (el: Element) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
};
const go = () => document.querySelector<HTMLButtonElement>(".start-space-actions button")!;

// The dialog unmounts when it closes, as in the inbox, so the key after it reaches the layer below.
function Stacked() {
  const [shown, setShown] = useState(true);
  if (!shown) return null;
  return (
    <SpaceLaunchModal
      prompt="please look at the flaky test"
      inboxItemId="item-1"
      onClose={() => {
        onClose();
        setShown(false);
      }}
      onLaunched={onLaunched}
    />
  );
}

async function open(): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        {/* The dialog the request was opened from, underneath (the inbox). */}
        <Modal title="inbox" onClose={underClose} />
        <Stacked />
      </ToastProvider>,
    );
  });
  await settle();
}

beforeEach(() => {
  localStorage.clear();
  onClose.mockReset();
  onLaunched.mockReset();
  underClose.mockReset();
  spacesAnswer = { spaces: [] };
  useUIStateStore.setState({ state: {} as never });
  useReposStore.setState({ repos: [], loadState: "ok" });
  useSpacesStore.getState().clear();
  useSessionsStore.setState({ sessions: [] });
  apiGet.mockClear();
  apiPost.mockReset();
  apiPost.mockImplementation(async (...a: unknown[]) => (String(a[0]).endsWith("/launch") || a[0] === "api/sessions" ? { name: "space-1" } : {}));
});

afterEach(async () => {
  await act(async () => root?.unmount());
  // The unmount's own history.back() answers with a popstate a tick later; the next test must not
  // meet it halfway through its own back presses.
  await settle();
  host.remove();
  document.body.innerHTML = "";
  useLaunchSeed.getState().clear();
});

describe("space launch dialog: back key", () => {
  it("holds the back key too while the create is out, then peels this dialog before the inbox", async () => {
    let answer!: (v: unknown) => void;
    apiPost.mockImplementation(() => new Promise((r) => (answer = r)));
    await open();
    // The real history: popstate arrives a task after history.back(), even in jsdom.
    const back = () =>
      act(async () => {
        const popped = new Promise<void>((r) => window.addEventListener("popstate", () => r(), { once: true }));
        history.back();
        await popped;
      });
    await click(go());
    await back();
    // The refused press gave the dialog's guard entry back, or the next press would fall through.
    expect(history.state?.afModal).toBe(true);
    await back();
    expect(history.state?.afModal).toBe(true); // repeated presses must not reach the inbox, or the page behind it
    expect(onClose).not.toHaveBeenCalled();
    expect(underClose).not.toHaveBeenCalled();
    await act(async () => answer({ error: { code: "runtime_failed", message: "boom" } }));
    await settle();
    await back();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(underClose).not.toHaveBeenCalled();
    await back();
    expect(underClose).toHaveBeenCalledTimes(1);
  });

});
