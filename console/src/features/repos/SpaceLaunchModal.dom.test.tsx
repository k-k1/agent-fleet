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
        <SpaceLaunchModal prompt="please look at the flaky test" onClose={onClose} onLaunched={onLaunched} />
      </ToastProvider>,
    );
  });
  await settle();
}

beforeEach(() => {
  localStorage.clear();
  onClose.mockReset();
  onLaunched.mockReset();
  spacesAnswer = { spaces: [] };
  useUIStateStore.setState({ state: {} as never });
  useReposStore.setState({ repos: [], loadState: "ok" });
  useSpacesStore.getState().clear();
  useSessionsStore.setState({ sessions: [] });
  useLaunchSeed.getState().set("please look at the flaky test", "");
  useLaunchSeed.getState().setInboxItem("item-1");
  apiGet.mockClear();
  apiPost.mockReset();
  apiPost.mockImplementation(async (...a: unknown[]) => (String(a[0]).endsWith("/launch") || a[0] === "api/sessions" ? { name: "space-1" } : {}));
});

afterEach(async () => {
  await act(async () => root?.unmount());
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
