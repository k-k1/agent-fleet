// The space dialog an inbox request opens (#5232): the Start hub's space form over the request's
// text, launched through the CP's request route with the same `space` field the hub sends. What is
// pinned is what goes out — the route, the space, the prompt — and that the request's claim and
// badge ride along, because a wrong route would start a session the request never hears of.
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
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
const { useInboxStore } = await import("../inbox/store.ts");
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
const box = () => document.querySelector<HTMLTextAreaElement>("#start-draft-text")!;
const sessionCalls = (path: string) => apiPost.mock.calls.filter((c) => c[0] === path);

async function open(): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        {/* The dialog the request was opened from, underneath (the inbox). */}
        <Modal title="inbox" onClose={underClose} />
        <SpaceLaunchModal prompt="please look at the flaky test" inboxItemId="item-1" onClose={onClose} onLaunched={onLaunched} />
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

describe("space launch for an inbox request", () => {
  it("starts the request in a new space through the request route, with its text as the prompt", async () => {
    await open();
    expect(box().value).toBe("please look at the flaky test");
    await click(go());
    expect(sessionCalls("api/sessions")).toHaveLength(0);
    const calls = sessionCalls("api/inbox/items/item-1/launch");
    expect(calls).toHaveLength(1);
    const body = calls[0][2] as Record<string, unknown>;
    expect(body).toMatchObject({ dir: "", kind: "claude", space: { name: "" }, initial_prompt: "please look at the flaky test" });
    for (const k of ["worktree", "branch", "new_branch", "subdir"]) expect(body).not.toHaveProperty(k);
    expect(onLaunched).toHaveBeenCalledWith("space-1");
    expect(onClose).toHaveBeenCalled();
  });

  it("sends an edited prompt, and an existing space by its folder alone", async () => {
    spacesAnswer = { spaces: [{ folder: "s-abc", name: "調べもの", dir: "/home/dev/spaces/s-abc", createdAt: "2026-11-01T00:00:00Z" }] };
    await open();
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), "edited text");
      box().dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(document.querySelector<HTMLButtonElement>(".start-place-chip")!);
    await click([...document.querySelectorAll<HTMLButtonElement>(".start-place-opt")].find((b) => b.textContent?.includes("調べもの"))!);
    await click(go());
    const body = sessionCalls("api/inbox/items/item-1/launch")[0][2] as Record<string, unknown>;
    expect(body).toMatchObject({ space: { folder: "s-abc" }, initial_prompt: "edited text" });
    expect(body.space).not.toHaveProperty("name");
  });

  it("names the request itself, whatever the global launch seed holds", async () => {
    // Another request's seed (a repository launch left open, say) must not decide the route.
    useLaunchSeed.getState().set("another request", "");
    useLaunchSeed.getState().setInboxItem("item-9");
    await open();
    await click(go());
    expect(sessionCalls("api/inbox/items/item-9/launch")).toHaveLength(0);
    const calls = sessionCalls("api/inbox/items/item-1/launch");
    expect(calls).toHaveLength(1);
    expect(calls[0][2]).toMatchObject({ initial_prompt: "please look at the flaky test" });
    // And it leaves the seed alone: a plain launch afterwards still goes to api/sessions.
    expect(useLaunchSeed.getState().inboxItemId).toBe("item-9");
  });

  it("keeps Escape and one launch here while the create is out, instead of closing the dialog under it", async () => {
    let answer!: (v: unknown) => void;
    apiPost.mockImplementation(() => new Promise((r) => (answer = r)));
    await open();
    await click(go());
    await click(go()); // a second press while the first is in flight
    expect(sessionCalls("api/inbox/items/item-1/launch")).toHaveLength(1);
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(underClose).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => answer({ error: { code: "runtime_failed", message: "boom" } }));
    await settle();
    // The refused create leaves the form (and its text) to try again.
    expect(box().value).toBe("please look at the flaky test");
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalled();
    expect(underClose).not.toHaveBeenCalled();
  });

  it("refreshes the request card once the session exists", async () => {
    const refresh = vi.spyOn(useInboxStore.getState(), "refresh").mockResolvedValue(undefined as never);
    await open();
    await click(go());
    expect(refresh).toHaveBeenCalled();
    refresh.mockRestore();
  });

  it("does not launch while the Agent has not said it supports spaces", async () => {
    spacesAnswer = { error: { code: "http_404" } };
    await open();
    expect(go().disabled).toBe(true);
    await click(go());
    expect(apiPost).not.toHaveBeenCalled();
  });
});
