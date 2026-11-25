// The Active view (ADR 0145): which sessions it lists, under which heading, and what a folded
// parent says.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
const api = vi.fn(async (url: string): Promise<unknown> => (url.startsWith("api/spaces") ? LIST : {}));

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (url: string) => api(url),
  raw: async () => ({ ok: true, status: 200 }),
  sessionDelete: async () => ({ ok: true, status: 200 }),
  errText: () => "",
  sessionSetLock: async () => ({ ok: true }),
  sessionKeepAwake: async () => ({ ok: true }),
  sessionStopAfterTurn: async () => ({ ok: true }),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => "",
  getUser: () => "",
}));

const { ActiveSection } = await import("./ActiveSection.tsx");
const { ACTIVE_EXPANDED_KEY } = await import("./store.ts");
const { readFoldValue, setFoldScope, writeFoldValue } = await import("../../lib/foldStore.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useReposStore } = await import("../repos/store.ts");
const { spacesOwner, useSpacesStore } = await import("../spaces/store.ts");
const { setSettings } = await import("../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 5));
  });

async function render(sessions: Session[]): Promise<void> {
  useSessionsStore.setState({ sessions });
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ActiveSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}

const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", alive: true, state: "idle", createdAt: "2026-11-01T00:00:00Z", ...extra });
const heads = () => [...host.querySelectorAll(".act-gtitle .ovw-gname")].map((e) => e.textContent);
const cards = () => [...host.querySelectorAll<HTMLElement>(".ovw-card")].map((e) => (e.getAttribute("title") || "").split("ID: ")[1]);

beforeEach(() => {
  localStorage.clear();
  api.mockClear();
  setSettings({ workingSets: [], workingSetActive: "" });
  // The names are the Sessions view's to ask for; this view only reads what is there.
  useSpacesStore.setState({ spaces: LIST.spaces, owner: spacesOwner(), failures: 0, failOwner: "" });
  setFoldScope({ user: "u", tenant: "t1" });
  useReposStore.setState({ repos: [{ name: "app", remote: "github.com", remotePath: "acme/app" }] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("ActiveSection", () => {
  it("lists what is running under its repository, its space, or Other, and nothing that merely stopped", async () => {
    await render([
      sess("a", { repo: "app" }),
      sess("old", { repo: "app", alive: false }),
      sess("sp", { space: "q3-abcdef" }),
      sess("home"),
      sess("sh", { kind: "shell" }),
    ]);
    expect(heads()).toEqual(["app", "Q3 report", "Other sessions"]);
    expect(cards()).toEqual(["a", "sp", "home"]);
    // The heading names the project, so the card does not repeat it.
    expect(host.querySelector(".ovw-project")).toBeNull();
  });

  it("asks the server for nothing", async () => {
    useSpacesStore.getState().clear();
    await render([sess("sp", { space: "q3-abcdef" })]);
    // The folder stands in until the Sessions view's request is answered.
    expect(heads()).toEqual(["q3-abcdef"]);
    expect(api).not.toHaveBeenCalled();
  });

  it("says so when nothing is running", async () => {
    await render([sess("old", { repo: "app", alive: false })]);
    expect(host.querySelector(".ovw-card")).toBeNull();
    expect(host.querySelector(".section-empty")?.textContent).toContain("Nothing is running");
  });

  it("starts a parent folded, says a hidden child is waiting, and opens on the fold button", async () => {
    await render([
      sess("p", { repo: "app" }),
      sess("c1", { repo: "app", originSession: "p", state: "question" }),
      sess("c2", { repo: "app", originSession: "p", alive: false }),
    ]);
    expect(cards()).toEqual(["p"]);
    expect(host.querySelector(".ovw-hidden")?.textContent).toContain("2");
    expect(host.querySelector(".ovw-hidden--waiting")?.textContent).toBe("1");

    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(cards()).toEqual(["p", "c1", "c2"]);
    expect(host.querySelector(".ovw-hidden--waiting")).toBeNull();
    expect(JSON.parse(readFoldValue(ACTIVE_EXPANDED_KEY) || "[]")).toEqual(["p"]);

    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(cards()).toEqual(["p"]);
  });

  it("forgets an opened parent that no longer exists when the fold is next changed", async () => {
    writeFoldValue(ACTIVE_EXPANDED_KEY, JSON.stringify(["gone"]));
    await render([sess("p", { repo: "app" }), sess("c1", { repo: "app", originSession: "p" })]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(JSON.parse(readFoldValue(ACTIVE_EXPANDED_KEY) || "[]")).toEqual(["p"]);
  });

  it("keeps the opened parents of each tenant apart", async () => {
    const family = [sess("p", { repo: "app" }), sess("c1", { repo: "app", originSession: "p" })];
    await render(family);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(cards()).toEqual(["p", "c1"]);
    // Another tenant: its own sessions, nothing opened, and changing a fold there prunes only there.
    await act(async () => setFoldScope({ tenant: "t2" }));
    await render([sess("q", { repo: "app" }), sess("d1", { repo: "app", originSession: "q" })]);
    expect(cards()).toEqual(["q"]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(cards()).toEqual(["q", "d1"]);
    await act(async () => setFoldScope({ tenant: "t1" }));
    await render(family);
    expect(cards()).toEqual(["p", "c1"]);
  });

  it("a middle click or a key on the fold button does not open the session", async () => {
    await render([sess("p", { repo: "app" }), sess("c1", { repo: "app", originSession: "p" })]);
    const before = JSON.stringify(useLayoutStore.getState().layout);
    const fold = host.querySelector<HTMLButtonElement>(".ovw-fold")!;
    await act(async () => void fold.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 })));
    for (const key of ["Enter", " "]) await act(async () => void fold.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));
    expect(JSON.stringify(useLayoutStore.getState().layout)).toBe(before);
    // Control: the same middle click on the card itself does open it.
    await act(async () => void host.querySelector(".ovw-card")!.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 })));
    expect(JSON.stringify(useLayoutStore.getState().layout)).not.toBe(before);
  });
});
