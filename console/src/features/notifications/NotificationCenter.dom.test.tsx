// Opening the notification center used to acknowledge everything in it (markSeen through the
// newest seq). That was harmless while the unseen flag only drove one number on the bell; it is
// not any more, because the same flag now draws the per-session dots on the rail rows and the
// background pane tabs. A glance at the bell would have cleared the dot of every session the
// user never opened — exactly what those dots exist to survive.
//
// So: opening reads nothing, and clearing in bulk is a press of its own.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn(async (..._args: unknown[]) => ({}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

const { NotificationCenter } = await import("./NotificationCenter.tsx");
const { useNotificationStore } = await import("./store.ts");
type FleetNotification = import("./store.ts").FleetNotification;
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useToastLog } = await import("../../lib/toastLog.ts");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const unseenEvent = (): FleetNotification => ({
  seq: 7, id: "e7", kind: "answer-ready", target: { type: "session", id: "s1" },
  displayName: "s1", payload: {}, createdAt: "2026-09-22T00:00:00Z", seen: false,
});

const render = async () => {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <NotificationCenter />
      </ToastProvider>,
    );
  });
};

const click = async (sel: string) => {
  const el = host.querySelector<HTMLElement>(sel);
  if (!el) throw new Error(`not in the DOM: ${sel}`);
  await act(async () => {
    el.click();
  });
};

const seenCalls = () => apiJSON.mock.calls.filter((c) => c[0] === "api/notifications/seen");

beforeEach(() => {
  localStorage.clear();
  apiJSON.mockClear();
  useToastLog.setState({ items: [] });
  useNotificationStore.setState({ items: [unseenEvent()], maxSeq: 7, unseenCount: 1, sourceState: "ready", initialized: true });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("acknowledging notifications", () => {
  it("opening the center does not mark anything seen", async () => {
    await render();
    await click(".notification-btn");
    // The panel really did open — otherwise "nothing was acknowledged" would be vacuous.
    expect(host.querySelector(".notification-panel")).not.toBeNull();
    expect(seenCalls()).toEqual([]);
    expect(useNotificationStore.getState().items[0].seen).toBe(false);
    expect(useNotificationStore.getState().unseenCount).toBe(1);
  });

  it("mark-all-read clears the server rows and the local toast log in one press", async () => {
    useToastLog.setState({ items: [{ id: "l1", kind: "error", message: "boom", createdAt: "2026-09-22T00:00:00Z", seen: false }] });
    await render();
    await click(".notification-btn");
    await click(".notification-readall");
    expect(seenCalls()).toEqual([["api/notifications/seen", "POST", { throughSeq: 7, eventIds: undefined }]]);
    expect(useNotificationStore.getState().unseenCount).toBe(0);
    expect(useToastLog.getState().items[0].seen).toBe(true);
  });

  it("the button is there but inert once nothing is unread, instead of appearing and vanishing", async () => {
    useNotificationStore.setState({ items: [{ ...unseenEvent(), seen: true }], unseenCount: 0 });
    await render();
    await click(".notification-btn");
    const btn = host.querySelector<HTMLButtonElement>(".notification-readall");
    expect(btn?.disabled).toBe(true);
    expect(btn?.title).toBe(t("noti.mark_all_read"));
  });
});
