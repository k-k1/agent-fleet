// Launching from a handoff another member offered (issue #2020, ADR 0057 decision 6).
//
// The offer is taken before the session is created, so an offer the sender withdrew while the
// recipient's launch dialog was open is never launched, and the recipient is told why. A launch
// that fails gives the take back. These replace run-sheet step H-14 on #1960.
//
// StartHost, useStartWork and the API client are the real ones: only fetch is stubbed, so the
// answers go through the same parsing the Console applies to the CP's (the client stamps every
// error's `status` with the HTTP status, which is why the CP names the offer's in offerStatus).
// The dialog is a stub that presses launch.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { LaunchOpts } from "./LaunchModal.tsx";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));
vi.mock("./useRepoRail.ts", () => ({ useRepoRailContext: () => ({ access: { kinds: { get: (k: string) => ({ kind: k }), has: () => true }, noRoute: false, allOff: false, refresh: () => {} }, launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("./StartModal.tsx", () => ({ StartModal: () => null }));
vi.mock("../mirror/HandoffProposal.tsx", () => ({ markHandoffLaunched: vi.fn() }));
vi.mock("./LaunchModal.tsx", () => ({
  LaunchModal: (p: { onLaunch: (o: LaunchOpts) => Promise<{ ok: boolean }> }) => (
    <button
      type="button"
      data-testid="launch"
      onClick={() =>
        void p
          .onLaunch({
            kind: "claude", driver: (globalThis as { __launchDriver?: string }).__launchDriver ?? "", model: "", effort: "", startMode: "normal", prompt: "carry on",
            title: "Carry on", images: [], subdir: "", base: "feature/x", newBranch: "take/x", worktree: true,
          } as LaunchOpts)
          .catch(() => undefined) // a thrown launch is the dialog's to show; here only the offer matters
      }
    >
      launch
    </button>
  ),
}));

const { StartHost } = await import("./StartHost.tsx");
const { useLaunchSeed, useLaunchTarget } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;
const fetchMock = vi.fn();

type Answer = { status: number; body: unknown } | { status: number; raw: string } | "network-error";

/** Answers fetch per path suffix (POSTs only; the handoff store's GETs get empty lists). */
function routes(answers: Record<string, Answer>) {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://console.test/").pathname;
    if ((init?.method ?? "GET") === "GET") return new Response(JSON.stringify({ offers: [] }), { status: 200 });
    for (const [suffix, a] of Object.entries(answers)) {
      if (!path.endsWith(suffix)) continue;
      if (a === "network-error") throw new TypeError("Failed to fetch");
      if ("raw" in a) return new Response(a.raw, { status: a.status, headers: { "Content-Type": "text/html" } });
      return new Response(JSON.stringify(a.body), { status: a.status, headers: { "Content-Type": "application/json" } });
    }
    throw new Error("unexpected call " + path);
  });
}

/** The POSTs made to paths ending in suffix, with their parsed bodies. */
const posts = (suffix: string) =>
  fetchMock.mock.calls
    .filter(([url, init]) => (init?.method ?? "GET") === "POST" && new URL(url, "http://console.test/").pathname.endsWith(suffix))
    .map(([, init]) => JSON.parse(String(init?.body ?? "null")) as Record<string, unknown>);
const postPaths = () =>
  fetchMock.mock.calls
    .filter(([, init]) => (init?.method ?? "GET") === "POST")
    .map(([url]) => new URL(url, "http://console.test/").pathname);

async function pressLaunch() {
  await act(async () => {
    host.querySelector<HTMLButtonElement>("[data-testid=launch]")!.click();
  });
  // Let the take → create → accept/release chain settle.
  for (let i = 0; i < 10; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
}

beforeEach(async () => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  useLaunchSeed.getState().set("carry on", "Carry on", "", "", "offer-1");
  useLaunchTarget.getState().open({ name: "private", path: "/home/dev/repos/private", branch: "feature/x" } as never);
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

const decided = (offerStatus: string): Answer => ({
  status: 409,
  body: { error: { code: "handoff_already_decided", message: "the handoff was already decided", status: offerStatus, offerStatus } },
});

describe("launching from a received handoff", () => {
  it("refuses the launch of an offer withdrawn while the dialog was open, creates nothing, and says why", async () => {
    routes({ "/offer-1/take": decided("withdrawn") });
    await pressLaunch();
    expect(posts("/api/sessions")).toHaveLength(0);
    expect(posts("/accept")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("handoff.take_withdrawn"));
    // The dialog stays up (no launch happened), so the user can read why and close it.
    expect(host.querySelector("[data-testid=launch]")).not.toBeNull();
  });

  it.each([
    ["declined", "handoff.take_declined"],
    ["accepted", "handoff.take_accepted"],
    ["expired", "handoff.take_expired"],
  ] as const)("names a %s offer as such", async (status, key) => {
    routes({ "/offer-1/take": decided(status) });
    await pressLaunch();
    expect(posts("/api/sessions")).toHaveLength(0);
    expect(document.body.textContent).toContain(t(key));
  });

  it("takes the offer before the session is created, carries the take, and accepts with its token", async () => {
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-1" } },
      "/api/sessions": { status: 201, body: { name: "s-taken" } },
      "/offer-1/accept": { status: 200, body: { status: "accepted" } },
    });
    await pressLaunch();
    const order = postPaths();
    expect(order.findIndex((p) => p.endsWith("/offer-1/take"))).toBeLessThan(order.findIndex((p) => p.endsWith("/api/sessions")));
    expect(posts("/api/sessions")[0].handoff_take).toEqual({ offerId: "offer-1", token: "tok-1" });
    expect(posts("/offer-1/accept")[0]).toEqual({ sessionName: "s-taken", token: "tok-1" });
    expect(posts("/release")).toHaveLength(0);
  });

  it("gives the offer back when the launch fails, and does not accept it", async () => {
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-2" } },
      "/api/sessions": { status: 502, body: { error: { code: "worktree_failed", message: "worktree add: invalid reference" } } },
      "/offer-1/release": { status: 200, body: { status: "pending" } },
    });
    await pressLaunch();
    expect(posts("/offer-1/release")[0]).toEqual({ token: "tok-2" });
    expect(posts("/accept")).toHaveLength(0);
  });

  it("creates nothing and says why when the CP fences the create (the take was lost meanwhile)", async () => {
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-5" } },
      "/api/sessions": { status: 409, body: { error: { code: "handoff_take_lost", message: "lost" } } },
      "/offer-1/release": { status: 409, body: { error: { code: "handoff_not_taken", status: "withdrawn", offerStatus: "withdrawn" } } },
    });
    await pressLaunch();
    expect(posts("/accept")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("err.handoff_take_lost"));
  });

  // #2040: the create's outcome is unknown, so the session may exist. Giving the offer back
  // would let the sender withdraw it and start the same work.
  it.each([
    ["the request throws", "network-error" as Answer],
    ["a gateway answers with its own page", { status: 504, raw: "<html>Gateway Timeout</html>" } as Answer],
  ])("keeps the take when %s, and the next press accepts the session the lost launch created", async (_, lost) => {
    routes({ "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-3" } }, "/api/sessions": lost });
    await pressLaunch();
    expect(posts("/release")).toHaveLength(0);
    expect(posts("/accept")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("handoff.launch_unknown"));
    expect(host.querySelector("[data-testid=launch]")).not.toBeNull();

    fetchMock.mockClear();
    routes({ "/offer-1/take": { status: 200, body: { status: "accepted", sessionName: "s-lost" } } });
    await pressLaunch();
    expect(posts("/offer-1/take")[0]).toEqual({ token: "tok-3" });
    expect(posts("/api/sessions")).toHaveLength(0);
    expect(posts("/release")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("handoff.launch_recovered", { name: "s-lost" }));
  });

  it("launches under the fresh take the next press gets when nothing was created", async () => {
    routes({ "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-6" } }, "/api/sessions": "network-error" });
    await pressLaunch();
    fetchMock.mockClear();
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-7" } },
      "/api/sessions": { status: 201, body: { name: "s-again" } },
      "/offer-1/accept": { status: 200, body: { status: "accepted" } },
    });
    await pressLaunch();
    expect(posts("/offer-1/take")[0]).toEqual({ token: "tok-6" });
    expect(posts("/api/sessions")[0].handoff_take).toEqual({ offerId: "offer-1", token: "tok-7" });
    expect(posts("/offer-1/accept")[0]).toEqual({ sessionName: "s-again", token: "tok-7" });
  });

  it("keeps the take when the CP refuses to release it because the create reached the Agent", async () => {
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-8" } },
      "/api/sessions": { status: 504, body: { error: { code: "upstream_timeout", message: "gateway" } } },
      "/offer-1/release": { status: 409, body: { error: { code: "handoff_launch_unknown", message: "x", status: "pending", offerStatus: "pending" } } },
    });
    await pressLaunch();
    expect(document.body.textContent).toContain(t("handoff.launch_unknown"));
    fetchMock.mockClear();
    routes({ "/offer-1/take": { status: 200, body: { status: "accepted", sessionName: "s-proxied" } } });
    await pressLaunch();
    expect(posts("/offer-1/take")[0]).toEqual({ token: "tok-8" });
    expect(document.body.textContent).toContain(t("handoff.launch_recovered", { name: "s-proxied" }));
  });

  it("starts the driver fallback over under a new take, since a take starts one create", async () => {
    (globalThis as { __launchDriver?: string }).__launchDriver = "managed";
    let takes = 0;
    let creates = 0;
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      const path = new URL(url, "http://console.test/").pathname;
      const json = (status: number, body: unknown) =>
        new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if ((init?.method ?? "GET") === "GET") return json(200, { offers: [] });
      if (path.endsWith("/offer-1/take")) return json(200, { status: "pending", token: `tok-r${++takes}` });
      if (path.endsWith("/offer-1/release")) return json(409, { error: { code: "handoff_not_taken", status: "pending", offerStatus: "pending" } });
      if (path.endsWith("/offer-1/accept")) return json(200, { status: "accepted" });
      if (path.endsWith("/api/sessions")) {
        return ++creates === 1
          ? json(400, { error: { code: "driver_unsupported", message: "no managed" } })
          : json(201, { name: "s-tui" });
      }
      throw new Error("unexpected call " + path);
    });
    try {
      await pressLaunch();
    } finally {
      delete (globalThis as { __launchDriver?: string }).__launchDriver;
    }
    const created = posts("/api/sessions");
    expect(created.map((b) => b.handoff_take)).toEqual([
      { offerId: "offer-1", token: "tok-r1" },
      { offerId: "offer-1", token: "tok-r2" },
    ]);
    expect(created[1].driver).toBeUndefined();
    expect(posts("/offer-1/accept")[0]).toEqual({ sessionName: "s-tui", token: "tok-r2" });
  });

  it("says the outcome is still unknown when the CP cannot ask the workspace", async () => {
    routes({
      "/offer-1/take": { status: 409, body: { error: { code: "handoff_launch_unknown", message: "x", status: "pending", offerStatus: "pending" } } },
    });
    await pressLaunch();
    expect(posts("/api/sessions")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("handoff.take_unknown"));
  });

  it("launches nothing from an offer that is no longer launchable (#2058 review F5)", async () => {
    routes({
      "/offer-1/take": { status: 409, body: { error: { code: "handoff_unavailable", message: "x", offerStatus: "pending" } } },
    });
    await pressLaunch();
    expect(posts("/api/sessions")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("handoff.take_unavailable"));
  });

  it("shows a session the workspace reports after the handoff was given back (#2058)", async () => {
    routes({
      "/offer-1/take": {
        status: 409,
        body: { error: { code: "handoff_session_after_give_back", message: "x", offerStatus: "pending", sessionName: "s-late" } },
      },
    });
    await pressLaunch();
    expect(posts("/api/sessions")).toHaveLength(0);
    expect(document.body.textContent).toContain(t("handoff.take_after_give_back", { name: "s-late" }));
  });

  it("tells the user when the session started but the acceptance could not be recorded", async () => {
    routes({
      "/offer-1/take": { status: 200, body: { status: "pending", token: "tok-4" } },
      "/api/sessions": { status: 201, body: { name: "s-late" } },
      "/offer-1/accept": { status: 409, body: { error: { code: "handoff_not_taken", message: "x", status: "expired", offerStatus: "expired" } } },
    });
    await pressLaunch();
    expect(document.body.textContent).toContain(t("handoff.accept_unrecorded", { err: "expired" }));
  });

  it("does not take anything for a launch that is not from an offer", async () => {
    await act(async () => useLaunchSeed.getState().clear());
    routes({ "/api/sessions": { status: 201, body: { name: "s-plain" } } });
    await pressLaunch();
    expect(posts("/take")).toHaveLength(0);
    expect(posts("/api/sessions")).toHaveLength(1);
    expect(posts("/api/sessions")[0].handoff_take).toBeUndefined();
  });
});
