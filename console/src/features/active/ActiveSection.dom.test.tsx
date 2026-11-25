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
const { useActiveFold } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
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
  useSpacesStore.getState().clear();
  useActiveFold.setState({ expanded: [] });
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
    expect(JSON.parse(localStorage.getItem("af-active-expanded") || "[]")).toEqual(["p"]);

    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(cards()).toEqual(["p"]);
  });

  it("forgets an opened parent that no longer exists when the fold is next changed", async () => {
    useActiveFold.setState({ expanded: ["gone"] });
    await render([sess("p", { repo: "app" }), sess("c1", { repo: "app", originSession: "p" })]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(useActiveFold.getState().expanded).toEqual(["p"]);
  });
});
