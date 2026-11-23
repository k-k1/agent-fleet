// The Terminal view (ADR 0142 decision 10): shells by place, SSM logins by machine labelled from
// the registered hosts, the header's actions, and the reveal a count chip asks for.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
const HOSTS = [{ id: "h1", alias: "build box", profileId: "p", region: "ap-northeast-1", instanceId: "i-aaa", documentName: "" }];
const api = vi.fn(async (url: string): Promise<unknown> => {
  if (url.startsWith("api/spaces")) return LIST;
  if (url.startsWith("api/ssm/hosts")) return HOSTS;
  return {};
});

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (url: string) => api(url),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => "",
  getUser: () => "",
}));

const { TerminalsSection } = await import("./TerminalsSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useTerminalReveal } = await import("./store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 5));
  });

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <TerminalsSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await flush();
}

const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "shell", alive: true, createdAt: "2026-11-01T00:00:00Z", ...extra });
const ssm = (name: string, target: string, extra: Partial<Session> = {}): Session =>
  sess(name, { kind: "ssm", ssm: { target, region: "ap-northeast-1", accountId: "111122223333", profile: "ops" }, ...extra });
const heads = () => [...host.querySelectorAll(".term-group-row .space-group-name")].map((e) => e.textContent);
const subs = () => [...host.querySelectorAll(".term-group-sub")].map((e) => e.textContent);

beforeEach(() => {
  localStorage.clear();
  api.mockClear();
  useSpacesStore.getState().clear();
  useTerminalReveal.setState({ key: null, n: 0 });
  useReposStore.setState({ repos: [{ name: "app" }] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  // jsdom has no layout: the reveal scrolls its target into view.
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("Terminal view", () => {
  it("says there is nothing yet, and offers no tidy", async () => {
    useSessionsStore.setState({ sessions: [] });
    await render();
    expect(host.textContent).toContain("No shell or SSM session");
    expect(host.querySelector<HTMLButtonElement>('button[aria-label^="Tidy"]')!.disabled).toBe(true);
  });

  it("lists shells by place and SSM logins by machine, named from the registered hosts", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("h", { dir: "/home/dev" }),
        sess("r", { repo: "app", dir: "/home/dev/repos/app" }),
        sess("s", { repo: "q3-abcdef", dir: "/home/dev/spaces/q3-abcdef", space: "q3-abcdef" }),
        ssm("m1", "i-aaa"),
        ssm("m2", "i-aaa", { ssm: { target: "i-aaa", region: "ap-northeast-1", accountId: "111122223333", profile: "audit", document: "AF-Shell" } }),
        ssm("m3", "i-zzz"),
        sess("agent", { kind: "claude", repo: "app" }),
      ],
    });
    await render();
    expect(heads()).toEqual(["Home", "app", "Q3 report", "build box", "i-zzz"]);
    expect(api).toHaveBeenCalledWith("api/ssm/hosts");
    // The alias took the instance id's place, so the id moves to the line beside it; the account
    // and region tell machines with one id apart.
    expect(subs()).toContain("i-aaa · 111122223333 · ap-northeast-1");
    // Two profiles on one machine are one group; the row says which one it is.
    expect(host.querySelectorAll("[data-term-group^='ssm:']").length).toBe(2);
    const rows = [...host.querySelectorAll(".sess-l1")].map((e) => e.textContent ?? "");
    expect(rows.some((t) => t.includes("ops"))).toBe(true);
    expect(rows.some((t) => t.includes("audit · AF-Shell"))).toBe(true);
    // No agent session in here.
    expect(host.querySelectorAll(".sess-row").length).toBe(6);
  });

  it("scrolls to and focuses the group a chip asked for, on a view mounted by that very request", async () => {
    useSessionsStore.setState({ sessions: [sess("h", { dir: "/home/dev" }), sess("r", { repo: "app", dir: "/home/dev/repos/app" })] });
    // The request that shows the view comes before the view exists.
    act(() => useTerminalReveal.getState().reveal("shell:repo:app"));
    await render();
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(null)));
    });
    expect(document.activeElement?.getAttribute("data-term-group")).toBe("shell:repo:app");
    // A later request for another place moves it.
    act(() => useTerminalReveal.getState().reveal("shell:home"));
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(null)));
    });
    expect(document.activeElement?.getAttribute("data-term-group")).toBe("shell:home");
  });

  it("tidies the stopped terminals only: not a running one, not a locked one", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("up", { dir: "/home/dev" }),
        sess("down1", { dir: "/home/dev", alive: false }),
        ssm("down2", "i-aaa", { alive: false }),
        sess("kept", { dir: "/home/dev", alive: false, locked: true }),
      ],
    });
    await render();
    const tidy = host.querySelector<HTMLButtonElement>('button[aria-label^="Tidy"]')!;
    expect(tidy.disabled).toBe(false);
    await act(async () => tidy.click());
    // The confirmation names what it will delete: the two stopped, unlocked ones.
    const confirm = [...document.querySelectorAll("button")].find((b) => b.textContent === "Clean up 2");
    expect(confirm).toBeTruthy();
  });
});
