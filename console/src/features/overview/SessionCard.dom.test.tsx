// Render test for the overview card (ADR 0078): the two contracts the grid is built on.
//
//   1. Where a card leads: beside the grid on a wide screen, in this pane on a phone
//      (`beside={false}`), and in another pane whenever Ctrl/⌘ or the wheel is used.
//   2. Right-click, ⋯ and the Menu key open the same SessionMenu as the rail row and the tab.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Session = import("../../types/session.ts").Session;
const openSessionFromList = vi.fn((_s: Session, _split: boolean, _running: boolean) => true);
vi.mock("../sessions/open.ts", () => ({
  openSessionFromList: (s: Session, split: boolean, running: boolean) => openSessionFromList(s, split, running),
  openSessionTerminal: () => {},
  openSessionChat: () => {},
}));

const { SessionCard } = await import("./SessionCard.tsx");
const { useReposStore } = await import("../repos/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { PaneHoverProvider } = await import("../../lib/panehover.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type SessionActions = import("../sessions/useSessionActions.tsx").SessionActions;

let root: Root | null = null;
let host: HTMLDivElement;
const actions = {} as SessionActions;

const render = async (over: Partial<Session>, beside = true, waitingAt = 0): Promise<void> => {
  const s: Session = { name: "s1", kind: "claude", alive: true, state: "working", title: "決済の修正", ...over };
  await act(async () => {
    root!.render(
      <ToastProvider>
        <PaneHoverProvider>
          <SessionCard s={s} opens={[]} multi={false} beside={beside} running waitingAt={waitingAt} actions={actions} />
        </PaneHoverProvider>
      </ToastProvider>,
    );
  });
};

const card = () => host.querySelector<HTMLElement>(".ovw-card")!;
const menuItems = () => [...document.querySelectorAll<HTMLElement>(".ui-menu .ui-menu-item")].map((el) => el.textContent?.trim());

beforeEach(() => {
  localStorage.clear();
  openSessionFromList.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("SessionCard", () => {
  it("shows the session the way the rail row does: kind colour, name, full state text", async () => {
    await render({});
    expect(host.querySelector(".sess-kic")?.className).toContain("kind-claude");
    expect(host.querySelector(".ovw-title")?.textContent).toBe("決済の修正");
    // A card has room, so the chip keeps its text even for the calm states.
    expect(host.querySelector(".session-state")?.textContent?.trim()).toBe(t("state.working"));
    expect(host.querySelector(".session-state")?.className).not.toContain("mini");
  });

  it("puts the state chip in the head (top-right), not in a row of its own", async () => {
    await render({});
    expect(host.querySelector(".ovw-head .session-state")).not.toBeNull();
    // Nothing else to badge on a plain card, so the badge row is not rendered at all.
    expect(host.querySelector(".ovw-row")).toBeNull();
  });

  it("shows the parent-diff chip right of the branch, with the rail's own wording", async () => {
    useReposStore.setState({
      repos: [{ name: "app@wip-x", worktree: true, parent: "app", branch: "temp/x", integration: { targetBranch: "develop", targetUnique: 2, worktreeUnique: 0, relation: "contained" } }],
    });
    await render({ repo: "app@wip-x" });
    const chip = host.querySelector(".ovw-where .repo-chip.integration");
    expect(chip?.className).toContain("contained");
    expect(chip?.textContent).toBe(t("repo.sync.contained", { n: 2 }));
    expect(chip?.getAttribute("title")).toContain("develop");
  });

  it("shows how long it has been waiting, and says nothing when no ledger saw the change", async () => {
    const since = Date.now() - 12 * 60_000;
    await render({ state: "question" }, true, since);
    expect(host.querySelector(".ovw-waited")?.textContent).toBe(t("ovw.waiting_for", { d: "12m" }));
    expect(host.querySelector(".ovw-waited")?.className).toContain("on");
    // Answered: the same instant now reads as "working since you replied".
    await render({ state: "working" }, true, since);
    expect(host.querySelector(".ovw-waited")?.textContent).toBe(t("ovw.since_wait", { d: "12m" }));
    await render({ state: "question" }, true, 0);
    expect(host.querySelector(".ovw-waited")).toBeNull();
  });

  it("opens the session beside the grid on click, Enter and middle-click when there is room beside", async () => {
    await render({});
    await act(async () => card().click());
    expect(openSessionFromList).toHaveBeenLastCalledWith(expect.objectContaining({ name: "s1" }), true, true);
    await act(async () => {
      card().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    await act(async () => {
      card().dispatchEvent(new MouseEvent("auxclick", { button: 1, bubbles: true }));
    });
    expect(openSessionFromList).toHaveBeenCalledTimes(3);
    expect(openSessionFromList.mock.calls.every((c) => c[1] === true)).toBe(true);
  });

  it("on a phone a plain tap opens in this pane, and the modifier or the wheel still opens another", async () => {
    await render({}, false);
    await act(async () => card().click());
    expect(openSessionFromList).toHaveBeenLastCalledWith(expect.objectContaining({ name: "s1" }), false, true);
    await act(async () => {
      card().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(openSessionFromList).toHaveBeenLastCalledWith(expect.objectContaining({ name: "s1" }), false, true);
    await act(async () => {
      card().dispatchEvent(new MouseEvent("click", { ctrlKey: true, bubbles: true }));
    });
    expect(openSessionFromList).toHaveBeenLastCalledWith(expect.objectContaining({ name: "s1" }), true, true);
    await act(async () => {
      card().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true }));
    });
    expect(openSessionFromList).toHaveBeenLastCalledWith(expect.objectContaining({ name: "s1" }), true, true);
    await act(async () => {
      card().dispatchEvent(new MouseEvent("auxclick", { button: 1, bubbles: true }));
    });
    expect(openSessionFromList).toHaveBeenLastCalledWith(expect.objectContaining({ name: "s1" }), true, true);
    expect(openSessionFromList).toHaveBeenCalledTimes(5);
  });

  it("does not try to open a session whose folder is gone and has no transcript", async () => {
    await render({ kind: "shell", alive: false, resumable: false });
    await act(async () => card().click());
    expect(openSessionFromList).not.toHaveBeenCalled();
    expect(card().getAttribute("aria-disabled")).toBe("true");
  });

  it("right-click opens the shared session menu with the row's items", async () => {
    await render({});
    await act(async () => {
      card().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }));
    });
    const items = menuItems();
    expect(items).toContain(t("srow.stop"));
    expect(items).toContain(t("srow.rename"));
    expect(items).toContain(t("srow.copy_id", { name: "s1" }));
    // The menu, not the card, took the right-click: nothing was opened.
    expect(openSessionFromList).not.toHaveBeenCalled();
  });

  it("the ⋯ button opens the same menu without opening the session", async () => {
    await render({});
    await act(async () => host.querySelector<HTMLElement>(".ovw-menu-btn")!.click());
    expect(menuItems()).toContain(t("srow.stop"));
    expect(openSessionFromList).not.toHaveBeenCalled();
  });

  // Reported 2026-09-12: choosing "Stop" put the session in another pane behind the
  // confirmation dialog. The menu is a child of the card, and a React click bubbles through the
  // component tree (a portal does not stop it), so every item also counted as a card click.
  it("choosing an item from the menu does not also open the session", async () => {
    const halt = vi.fn(async () => {});
    await act(async () => {
      root!.render(
        <ToastProvider>
          <PaneHoverProvider>
            <SessionCard
              s={{ name: "s1", kind: "claude", alive: true, state: "working", title: "決済の修正" }}
              opens={[]}
              multi={false}
              beside
              running
              actions={{ ...actions, halt } as SessionActions}
            />
          </PaneHoverProvider>
        </ToastProvider>,
      );
    });
    await act(async () => host.querySelector<HTMLElement>(".ovw-menu-btn")!.click());
    const stop = [...document.querySelectorAll<HTMLElement>(".ui-menu .ui-menu-item")].find((el) => el.textContent?.trim() === t("srow.stop"));
    await act(async () => stop!.click());
    expect(halt).toHaveBeenCalledTimes(1);
    expect(openSessionFromList).not.toHaveBeenCalled();
  });

  it("the Menu key on a focused card opens it too", async () => {
    await render({});
    await act(async () => {
      card().dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
    });
    expect(menuItems()).toContain(t("srow.stop"));
  });
});
