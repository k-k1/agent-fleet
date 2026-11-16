// The wiring of a carried plan's review launch: StartHost runs the seed's onLaunched only
// after the session was created, and that discards the carried card (or says it could not).
// StartHost, useStartWork and the API client are real; only fetch is stubbed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { LaunchOpts } from "./LaunchModal.tsx";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
vi.mock("./useRepoRail.ts", () => ({ useRepoRailContext: () => ({ access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} }, launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("./StartModal.tsx", () => ({ StartModal: () => null }));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
vi.mock("./LaunchModal.tsx", () => ({
  LaunchModal: (p: { onLaunch: (o: LaunchOpts) => Promise<{ ok: boolean }>; onClose: () => void }) => (
    <>
      <button
        type="button"
        data-testid="launch"
        onClick={() =>
          void p
            .onLaunch({
              kind: "claude", driver: "", model: "", effort: "", startMode: "normal", prompt: "review", title: "Review",
              images: [], subdir: "", base: "main", newBranch: "", worktree: false,
            } as LaunchOpts)
            .catch(() => undefined)
        }
      >
        launch
      </button>
      <button type="button" data-testid="cancel" onClick={p.onClose}>
        cancel
      </button>
    </>
  ),
}));

const { StartHost } = await import("./StartHost.tsx");
const { useLaunchSeed, useLaunchTarget } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { discardCarriedAfterReview } = await import("../mirror/carriedPlanReview.ts");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;
const fetchMock = vi.fn();
const setCarried = vi.fn();
const toasts: string[] = [];

function routes(create: { status: number; body: unknown }, discard: { status: number; body: unknown } | "network-error") {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://console.test/").pathname;
    if ((init?.method ?? "GET") === "GET") return new Response(JSON.stringify({ offers: [] }), { status: 200 });
    const a = path.endsWith("/carried-answer") ? discard : path.endsWith("/api/sessions") ? create : null;
    if (a === "network-error") throw new TypeError("Failed to fetch");
    if (!a) throw new Error("unexpected call " + path);
    return new Response(JSON.stringify(a.body), { status: a.status, headers: { "Content-Type": "application/json" } });
  });
}
const discards = () =>
  fetchMock.mock.calls.filter(([url, init]) => init?.method === "POST" && String(url).endsWith("/carried-answer"));
const press = async (id: string) => {
  await act(async () => host.querySelector<HTMLButtonElement>(`[data-testid=${id}]`)!.click());
  for (let i = 0; i < 10; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};

beforeEach(async () => {
  fetchMock.mockReset();
  setCarried.mockReset();
  toasts.length = 0;
  vi.stubGlobal("fetch", fetchMock);
  useLaunchSeed.getState().set("review", "Review");
  useLaunchSeed.getState().setOnLaunched(discardCarriedAfterReview({ session: "planner", setCarried, toast: (m) => toasts.push(m) }));
  useLaunchTarget.getState().open({ name: "private", path: "/home/dev/repos/private", branch: "main" } as never);
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
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  useLaunchSeed.getState().clear();
  useLaunchTarget.getState().clear();
  vi.unstubAllGlobals();
});

describe("carried plan review launch", () => {
  it("discards the carried plan once the reviewer session exists", async () => {
    routes({ status: 201, body: { name: "reviewer" } }, { status: 200, body: {} });
    await press("launch");
    expect(discards()).toHaveLength(1);
    expect(JSON.parse(String(discards()[0][1].body))).toEqual({ decision: "discard" });
    expect(setCarried).toHaveBeenCalledWith(null);
  });

  it("hands the new session's name to onLaunched (what 'review again' remembers)", async () => {
    const seen: string[] = [];
    useLaunchSeed.getState().setOnLaunched((n) => seen.push(n));
    routes({ status: 201, body: { name: "reviewer" } }, { status: 200, body: {} });
    await press("launch");
    expect(seen).toEqual(["reviewer"]);
  });

  it("keeps the card when the launch fails", async () => {
    routes({ status: 502, body: { error: { code: "x", message: "boom" } } }, { status: 200, body: {} });
    await press("launch");
    expect(discards()).toHaveLength(0);
    expect(setCarried).not.toHaveBeenCalled();
  });

  it("keeps the card when the dialog is cancelled", async () => {
    routes({ status: 201, body: { name: "reviewer" } }, { status: 200, body: {} });
    await press("cancel");
    expect(discards()).toHaveLength(0);
    expect(setCarried).not.toHaveBeenCalled();
    expect(useLaunchSeed.getState().onLaunched).toBeNull();
  });

  it("says so, and keeps the card, when the discard itself fails after the launch", async () => {
    routes({ status: 201, body: { name: "reviewer" } }, "network-error");
    await press("launch");
    expect(discards()).toHaveLength(1);
    expect(setCarried).not.toHaveBeenCalled();
    expect(toasts).toEqual([t("mirror.carried_plan_discard_failed", { err: t("err.network") })]);
  });
});
