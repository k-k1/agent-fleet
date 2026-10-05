// "Menu of repository <name>" in the session context menu (issue #1557): the item leads to the
// row's own repository menu in the rail. Pinned here: when it is offered (a known working copy,
// a running workspace, a rail to open it in) and what the user is told when the row cannot be
// shown. Opening the row's menu itself is reveal.dom.test.tsx's.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toasts: unknown[] = [];
const toast = (m: unknown) => toasts.push(m);
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
let revealResult = true;
const revealed: string[] = [];
vi.mock("../repos/reveal.ts", () => ({
  openRepoMenuInRail: async (name: string) => {
    revealed.push(name);
    return revealResult;
  },
}));
let mobile = false;
vi.mock("../../lib/device.ts", async (orig) => ({
  ...(await orig<typeof import("../../lib/device.ts")>()),
  useIsMobile: () => mobile,
}));

const { SessionMenu } = await import("./SessionMenu.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { setPopoutMode } = await import("../../lib/popoutMode.ts");
const { setSetting } = await import("../../lib/settings.ts");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;
type Repo = import("../repos/store.ts").Repo;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;
const repo = (name: string, over: Partial<Repo> = {}): Repo => ({ name, path: `/r/${name}`, ...over }) as Repo;

let root: Root;
let host: HTMLDivElement;
let closed = 0;

const render = async (over: Partial<Session> = {}, running = true) => {
  const s = { name: "sk7f3q9", kind: "claude", alive: true, state: "idle", repo: "app@wip-sab", ...over } as Session;
  await act(async () => {
    root.render(<SessionMenu s={s} actions={actions} running={running} open place={() => {}} onClose={() => closed++} />);
  });
};
const item = () => document.querySelector<HTMLElement>(".sess-menu .codicon-repo")?.closest("button") ?? null;
const press = async () => {
  await act(async () => {
    item()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};

beforeEach(() => {
  toasts.length = 0;
  revealed.length = 0;
  revealResult = true;
  mobile = false;
  closed = 0;
  localStorage.clear();
  useReposStore.setState({ repos: [repo("app"), repo("app@wip-sab", { worktree: true, parent: "app" })] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  setPopoutMode(null);
  setSetting("workingSetActive", "");
  setSetting("workingSets", []);
});

describe("session menu → repository menu", () => {
  it("names the session's working copy and opens its menu in the rail", async () => {
    await render();
    expect(item()?.textContent).toContain(t("srow.repo_menu", { name: "app@wip-sab" }));
    await press();
    expect(closed).toBe(1);
    expect(revealed).toEqual(["app@wip-sab"]);
    expect(toasts).toHaveLength(0);
  });

  it("is not offered for a session outside any known working copy", async () => {
    await render({ repo: "", dir: "/home/dev" });
    expect(item()).toBeNull();
  });

  it("is not offered while the workspace is stopped", async () => {
    await render({}, false);
    expect(item()).toBeNull();
  });

  it("is not offered in a pop-out (no rail) or on a phone (the drawer is the shell's)", async () => {
    setPopoutMode("popout");
    await render();
    expect(item()).toBeNull();
    setPopoutMode(null);
    mobile = true;
    await render({ title: "re-render" });
    expect(item()).toBeNull();
  });

  it("says the working set hides the repository when that is why the row did not appear", async () => {
    setSetting("workingSets", [{ id: "w1", name: "other", repos: ["lib"], convs: [], sessions: [], schedules: [] }]);
    setSetting("workingSetActive", "w1");
    revealResult = false;
    await render();
    await press();
    expect(toasts).toEqual([t("srow.repo_menu_outside_wset", { name: "app@wip-sab" })]);
  });

  it("otherwise says the row is not shown", async () => {
    // In the active set (through its base), so the set is not the reason: the search is.
    setSetting("workingSets", [{ id: "w1", name: "mine", repos: ["app"], convs: [], sessions: [], schedules: [] }]);
    setSetting("workingSetActive", "w1");
    revealResult = false;
    await render();
    await press();
    expect(toasts).toEqual([t("srow.repo_menu_hidden", { name: "app@wip-sab" })]);
  });
});
