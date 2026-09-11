// Render test for the overview card (ADR 0078): the two contracts the grid is built on.
//
//   1. A card opens its session BESIDE the grid, never in its place — whatever the click.
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
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { PaneHoverProvider } = await import("../../lib/panehover.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type SessionActions = import("../sessions/useSessionActions.tsx").SessionActions;

let root: Root | null = null;
let host: HTMLDivElement;
const actions = {} as SessionActions;

const render = async (over: Partial<Session>): Promise<void> => {
  const s: Session = { name: "s1", kind: "claude", alive: true, state: "working", title: "決済の修正", ...over };
  await act(async () => {
    root!.render(
      <ToastProvider>
        <PaneHoverProvider>
          <SessionCard s={s} opens={[]} multi={false} running actions={actions} />
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

  it("opens the session beside the grid on click, Enter and middle-click", async () => {
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

  it("the Menu key on a focused card opens it too", async () => {
    await render({});
    await act(async () => {
      card().dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
    });
    expect(menuItems()).toContain(t("srow.stop"));
  });
});
