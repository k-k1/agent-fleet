// The assistant-conversation menu is ONE component, opened from the left rail's chat row and
// from a chat tab in a tabbed cell. This pins that the two entry points show the same items and
// run the same handlers, and that a draft / unlisted chat tab keeps the browser's own menu.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "../panes/Pane.tsx";
import { AssistantSection } from "./AssistantSection.tsx";
import { useChatStore } from "./store.ts";
import { getSettings, setSetting } from "../../lib/settings.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import type { Cell, PaneView } from "../../layout/types.ts";
import type { ConversationMeta } from "../../types/chat.ts";

const api = vi.hoisted(() => ({
  chatList: vi.fn(),
  chatDelete: vi.fn(),
  chatSetLock: vi.fn(),
  chatRename: vi.fn(),
  assistantList: vi.fn(),
}));
vi.mock("./api.ts", async (orig) => ({ ...(await orig<typeof import("./api.ts")>()), ...api }));
const copy = vi.hoisted(() => ({ copyText: vi.fn() }));
vi.mock("../../lib/clipboard.ts", () => copy);
// jsdom's MouseEvent rejects the real helper's `view: window`; the keyboard path only has to
// reach the same contextmenu handler, so dispatch the plain event.
vi.mock("../project/contextMenuKey.ts", async (orig) => ({
  ...(await orig<typeof import("../project/contextMenuKey.ts")>()),
  synthContextMenu: (el: HTMLElement) =>
    el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 })),
}));
vi.mock("./ChatView.tsx", () => ({ ChatView: () => <div className="chat-stub" /> }));
vi.mock("../terminal/TerminalView.tsx", () => ({ TerminalView: () => <div className="term-stub" /> }));

const meta = (id: string, over: Partial<ConversationMeta> = {}): ConversationMeta => ({
  id,
  slug: "a" + id,
  agent: "claude",
  title: "題名 " + id,
  created_at: 0,
  updated_at: 0,
  message_count: 2,
  ...over,
});

const chatView: PaneView = {
  id: "v-chat",
  session: null,
  content: { kind: "chat", conversationId: "c1", draftAssistantId: null },
  wrap: null,
};
const draftView: PaneView = {
  id: "v-draft",
  session: null,
  content: { kind: "chat", conversationId: null, draftAssistantId: "asst-1" },
  wrap: null,
};
const goneView: PaneView = {
  id: "v-gone",
  session: null,
  content: { kind: "chat", conversationId: "nope", draftAssistantId: null },
  wrap: null,
};
const cell: Cell = { id: "cell1", selectedViewId: "v-chat", views: [chatView, draftView, goneView] };
const noop = () => {};

describe("assistant conversation menu — rail row and chat tab", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  const show = async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    await act(async () => {
      root = createRoot(host!);
      root.render(
        <ToastProvider>
          <ConfirmProvider>
            <AssistantSection />
            <Pane cell={cell} pane={chatView} sessionMeta={null} tabbed
              onActivate={noop} onClose={noop} onSwap={noop} onDropSplit={noop} />
          </ConfirmProvider>
        </ToastProvider>,
      );
    });
    await act(async () => {});
  };
  const ctx = async (el: Element) => {
    const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 });
    await act(async () => {
      el.dispatchEvent(ev);
    });
    return ev;
  };
  const items = () => [...document.querySelectorAll<HTMLElement>("ul.ui-menu .ui-menu-item")];
  const labels = () => items().map((b) => b.textContent!.trim());
  // Text, disabled state and the icon classes (working-set check on/off, lock vs unlock).
  const shape = () =>
    items().map((b) => ({
      text: b.textContent!.trim(),
      disabled: (b as HTMLButtonElement).disabled,
      icon: b.querySelector("i,span.codicon")?.className ?? "",
    }));
  const rowButton = () => host!.querySelector<HTMLElement>(".chat-row .chat-open")!;
  const tab = (i: number) => host!.querySelectorAll<HTMLElement>(".pane-tab")[i];
  const dismiss = async () => {
    await act(async () => {
      document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    useWorkspaceStore.setState({ state: "running" });
    const convs = [meta("c1")];
    useChatStore.setState({ convs, titles: {} });
    api.chatList.mockResolvedValue({ conversations: convs });
    api.assistantList.mockResolvedValue({ assistants: [] });
    api.chatDelete.mockResolvedValue({ ok: true });
    api.chatSetLock.mockResolvedValue({ locked: true });
    api.chatRename.mockResolvedValue({});
    setSetting("workingSets", []);
    copy.copyText.mockResolvedValue(true);
  });
  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    document.body.innerHTML = "";
    useChatStore.setState({ convs: null, titles: {} });
  });

  it("shows the same items from the tab as from the rail row", async () => {
    await show();
    await ctx(rowButton());
    const rail = labels();
    expect(rail.length).toBeGreaterThanOrEqual(4);
    await dismiss();
    expect(items()).toHaveLength(0);

    const ev = await ctx(tab(0));
    expect(ev.defaultPrevented).toBe(true);
    expect(labels()).toEqual(rail);
  });

  it.each([false, true])("shows the same items, check states and disabled states with working sets (locked=%s)", async (locked) => {
    setSetting("workingSets", [
      { id: "w1", name: "含む", repos: [], convs: ["c1"], sessions: [], schedules: [] },
      { id: "w2", name: "含まない", repos: [], convs: [], sessions: [], schedules: [] },
    ]);
    api.chatList.mockResolvedValue({ conversations: [meta("c1", { locked })] });
    await show();
    await ctx(rowButton());
    const rail = shape();
    expect(rail.map((i) => i.text)).toEqual(expect.arrayContaining(["含む", "含まない"]));
    // The two sets must differ in their check state, or this would not prove the state is shared.
    const on = rail.find((i) => i.text === "含む")!.icon;
    const off = rail.find((i) => i.text === "含まない")!.icon;
    expect(on).not.toBe(off);
    expect(rail[rail.length - 1].disabled).toBe(locked); // the delete item is last
    await dismiss();

    await ctx(tab(0));
    expect(shape()).toEqual(rail);
  });

  it("toggles working-set membership of the tab's own conversation", async () => {
    setSetting("workingSets", [{ id: "w1", name: "含む", repos: [], convs: [], sessions: [], schedules: [] }]);
    await show();
    await ctx(tab(0));
    const w = items().find((b) => b.textContent!.includes("含む"))!;
    await act(async () => w.click());
    expect(getSettings().workingSets?.[0].convs).toEqual(["c1"]);
  });

  it("saves a rename from the tab for that conversation and reloads the list", async () => {
    await show();
    await ctx(tab(0));
    await act(async () => items()[1].click());
    const input = document.querySelector("input") as HTMLInputElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "新しい題");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const before = api.chatList.mock.calls.length;
    await act(async () => {
      input.form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(api.chatRename).toHaveBeenCalledWith("c1", "新しい題");
    expect(api.chatList.mock.calls.length).toBeGreaterThan(before);
  });

  it("keeps the rename dialog and its text when the list empties underneath it", async () => {
    await show();
    await ctx(tab(0));
    await act(async () => items()[1].click());
    const input = document.querySelector("input") as HTMLInputElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "入力途中");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    // A workspace stop makes the rail store an empty list.
    await act(async () => useChatStore.setState({ convs: [] }));
    expect((document.querySelector("input") as HTMLInputElement).value).toBe("入力途中");
  });

  it("opens from the menu key on the tab, like a session tab", async () => {
    await show();
    const btn = tab(0).querySelector<HTMLElement>("[role=tab]")!;
    await act(async () => {
      btn.dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true }));
    });
    expect(items().length).toBeGreaterThanOrEqual(4);
  });

  it("leaves a draft chat tab and an unlisted conversation's tab to the browser menu", async () => {
    await show();
    expect((await ctx(tab(1))).defaultPrevented).toBe(false);
    expect((await ctx(tab(2))).defaultPrevented).toBe(false);
    expect(items()).toHaveLength(0);
  });

  it("leaves the tab to the browser menu while the workspace is stopped", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    await show();
    expect((await ctx(tab(0))).defaultPrevented).toBe(false);
  });

  it("runs the same handlers from the tab: copy, lock, delete", async () => {
    await show();
    await ctx(tab(0));
    await act(async () => items()[0].click());
    expect(copy.copyText).toHaveBeenCalledWith("ac1");

    await ctx(tab(0));
    const lock = items().find((b) => b.querySelector(".codicon-lock"))!;
    await act(async () => lock.click());
    expect(api.chatSetLock).toHaveBeenCalledWith("c1", true);

    await ctx(tab(0));
    const del = items().find((b) => b.classList.contains("danger"))!;
    await act(async () => del.click());
    expect(api.chatDelete).toHaveBeenCalledWith("c1");
  });

  it("disables delete for a locked conversation, as the rail does", async () => {
    api.chatList.mockResolvedValue({ conversations: [meta("c1", { locked: true })] });
    await show();
    await ctx(tab(0));
    const del = items().find((b) => b.classList.contains("danger")) as HTMLButtonElement;
    expect(del.disabled).toBe(true);
    await act(async () => del.click());
    expect(api.chatDelete).not.toHaveBeenCalled();
  });

  it("opens the rename dialog from the tab", async () => {
    await show();
    await ctx(tab(0));
    await act(async () => items()[1].click());
    expect((document.querySelector("input") as HTMLInputElement).value).toBe("題名 c1");
  });
});
