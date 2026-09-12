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
const { kindLabel } = await import("../../lib/sessionkind.ts");
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
          <SessionCard s={s} opens={[]} beside={beside} running waitingAt={waitingAt} actions={actions} />
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

  // ADR 0078 decision 13. The kind is the coloured square, not a word repeated down the grid;
  // the model that ANSWERED is what varies and what the meta row now leads with.
  it("names the model that last answered, and does not spell out the kind", async () => {
    await render({ model: "claude-opus-5", context: { read: 1000, create: 10, fresh: 5, model: "claude-fable-5-1" } });
    const meta = host.querySelector(".ovw-meta")!;
    expect(meta.textContent).toContain("claude-fable-5-1");
    // The launch model is NOT what is shown once a turn has answered with another.
    expect(meta.textContent).not.toContain("claude-opus-5");
    expect(meta.textContent).not.toContain(kindLabel("claude"));
    // The kind is still readable — as the square's tooltip, as in the rail.
    expect(host.querySelector(".sess-kic")?.getAttribute("title")).toBe(kindLabel("claude"));
  });

  it("falls back to the launch model until the session has answered once", async () => {
    await render({ model: "claude-opus-5" });
    expect(host.querySelector(".ovw-meta")?.textContent).toContain("claude-opus-5");
  });

  // The gauge is the mirror's own ContextBar (decision 13), so a card and its chat can never
  // disagree about how full a session is.
  it("draws the mirror's context gauge, and the token trend when there is one", async () => {
    await render({
      context: { read: 120000, create: 8000, fresh: 2000, model: "claude-opus-5" },
      tokenSpends: [1200, 800, 4300],
    });
    expect(host.querySelector(".ovw-ctx .mirror-ctxbar")).not.toBeNull();
    // Segments sized against the window, exactly as the chat sizes them.
    expect(host.querySelector<HTMLElement>(".ovw-ctx .cb-read")?.style.width).toBe("12%");
    expect(host.querySelector(".ovw-ctx .cb-label")?.textContent).toContain("13%");
    // The trend: one polyline over the series the Agent sent.
    expect(host.querySelector(".ovw-ctx .cb-trend .spark polyline")).not.toBeNull();
  });

  it("shows no gauge before the first turn, and no trend under two points", async () => {
    await render({});
    expect(host.querySelector(".ovw-ctx")).toBeNull();
    await render({ context: { read: 0, create: 0, fresh: 0, model: "claude-opus-5" } });
    expect(host.querySelector(".ovw-ctx")).toBeNull();
    // A gauge with no trend beside it: the sparkline needs two points to mean anything.
    await render({ context: { read: 1000, create: 0, fresh: 0, model: "claude-opus-5" }, tokenSpends: [1200] });
    expect(host.querySelector(".ovw-ctx .mirror-ctxbar")).not.toBeNull();
    expect(host.querySelector(".ovw-ctx .cb-trend")).toBeNull();
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

  // ADR 0078 decision 12. The text arrives folded and capped from the Agent, so the card's
  // job is only to show it under the meta row — and to draw no row at all without one, which
  // is what keeps a card that has said nothing the same height it has always been.
  it("shows the agent's last utterance under the meta row, and nothing when there is none", async () => {
    await render({ lastSay: "転写の末尾から 1 行を作るところまで実装しました" });
    const say = host.querySelector<HTMLElement>(".ovw-say");
    expect(say?.textContent).toBe("転写の末尾から 1 行を作るところまで実装しました");
    // Under the meta row: the rows above must not move when a session speaks.
    const rows = [...card().children].map((el) => el.className);
    expect(rows.indexOf("ovw-say")).toBe(rows.indexOf("ovw-meta") + 1);
    // The full line is readable on hover even once the card ellipsizes it.
    expect(say?.getAttribute("title")).toContain("転写の末尾から 1 行を作るところまで実装しました");
    expect(say?.getAttribute("title")).toContain(t("ovw.last_say_hint"));

    await render({});
    expect(host.querySelector(".ovw-say")).toBeNull();
    await render({ lastSay: "" });
    expect(host.querySelector(".ovw-say")).toBeNull();
  });

  it("shows it on a stopped card too — there the last word is the only clue left", async () => {
    await render({ alive: false, state: "", lastSay: "テストが全部緑になりました" });
    expect(host.querySelector(".ovw-say")?.textContent).toBe("テストが全部緑になりました");
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
