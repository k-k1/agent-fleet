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

// The kinds the ▼ menu offers and the quick launch are the Repositories rows' own (useRepoRail /
// useQuickLaunch, tested with them); here only what the heading does with them.
const { rail, quickLaunch } = vi.hoisted(() => ({
  rail: { launchKinds: ["claude", "codex"] as string[], access: { kinds: null, refresh: async () => {} } },
  quickLaunch: vi.fn(async (..._args: unknown[]): Promise<void | "dialog"> => undefined),
}));
vi.mock("../repos/useRepoRail.ts", () => ({ useRepoRailContext: () => rail }));
vi.mock("../repos/useQuickLaunch.ts", () => ({ useQuickLaunch: () => quickLaunch }));

const { ActiveSection } = await import("./ActiveSection.tsx");
const { ACTIVE_EXPANDED_KEY, ACTIVE_EXPANDED_MAX, ACTIVE_GROUPS_FOLDED_KEY, ACTIVE_GROUPS_OPENED_KEY } = await import("./store.ts");
const { readFoldValue, setFoldScope, writeFoldValue } = await import("../../lib/foldStore.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { waitingSessions, sessionBadges } = await import("../../app/activity/badges.ts");
const { queueScope, useQueueSettingsCache } = await import("../memo/queueSettingsCache.ts");
const { readMemoQueues } = await import("../memo/queues.ts");
const { useSchedulesStore } = await import("../schedules/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useLaunchTarget, useReposStore } = await import("../repos/store.ts");
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
  useQueueSettingsCache.getState().reset();
  api.mockClear();
  setSettings({ workingSets: [], workingSetActive: "" });
  // The default state is unsettled ("…"): each test says what it needs.
  useWorkspaceStore.setState({ state: "running" });
  // The names are the Sessions view's to ask for; this view only reads what is there.
  useSpacesStore.setState({ spaces: LIST.spaces, owner: spacesOwner(), failures: 0, failOwner: "" });
  setFoldScope({ user: "u", tenant: "t1" });
  useReposStore.setState({ repos: [{ name: "app", remote: "github.com", remotePath: "acme/app" }], loaded: true });
  useLaunchTarget.getState().clear();
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

  it("lists a session that stopped within a day and counts the older ones in a line at the group's foot", async () => {
    const ago = (h: number) => new Date(Date.now() - h * 3600 * 1000).toISOString();
    await render([
      sess("a", { repo: "app" }),
      sess("recent", { repo: "app", alive: false, stoppedAt: ago(2) }),
      sess("old", { repo: "app", alive: false, stoppedAt: ago(30) }),
      sess("older", { repo: "app", alive: false }),
    ]);
    expect(cards()).toEqual(["a", "recent"]);
    expect(host.querySelector(".act-older")?.textContent).toBe("2 more stopped sessions not shown (older than 24 hours, or no stop time)");
  });

  it("moves the window with the clock when the list itself does not change", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    try {
      vi.setSystemTime(new Date("2026-11-26T12:00:00Z"));
      const list = [
        sess("a", { repo: "app" }),
        sess("fading", { repo: "app", alive: false, stoppedAt: "2026-11-25T12:10:00Z" }),
      ];
      await render(list);
      expect(cards()).toEqual(["a", "fading"]);
      expect(host.querySelector(".act-older")).toBeNull();
      // Twenty minutes later the very same list has left the window.
      await act(async () => {
        vi.setSystemTime(new Date("2026-11-26T12:20:00Z"));
        vi.advanceTimersByTime(60_000);
      });
      expect(cards()).toEqual(["a"]);
      expect(host.querySelector(".act-older")?.textContent).toContain("1 more stopped session");
    } finally {
      vi.useRealTimers();
    }
  });

  it("offers no menu on a card while the workspace is stopped, and offers it when it runs", async () => {
    const list = [sess("a", { repo: "app" }), sess("recent", { repo: "app", alive: false, stoppedAt: new Date().toISOString() })];
    useWorkspaceStore.setState({ state: "stopped" });
    await render(list);
    expect(host.querySelector(".ovw-menu-btn")).toBeNull();
    const card = host.querySelector<HTMLElement>(".ovw-card")!;
    const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    card.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false);
    useWorkspaceStore.setState({ state: "running" });
    await flush();
    expect(host.querySelector(".ovw-menu-btn")).not.toBeNull();
  });

  it("says the workspace is stopped, ahead of any working-set wording, when nothing recent is left", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    setSettings({ workingSets: [{ id: "w1", name: "Mine", repos: [], convs: [], sessions: [], schedules: [] }], workingSetActive: "w1" });
    await render([sess("old", { repo: "app", alive: false })]);
    expect(host.querySelector(".section-empty")?.textContent).toContain("The workspace is stopped");
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

  it("keeps the other opened parents when a fold changes, and bounds the list", async () => {
    writeFoldValue(ACTIVE_EXPANDED_KEY, JSON.stringify(["elsewhere"]));
    await render([sess("p", { repo: "app" }), sess("c1", { repo: "app", originSession: "p" })]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(JSON.parse(readFoldValue(ACTIVE_EXPANDED_KEY) || "[]")).toEqual(["elsewhere", "p"]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    writeFoldValue(ACTIVE_EXPANDED_KEY, JSON.stringify(Array.from({ length: ACTIVE_EXPANDED_MAX }, (_, i) => "old" + i)));
    await act(async () => setFoldScope({ tenant: "t9" }));
    await act(async () => setFoldScope({ tenant: "t1" }));
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    const kept = JSON.parse(readFoldValue(ACTIVE_EXPANDED_KEY) || "[]") as string[];
    expect(kept).toHaveLength(ACTIVE_EXPANDED_MAX);
    expect([kept[0], kept[kept.length - 1]]).toEqual(["old1", "p"]);
  });

  it("keeps the opened parents of each tenant apart, even while the previous tenant's list is still on screen", async () => {
    const family = [sess("p", { repo: "app" }), sess("c1", { repo: "app", originSession: "p" })];
    const stored = () => JSON.parse(readFoldValue(ACTIVE_EXPANDED_KEY) || "[]") as string[];
    await render(family);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(cards()).toEqual(["p", "c1"]);
    // The other tenant already has a parent open.
    await act(async () => setFoldScope({ tenant: "t2" }));
    writeFoldValue(ACTIVE_EXPANDED_KEY, JSON.stringify(["q"]));
    await act(async () => setFoldScope({ tenant: "t1" }));
    // The switch: the scope changes at once, the session list a moment later. Until then the
    // previous tenant's rows are on screen, and a fold pressed on one must not cost this
    // tenant what it had open.
    await act(async () => setFoldScope({ tenant: "t2" }));
    expect(cards()).toEqual(["p"]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(stored()).toEqual(["q", "p"]);
    await act(async () => host.querySelector<HTMLButtonElement>(".ovw-fold")!.click());
    expect(stored()).toEqual(["q"]);
    await render([sess("q", { repo: "app" }), sess("d1", { repo: "app", originSession: "q" })]);
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

  describe("group headings", () => {
    const two = () => [sess("a", { repo: "app" }), sess("w", { repo: "app", state: "question" }), sess("sp", { space: "q3-abcdef" })];
    const headBtn = (key: string) => host.querySelector<HTMLButtonElement>(`[data-active-group="${key}"] .act-gfold`)!;
    const storedFolded = () => JSON.parse(readFoldValue(ACTIVE_GROUPS_FOLDED_KEY) || "[]") as string[];

    it("starts open, folds on the heading button, and keeps the counts on the folded heading", async () => {
      await render(two());
      const btn = host.querySelector<HTMLButtonElement>(".act-gfold")!;
      expect(btn.getAttribute("aria-expanded")).toBe("true");
      expect(cards()).toEqual(["w", "a", "sp"]);
      const key0 = btn.closest("[data-active-group]")!.getAttribute("data-active-group")!;
      await act(async () => btn.click());
      expect(host.querySelector<HTMLButtonElement>(".act-gfold")!.getAttribute("aria-expanded")).toBe("false");
      // The folded group's cards are out of the way, the other group is untouched.
      const visible = [...host.querySelectorAll<HTMLElement>(".ovw-card")].map((e) => (e.getAttribute("title") || "").split("ID: ")[1]);
      expect(visible).toEqual(["sp"]);
      const head = host.querySelector(`[data-active-group="${key0}"] .act-gtitle`)!;
      expect(head.textContent).toContain("2 running / 2 total");
      expect(head.querySelector(".act-gwait")?.textContent).toBe("1");
      // The button's own name says what the bare number is.
      expect(head.querySelector(".act-gfold")!.getAttribute("aria-label")).toBe("app, 2 running / 2 total, 1 session in this group is waiting for your answer");
      expect(storedFolded()).toEqual([key0]);
      await act(async () => host.querySelector<HTMLButtonElement>(".act-gfold")!.click());
      expect(storedFolded()).toEqual([]);
    });

    it("counts a waiting child on the heading although its family is folded", async () => {
      await render([sess("p", { repo: "app" }), sess("c", { repo: "app", originSession: "p", state: "question" })]);
      // The family starts folded: the waiting child has no card.
      expect(cards()).toEqual(["p"]);
      await act(async () => host.querySelector<HTMLButtonElement>(".act-gfold")!.click());
      const btn = host.querySelector(".act-gfold")!;
      expect(btn.querySelector(".act-gwait")?.textContent).toBe("1");
      expect(btn.getAttribute("aria-label")).toContain("1 session in this group is waiting");
    });

    it("folds the Other group and a space too, and remembers each per tenant", async () => {
      await render([sess("home"), sess("sp", { space: "q3-abcdef" })]);
      await act(async () => headBtn("o:").click());
      await act(async () => headBtn("s:q3-abcdef").click());
      expect(storedFolded().sort()).toEqual(["o:", "s:q3-abcdef"]);
      await act(async () => setFoldScope({ tenant: "t2" }));
      expect(headBtn("o:").getAttribute("aria-expanded")).toBe("true");
      expect(storedFolded()).toEqual([]);
      await act(async () => setFoldScope({ tenant: "t1" }));
      expect(headBtn("o:").getAttribute("aria-expanded")).toBe("false");
      expect(headBtn("s:q3-abcdef").getAttribute("aria-expanded")).toBe("false");
    });

    it("a group that appears later starts open", async () => {
      await render([sess("home")]);
      await act(async () => headBtn("o:").click());
      await render([sess("home"), sess("sp", { space: "q3-abcdef" })]);
      expect(headBtn("o:").getAttribute("aria-expanded")).toBe("false");
      expect(headBtn("s:q3-abcdef").getAttribute("aria-expanded")).toBe("true");
    });
  });

  describe("repository heading launch", () => {
    const remote = { remote: "github.com", remotePath: "acme/app" };
    const base = { ...remote, name: "app-clone", path: "/home/dev/repos/app-clone", branch: "main" };
    const worktree = { ...remote, name: "app@task", path: "/home/dev/repos/app@task", branch: "task", worktree: true, parent: base.name };
    const second = { ...base, name: "second-clone", path: "/home/dev/repos/second-clone", branch: "develop" };
    const mainButton = () => host.querySelector<HTMLButtonElement>(".launch-split .launch-main")!;
    const caretButton = () => host.querySelector<HTMLButtonElement>(".launch-split .launch-caret")!;
    const cloneItems = () => [...document.querySelectorAll<HTMLButtonElement>('.act-launch-menu [role="menuitem"]')];
    const kindItems = () => [...document.querySelectorAll<HTMLButtonElement>('.launch-menu [role="menuitem"]')];
    const anyMenu = () => document.querySelectorAll(".act-launch-menu, .launch-menu").length;
    let restoreLayout: () => void;

    beforeEach(() => {
      quickLaunch.mockClear();
      quickLaunch.mockResolvedValue(undefined);
      rail.launchKinds = ["claude", "codex"];
      const layout = vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) {
        return this.parentElement;
      });
      restoreLayout = () => layout.mockRestore();
    });

    afterEach(() => restoreLayout());

    async function openCloneMenu(part: "main" | "caret") {
      useReposStore.setState({ repos: [second, worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      const button = part === "main" ? mainButton() : caretButton();
      button.focus();
      await act(async () => button.click());
      expect(button.getAttribute("aria-haspopup")).toBe("menu");
      expect(button.getAttribute("aria-expanded")).toBe("true");
      expect(document.activeElement).toBe(cloneItems()[0]);
      return button;
    }

    it("is the Repositories row's split button: same classes, play icon, Launch label and a caret", async () => {
      useReposStore.setState({ repos: [worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      expect(host.querySelectorAll(".launch-split")).toHaveLength(1);
      expect(mainButton().textContent?.trim()).toBe("Launch");
      expect(mainButton().querySelector(".codicon-play")).not.toBeNull();
      expect(caretButton().querySelector(".codicon-chevron-down")).not.toBeNull();
      // The generic Button (which grows to 44px on touch and sticks out of the heading) is not used.
      expect(host.querySelector(".act-gtitle .ui-btn")).toBeNull();
    });

    it("opens the actual base checkout from a folded group with only a worktree session", async () => {
      const unrelated = { remote: "github.com", remotePath: "other/app", name: "app", path: "/home/dev/repos/app" };
      useReposStore.setState({ repos: [worktree, unrelated, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true }), sess("sp", { space: "q3-abcdef" }), sess("home")]);
      expect(host.querySelectorAll(".launch-split")).toHaveLength(1);
      const fold = host.querySelector<HTMLButtonElement>(".act-gfold")!;
      await act(async () => fold.click());
      const button = mainButton();
      expect(button.getAttribute("aria-label")).toBe("Launch in github.com/acme/app");
      expect(button.disabled).toBe(false);
      expect(button.getAttribute("aria-haspopup")).toBe("dialog");
      expect(button.hasAttribute("aria-expanded")).toBe(false);
      await act(async () => button.click());
      expect(useLaunchTarget.getState().target).toEqual(base);
      expect(useLaunchTarget.getState().initialKind).toBe("");
      expect(fold.getAttribute("aria-expanded")).toBe("false");
      expect(cards()).toEqual(["sp", "home"]);
    });

    it("quick-launches a kind from the caret in the base checkout, in a split pane on Ctrl-click, leaving the fold alone", async () => {
      useReposStore.setState({ repos: [worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      const fold = host.querySelector<HTMLButtonElement>(".act-gfold")!;
      await act(async () => fold.click());
      expect(caretButton().getAttribute("aria-label")).toBe("Quick launch in github.com/acme/app");
      await act(async () => caretButton().click());
      expect(kindItems().map((item) => item.textContent?.trim())).toEqual(["Claude", "Codex"]);
      expect(document.activeElement).toBe(kindItems()[0]);
      await act(async () => kindItems()[1].click());
      expect(quickLaunch).toHaveBeenCalledWith(base, "codex", false);
      expect(anyMenu()).toBe(0);
      expect(document.activeElement).toBe(caretButton());
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(fold.getAttribute("aria-expanded")).toBe("false");
      await act(async () => caretButton().click());
      await act(async () => kindItems()[0].dispatchEvent(new MouseEvent("click", { bubbles: true, ctrlKey: true })));
      expect(quickLaunch).toHaveBeenLastCalledWith(base, "claude", true);
    });

    it("opens the dialog on the kind when the quick launch hands the choice back", async () => {
      quickLaunch.mockResolvedValue("dialog");
      useReposStore.setState({ repos: [worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      await act(async () => caretButton().click());
      await act(async () => kindItems()[1].click());
      expect(useLaunchTarget.getState().target).toEqual(base);
      expect(useLaunchTarget.getState().initialKind).toBe("codex");
    });

    it("asks which clone first for the main button, then opens the selected one in the dialog", async () => {
      const button = await openCloneMenu("main");
      expect(cloneItems().map((item) => item.textContent)).toEqual(["app-clone", "second-clone"]);
      expect(useLaunchTarget.getState().target).toBeNull();
      await act(async () => cloneItems()[1].click());
      expect(useLaunchTarget.getState().target).toEqual(second);
      expect(button.getAttribute("aria-expanded")).toBe("false");
      expect(document.activeElement).toBe(button);
    });

    it("asks which clone first for the caret too, then lists the kinds for that clone", async () => {
      const caret = await openCloneMenu("caret");
      expect(mainButton().getAttribute("aria-expanded")).toBe("false");
      await act(async () => cloneItems()[1].click());
      expect(cloneItems()).toHaveLength(0);
      expect(kindItems()).toHaveLength(2);
      expect(document.activeElement).toBe(kindItems()[0]);
      expect(caret.getAttribute("aria-expanded")).toBe("true");
      expect(quickLaunch).not.toHaveBeenCalled();
      await act(async () => kindItems()[0].click());
      expect(quickLaunch).toHaveBeenCalledWith(second, "claude", false);
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(document.activeElement).toBe(caret);
    });

    it.each(["main", "caret"] as const)("pressing the other part while the %s's clone menu is open switches menus", async (part) => {
      await openCloneMenu(part);
      const other = part === "main" ? caretButton() : mainButton();
      await act(async () => other.click());
      expect(other.getAttribute("aria-expanded")).toBe("true");
      expect(cloneItems()).toHaveLength(2);
    });

    it.each([
      ["main", "Escape"], ["main", "Tab"], ["main", "outside press"],
      ["caret", "Escape"], ["caret", "Tab"], ["caret", "outside press"],
    ] as const)("closes the %s's clone menu on %s without launching", async (part, action) => {
      const button = await openCloneMenu(part);
      await act(async () => {
        if (action === "outside press") {
          for (const type of ["mousedown", "mouseup", "click"]) {
            host.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, detail: 1 }));
          }
        } else {
          cloneItems()[0].dispatchEvent(new KeyboardEvent("keydown", { key: action, bubbles: true }));
        }
      });
      expect(anyMenu()).toBe(0);
      expect(button.getAttribute("aria-expanded")).toBe("false");
      expect(document.activeElement).toBe(button);
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(quickLaunch).not.toHaveBeenCalled();
    });

    it("closes the kind menu on Escape and Tab, returning focus to the caret", async () => {
      useReposStore.setState({ repos: [worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      for (const key of ["Escape", "Tab"]) {
        await act(async () => caretButton().click());
        expect(kindItems()).toHaveLength(2);
        await act(async () => kindItems()[0].dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));
        expect(anyMenu()).toBe(0);
        expect(document.activeElement).toBe(caretButton());
      }
      expect(quickLaunch).not.toHaveBeenCalled();
    });

    it.each(["one clone", "no clones", "stopped"])("restores heading focus when the open menu becomes unavailable with %s", async (change) => {
      const button = await openCloneMenu("main");
      await act(async () => {
        if (change === "stopped") useWorkspaceStore.setState({ state: "stopped" });
        else useReposStore.setState({ repos: change === "one clone" ? [worktree, base] : [worktree] });
      });
      expect(anyMenu()).toBe(0);
      expect(document.activeElement).toBe(change === "one clone" ? button : host.querySelector(".act-gfold"));
      await act(async () => {
        useReposStore.setState({ repos: [worktree, base, second] });
        useWorkspaceStore.setState({ state: "running" });
      });
      expect(button.getAttribute("aria-expanded")).toBe("false");
      expect(anyMenu()).toBe(0);
      expect(useLaunchTarget.getState().target).toBeNull();
    });

    it("launches from the parent folder for a local-only worktree", async () => {
      const local = { name: "local", path: "/home/dev/repos/local" };
      useReposStore.setState({ repos: [{ name: "local@task", path: "/home/dev/repos/local@task", worktree: true, parent: local.name }, local] });
      await render([sess("wt", { repo: "local@task", worktree: true })]);
      await act(async () => mainButton().click());
      expect(useLaunchTarget.getState().target).toEqual(local);
    });

    it("disables both parts while the workspace is stopped or a base checkout cannot be resolved", async () => {
      useWorkspaceStore.setState({ state: "stopped" });
      useReposStore.setState({ repos: [worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      for (const part of [mainButton, caretButton]) {
        expect(part().disabled).toBe(true);
        expect(part().getAttribute("aria-description")).toBe("Start the workspace to launch a session.");
        await act(async () => part().click());
      }
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(anyMenu()).toBe(0);

      await act(async () => {
        useWorkspaceStore.setState({ state: "running" });
        useReposStore.setState({ repos: [worktree, { ...base, path: undefined }] });
      });
      for (const part of [mainButton, caretButton]) {
        expect(part().disabled).toBe(true);
        expect(part().getAttribute("aria-description")).toBe("No local checkout is available for this repository.");
        await act(async () => part().click());
      }
      expect(useLaunchTarget.getState().target).toBeNull();
      expect(anyMenu()).toBe(0);
    });

    it("leaves Launch disabled when no agent can start in a directory", async () => {
      rail.launchKinds = [];
      useReposStore.setState({ repos: [worktree, base] });
      await render([sess("wt", { repo: worktree.name, worktree: true })]);
      expect(mainButton().disabled).toBe(true);
      expect(mainButton().title).toBe("No available agents");
      expect(caretButton().disabled).toBe(true);
    });
  });

  describe("unattended groups", () => {
    const headBtn = (key: string) => host.querySelector<HTMLButtonElement>(`[data-active-group="${key}"] .act-gfold`)!;
    const list = [
      sess("home"),
      sess("run", { origin: "queue", state: "question" }),
      sess("rev", { origin: "session", originSession: "run" }),
      sess("tick", { origin: "schedule", scheduleId: "sc1" }),
      sess("idle-run", { origin: "queue", alive: false }),
    ];

    it("come last, start folded with their counts, and open on the heading, remembered per tenant", async () => {
      await render(list);
      expect(heads()).toEqual(["Other sessions", "Auto-run", "Schedules"]);
      expect(cards()).toEqual(["home"]);
      expect(headBtn("u:autorun").getAttribute("aria-expanded")).toBe("false");
      expect(headBtn("u:autorun").querySelector(".ovw-gcount")?.textContent).toBe("2 running / 2 total");
      expect(headBtn("u:autorun").querySelector(".act-gwait")?.textContent).toBe("1");
      await act(async () => headBtn("u:autorun").click());
      // The run is a family: its reviewer is folded under it.
      expect(cards()).toEqual(["home", "run"]);
      expect(JSON.parse(readFoldValue(ACTIVE_GROUPS_OPENED_KEY) || "[]")).toEqual(["u:autorun"]);
      expect(JSON.parse(readFoldValue(ACTIVE_GROUPS_FOLDED_KEY) || "[]")).toEqual([]);
      await act(async () => setFoldScope({ tenant: "t2" }));
      expect(headBtn("u:autorun").getAttribute("aria-expanded")).toBe("false");
      await act(async () => setFoldScope({ tenant: "t1" }));
      expect(headBtn("u:autorun").getAttribute("aria-expanded")).toBe("true");
    });

    it("labels each queue and remembers its fold independently as metadata loads", async () => {
      const runs = [
        sess("run-a", { origin: "queue", queueCategoryId: "a" }),
        sess("review-a", { origin: "session", originSession: "run-a" }),
        sess("run-b", { origin: "queue", queueCategoryId: "b" }),
      ];
      await render(runs);
      expect(heads()).toEqual(["Auto-run", "Auto-run"]);
      expect(headBtn("u:autorun:a").getAttribute("aria-expanded")).toBe("false");
      expect(headBtn("u:autorun:b").getAttribute("aria-expanded")).toBe("false");
      await act(async () => headBtn("u:autorun:a").click());
      const cache = useQueueSettingsCache.getState();
      await act(async () => cache.publish(queueScope(), cache.begin(), readMemoQueues({
        queues: [
          { categoryId: "a", category: "Reviews", repo: "app", categoryPosition: 2 },
          { categoryId: "b", category: "Builds", repo: "app", categoryPosition: 1 },
        ],
      })!));
      expect(heads()).toEqual(["Builds (app)", "Reviews (app)"]);
      expect(headBtn("u:autorun:a").getAttribute("aria-label")).toBe("Reviews (app), 2 running / 2 total");
      expect(headBtn("u:autorun:a").getAttribute("aria-expanded")).toBe("true");
      expect(headBtn("u:autorun:b").getAttribute("aria-expanded")).toBe("false");
      expect(cards()).toEqual(["run-a"]);
      expect(JSON.parse(readFoldValue(ACTIVE_GROUPS_OPENED_KEY) || "[]")).toEqual(["u:autorun:a"]);
      await act(async () => cache.applyRemoved(queueScope(), "a"));
      expect(heads()).toEqual(["Builds (app)", "Auto-run"]);
      expect(headBtn("u:autorun:a").getAttribute("aria-expanded")).toBe("true");
      expect(cards()).toEqual(["run-a"]);
    });

    it("Schedules folds, opens and is stored like Auto-run, and a shown schedule worktree child is listed once", async () => {
      useReposStore.setState({ repos: [{ name: "app", remote: "github.com", remotePath: "acme/app" }, { name: "app@wip-x", remote: "github.com", remotePath: "acme/app", worktree: true, parent: "app" }] });
      useSchedulesStore.setState({ shownIds: new Set(["sc1"]) });
      await render([
        sess("tick", { origin: "schedule", scheduleId: "sc1" }),
        sess("wt", { origin: "session", originSession: "tick", worktree: true, repo: "app@wip-x" }),
        sess("run", { origin: "queue" }),
      ]);
      expect(headBtn("u:schedule").getAttribute("aria-expanded")).toBe("false");
      expect(headBtn("u:schedule").querySelector(".ovw-gname")?.textContent).toBe("Schedules");
      // The worktree child sits under its repository, once; the folded group lists only the run itself.
      expect(cards()).toEqual(["wt"]);
      await act(async () => headBtn("u:autorun").click());
      await act(async () => headBtn("u:schedule").click());
      expect(cards().sort()).toEqual(["run", "tick", "wt"]);
      expect(JSON.parse(readFoldValue(ACTIVE_GROUPS_OPENED_KEY) || "[]")).toEqual(["u:autorun", "u:schedule"]);
    });

    it("leaves their waiting sessions out of the rail icon's count", async () => {
      await render([sess("run", { origin: "queue", state: "question" }), sess("mine", { state: "question" })]);
      const waiting = waitingSessions(useSessionsStore.getState().sessions);
      expect(waiting).toBe(1);
      expect(sessionBadges(waiting, false, true).active).toEqual({ tone: "waiting", count: 1 });
      expect(host.querySelector('[data-active-group="u:autorun"] .act-gwait')?.textContent).toBe("1");
    });

    it("appear only while an unattended run is running", async () => {
      await render([sess("home"), sess("idle-run", { origin: "queue", alive: false })]);
      expect(heads()).toEqual(["Other sessions"]);
    });

    it("do not count on the section total", async () => {
      await render(list);
      expect(host.querySelector(".ui-section-count")?.textContent).toBe("1");
    });
  });
});
