// A handoff from another member's space (#5233): there is no repository to open it in, so the
// recipient's launch is a dialog that starts in a new space of their own, and the offer is taken
// around it exactly as for a repository. StartHost, useStartWork and the API client are the
// real ones, only fetch is stubbed; the dialog is a stub that reports what it was opened with
// and presses launch with the options the real space dialog would send.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { LaunchOpts } from "./LaunchModal.tsx";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
vi.mock("./useRepoRail.ts", () => ({ useRepoRailContext: () => ({ access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} }, launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("./StartModal.tsx", () => ({ StartModal: () => <div data-testid="hub" /> }));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
vi.mock("./LaunchModal.tsx", () => ({
  LaunchModal: (p: { repo: string; newSpace?: boolean; home?: boolean; initialPrompt?: string; initialTitle?: string; onClose: () => void; onLaunch: (o: LaunchOpts) => Promise<{ ok: boolean }> }) => (
    <>
    <button type="button" data-testid="close" onClick={p.onClose}>
      close
    </button>
    <button
      type="button"
      data-testid="launch"
      data-new-space={String(!!p.newSpace)}
      data-home={String(!!p.home)}
      data-repo={p.repo}
      data-prompt={p.initialPrompt ?? ""}
      data-title={p.initialTitle ?? ""}
      onClick={() =>
        void p
          .onLaunch({
            kind: "claude", driver: "", model: "", effort: "", startMode: "normal", prompt: "carry on", title: "Carry on",
            images: [], subdir: "", base: "", newBranch: "", worktree: false, space: p.newSpace ? { name: "" } : undefined,
          } as LaunchOpts)
          .catch(() => undefined)
      }
    >
      launch
    </button>
    </>
  ),
}));

const { StartHost } = await import("./StartHost.tsx");
const { useLaunchSeed, useLaunchTarget, useSpaceHandoff } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
const fetchMock = vi.fn();

type Answer = { status: number; body: unknown } | "network-error";
function routes(answers: Record<string, Answer>) {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://console.test/").pathname;
    if ((init?.method ?? "GET") === "GET") return new Response(JSON.stringify({ offers: [] }), { status: 200 });
    for (const [suffix, a] of Object.entries(answers)) {
      if (!path.endsWith(suffix)) continue;
      if (a === "network-error") throw new TypeError("Failed to fetch");
      return new Response(JSON.stringify(a.body), { status: a.status, headers: { "Content-Type": "application/json" } });
    }
    throw new Error("unexpected call " + path);
  });
}
const posts = (suffix: string) =>
  fetchMock.mock.calls
    .filter(([url, init]) => (init?.method ?? "GET") === "POST" && new URL(url, "http://console.test/").pathname.endsWith(suffix))
    .map(([, init]) => JSON.parse(String(init?.body ?? "null")) as Record<string, unknown>);
const dialog = () => host.querySelector<HTMLButtonElement>("[data-testid=launch]");

async function mount() {
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
}

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  useLaunchSeed.getState().clear();
  useSpaceHandoff.getState().clear();
  useLaunchTarget.getState().clear();
  vi.unstubAllGlobals();
});

describe("launching from a handoff that came from a space", () => {
  it("opens a new-space dialog seeded with the text and title, neither a repository nor home", async () => {
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    await mount();
    expect(dialog()?.dataset).toMatchObject({ newSpace: "true", home: "false", repo: "", prompt: "carry on", title: "Carry on" });
  });

  it("takes the offer, creates the session in a new space and accepts with the take's token", async () => {
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-1" } },
      "/api/sessions": { status: 201, body: { name: "s-space" } },
      "/offer-1/accept": { status: 200, body: { status: "accepted" } },
    });
    await mount();
    await act(async () => dialog()!.click());
    for (let i = 0; i < 10; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
    const create = posts("/api/sessions")[0];
    expect(create.space).toEqual({ name: "" });
    expect(create.dir ?? "").toBe("");
    expect(create.handoff_take).toEqual({ offerId: "offer-1", token: "tok-1" });
    expect(posts("/offer-1/accept")[0]).toEqual({ sessionName: "s-space", token: "tok-1" });
    // Launched: the dialog and the seed are gone.
    expect(dialog()).toBeNull();
    expect(useSpaceHandoff.getState().offerId).toBe("");
  });

  async function press(id: string) {
    await act(async () => host.querySelector<HTMLButtonElement>(`[data-testid=${id}]`)!.click());
    for (let i = 0; i < 10; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
  }

  it("gives the offer back when the create is refused, and keeps the dialog and the seed", async () => {
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-2" } },
      "/api/sessions": { status: 502, body: { error: { code: "space_failed", message: "no space" } } },
      "/offer-1/release": { status: 200, body: { status: "pending" } },
    });
    await mount();
    await press("launch");
    expect(posts("/offer-1/release")[0]).toEqual({ token: "tok-2" });
    expect(posts("/accept")).toHaveLength(0);
    expect(dialog()).not.toBeNull();
    expect(useSpaceHandoff.getState().offerId).toBe("offer-1");
  });

  it("keeps the take when the create's outcome is unknown", async () => {
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    routes({ "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-3" } }, "/api/sessions": "network-error" });
    await mount();
    await press("launch");
    // The create did go out, under the take; nothing was given back or accepted.
    expect(posts("/offer-1/take")).toHaveLength(1);
    expect(posts("/api/sessions")).toHaveLength(1);
    expect(posts("/api/sessions")[0].handoff_take).toEqual({ offerId: "offer-1", token: "tok-3" });
    expect(posts("/release")).toHaveLength(0);
    expect(posts("/accept")).toHaveLength(0);
    expect(dialog()).not.toBeNull();

    // The next press asks the CP about the lost launch with that token, and creates nothing.
    fetchMock.mockClear();
    routes({ "/offer-1/take": { status: 200, body: { status: "accepted", sessionName: "s-lost" } } });
    await press("launch");
    expect(posts("/offer-1/take")[0]).toEqual({ token: "tok-3" });
    expect(posts("/api/sessions")).toHaveLength(0);
  });

  it("takes nothing when the dialog is cancelled, and leaves no seed behind", async () => {
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    routes({});
    await mount();
    await press("close");
    expect(fetchMock.mock.calls.filter(([, init]) => (init?.method ?? "GET") === "POST")).toHaveLength(0);
    expect(dialog()).toBeNull();
    expect(useSpaceHandoff.getState()).toMatchObject({ offerId: "", prompt: "" });
  });

  // A workspace that finished starting opens the hub by itself. Over a handoff's dialog it would
  // copy the offer's text into the hub's draft, from which a repository launch could send it under
  // the same offer: the dedicated launch keeps the screen instead.
  it("does not open the Start hub over the dialog, and keeps the offer with it", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    await mount();
    await act(async () => useSessionsStore.getState().openStart());
    expect(host.querySelector("[data-testid=hub]")).toBeNull();
    expect(dialog()?.dataset.newSpace).toBe("true");
    expect(useSpaceHandoff.getState().offerId).toBe("offer-1");
    // The hub opens again once the handoff is out of the way.
    await press("close");
    await act(async () => useSessionsStore.getState().openStart());
    expect(host.querySelector("[data-testid=hub]")).not.toBeNull();
  });

  // The clone-complete toast opens a repository's dialog without the hub. That dialog reads the
  // launch seed, and the space handoff must not be in it: launching there takes and accepts no offer.
  it("does not let a repository dialog opened meanwhile take the space handoff's offer", async () => {
    useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    routes({ "/api/sessions": { status: 201, body: { name: "s-repo" } } });
    await mount();
    expect(dialog()?.dataset.newSpace).toBe("true");
    await act(async () => useLaunchTarget.getState().open({ name: "private", path: "/home/dev/repos/private", branch: "main" } as never));
    expect(dialog()?.dataset).toMatchObject({ newSpace: "false", repo: "private", prompt: "" });
    await press("launch");
    expect(posts("/api/sessions")).toHaveLength(1);
    expect(posts("/api/sessions")[0]).not.toHaveProperty("handoff_take");
    expect(posts("/offer-1/take")).toHaveLength(0);
    expect(posts("/accept")).toHaveLength(0);
    expect(useSpaceHandoff.getState().offerId).toBe("offer-1");
  });

  it("still shows the dialog when the Start hub is open", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    await mount();
    await act(async () => useSessionsStore.getState().openStart());
    await act(async () => {
      useSpaceHandoff.getState().open("offer-1", "carry on", "Carry on");
    });
    expect(dialog()?.dataset.newSpace).toBe("true");
  });

  it("never opens the space dialog for a repository offer", async () => {
    useLaunchSeed.getState().set("carry on", "Carry on", "", "", "offer-1");
    useLaunchTarget.getState().open({ name: "private", path: "/home/dev/repos/private", branch: "feature/x" } as never);
    await mount();
    expect(dialog()?.dataset).toMatchObject({ newSpace: "false", repo: "private" });
  });
});
