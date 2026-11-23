// The Terminal view (ADR 0142 decision 10): shells by place, SSM logins by machine labelled from
// the registered hosts, the header's actions, and the reveal a count chip asks for.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const LIST = { root: "/home/dev/spaces", spaces: [{ folder: "q3-abcdef", name: "Q3 report", dir: "/home/dev/spaces/q3-abcdef" }] };
let HOSTS: unknown[] = [{ id: "h1", alias: "build box", profileId: "p", region: "ap-northeast-1", instanceId: "i-aaa", documentName: "" }];
const PROFILES = [{ id: "p", accountId: "111122223333", region: "ap-northeast-1" }];
let tenant = "";
let hostsGate: Promise<void> | null = null;
const api = vi.fn(async (url: string): Promise<unknown> => {
  if (url.startsWith("api/spaces")) return LIST;
  if (url.startsWith("api/ssm/hosts")) {
    const answer = HOSTS;
    if (hostsGate) await hostsGate;
    return answer;
  }
  if (url.startsWith("api/ssm/profiles")) return PROFILES;
  return {};
});
const sessionDelete = vi.fn(async (..._a: unknown[]) => ({ ok: true, status: 200 }));

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (url: string) => api(url),
  raw: async () => ({ ok: true, status: 200 }),
  sessionDelete: (...a: unknown[]) => sessionDelete(...a),
  errText: () => "",
  sessionSetLock: async () => ({ ok: true }),
  sessionKeepAwake: async () => ({ ok: true }),
  sessionStopAfterTurn: async () => ({ ok: true }),
  isTransientErr: (d: { error?: { status?: number } }) => (d?.error?.status ?? 0) >= 500,
  getTenant: () => tenant,
  getUser: () => "",
}));

const { TerminalsSection } = await import("./TerminalsSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useSpacesStore } = await import("../spaces/store.ts");
const { useTerminalReveal } = await import("./store.ts");
const { ViewShownContext } = await import("../../lib/viewShown.ts");
const { setSettings } = await import("../../lib/settings.ts");
const { useSettingsUI } = await import("../settings/store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 5));
  });

async function render(shown = true): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ViewShownContext.Provider value={shown}>
            <TerminalsSection />
          </ViewShownContext.Provider>
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
  sessionDelete.mockClear();
  tenant = "";
  useSettingsUI.setState({ settingsOpen: false });
  hostsGate = null;
  HOSTS = [{ id: "h1", alias: "build box", profileId: "p", region: "ap-northeast-1", instanceId: "i-aaa", documentName: "" }];
  setSettings({ workingSets: [], workingSetActive: "" });
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
    await act(async () => confirm!.click());
    // Without `stop`: a session that was resumed while the dialog was open is refused by the
    // Agent instead of being stopped and trashed.
    expect(sessionDelete.mock.calls.map(([n, o]) => [n, o]).sort()).toEqual([
      ["down1", { stop: false }],
      ["down2", { stop: false }],
    ]);
  });

  it("asks for the hosts again when the view comes back on screen, and names the machine by the new alias", async () => {
    useSessionsStore.setState({ sessions: [ssm("m1", "i-aaa")] });
    await render(false);
    // Nothing is read while the view is off screen.
    expect(api).not.toHaveBeenCalledWith("api/ssm/hosts");
    await render(true);
    expect(heads()).toEqual(["build box"]);
    // A host renamed in Settings meanwhile.
    HOSTS = [{ id: "h1", alias: "renamed", profileId: "p", region: "ap-northeast-1", instanceId: "i-aaa", documentName: "" }];
    await render(false);
    expect(heads()).toEqual(["build box"]);
    await render(true);
    expect(heads()).toEqual(["renamed"]);
  });

  it("reads the hosts again when the Settings dialog over the view closes, without leaving the view", async () => {
    useSessionsStore.setState({ sessions: [ssm("m1", "i-aaa")] });
    await render(true);
    expect(heads()).toEqual(["build box"]);
    // Rename the host in Settings, a dialog laid over this view: the view stays selected and shown.
    await act(async () => useSettingsUI.setState({ settingsOpen: true }));
    HOSTS = [{ id: "h1", alias: "renamed", profileId: "p", region: "ap-northeast-1", instanceId: "i-aaa", documentName: "" }];
    await act(async () => useSettingsUI.setState({ settingsOpen: false }));
    await flush();
    expect(heads()).toEqual(["renamed"]);
  });

  it("falls back to the instance id when a read of the hosts gets no answer, and recovers on the next one", async () => {
    useSessionsStore.setState({ sessions: [ssm("m1", "i-aaa")] });
    await render(true);
    expect(heads()).toEqual(["build box"]);
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) => {
      if (url.startsWith("api/ssm/hosts")) throw new Error("network");
      return base(url);
    });
    // Settings closes after a rename, and the read that follows is lost.
    await act(async () => useSettingsUI.setState({ settingsOpen: true }));
    await act(async () => useSettingsUI.setState({ settingsOpen: false }));
    await flush();
    expect(heads()).toEqual(["i-aaa"]);
    api.mockImplementation(base);
    HOSTS = [{ id: "h1", alias: "renamed", profileId: "p", region: "ap-northeast-1", instanceId: "i-aaa", documentName: "" }];
    await act(async () => useSettingsUI.setState({ settingsOpen: true }));
    await flush();
    expect(heads()).toEqual(["renamed"]);
  });

  it("names nothing when the profiles cannot be read, as the account of a host is in its profile", async () => {
    useSessionsStore.setState({ sessions: [ssm("m1", "i-aaa")] });
    const base = api.getMockImplementation()!;
    api.mockImplementation(async (url: string) => (url.startsWith("api/ssm/profiles") ? { error: { code: "http_500", status: 500 } } : base(url)));
    await render(true);
    expect(heads()).toEqual(["i-aaa"]);
    api.mockImplementation(base);
  });

  it("does not name this workspace's machines with an answer that came from another", async () => {
    useSessionsStore.setState({ sessions: [ssm("m1", "i-aaa")] });
    tenant = "t1";
    await render(true);
    expect(heads()).toEqual(["build box"]);
    // The tenant changes; the other workspace's answer is still on its way.
    let release!: () => void;
    hostsGate = new Promise((r) => (release = r));
    HOSTS = [];
    tenant = "t2";
    // A tenant switch replaces the session list, which is what re-renders the view.
    await act(async () => useSessionsStore.setState({ sessions: [ssm("m1", "i-aaa")] }));
    expect(heads()).toEqual(["i-aaa"]);
    release();
    await flush();
    expect(heads()).toEqual(["i-aaa"]);
  });

  it("shows a worktree's shells under a set that lists its parent, and leaves out a set's strangers", async () => {
    useReposStore.setState({ repos: [{ name: "app" }, { name: "scratch", worktree: true, parent: "app" }, { name: "other" }] });
    useSessionsStore.setState({
      sessions: [
        sess("w", { repo: "scratch", dir: "/home/dev/repos/scratch" }),
        sess("o", { repo: "other", dir: "/home/dev/repos/other" }),
      ],
    });
    setSettings({ workingSets: [{ id: "w1", name: "A", repos: ["app"], convs: [], sessions: [], schedules: [] }], workingSetActive: "w1" });
    await render();
    expect(heads()).toEqual(["app"]);
  });

  it("leaves a shell out of a set that lists only the copy of the same name, unless it was added directly", async () => {
    useReposStore.setState({ repos: [{ name: "app", path: "/home/dev/repos/app" }] });
    useSessionsStore.setState({ sessions: [sess("x", { repo: "app", dir: "/home/dev/notes/repos/app" })] });
    const set = { id: "w1", name: "A", repos: ["app"], convs: [], sessions: [] as string[], schedules: [] };
    setSettings({ workingSets: [set], workingSetActive: "w1" });
    await render();
    expect(heads()).toEqual([]);
    await act(async () => setSettings({ workingSets: [{ ...set, sessions: ["x"] }], workingSetActive: "w1" }));
    expect(heads()).toEqual(["Home"]);
  });

  it("consumes a reveal, so a view mounted again later does not jump back", async () => {
    useSessionsStore.setState({ sessions: [sess("h", { dir: "/home/dev" })] });
    act(() => useTerminalReveal.getState().reveal("shell:home"));
    await render();
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(null)));
    });
    expect(useTerminalReveal.getState().key).toBeNull();
  });
});
