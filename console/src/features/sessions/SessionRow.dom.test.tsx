// Render test for the rail row's state chip. The rail is narrow, so every calm state collapses
// to an icon and only the states that want the user keep their text — which makes "does this
// chip show its text" a real behaviour, not styling: a handoff nobody has launched is the next
// step of the work, and as a bare icon in a list of bare icons it is not findable.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { SessionRow } = await import("./SessionRow.tsx");
const { t } = await import("../../lib/i18n/index.ts");
const { useNotificationStore } = await import("../notifications/store.ts");
type FleetNotification = import("../notifications/store.ts").FleetNotification;
type Session = import("../../types/session.ts").Session;

let root: Root | null = null;
let host: HTMLDivElement;

const render = async (over: Partial<Session>): Promise<void> => {
  const s: Session = { name: "s1", kind: "claude", alive: true, state: "idle", title: "作業", ...over };
  await act(async () => {
    root!.render(
      <ul>
        <SessionRow s={s} selected={false} opens={[]} multi={false} running readOnly />
      </ul>,
    );
  });
};

const chip = () => host.querySelector<HTMLElement>(".session-state");

const notify = (targetID: string, seen: boolean): FleetNotification => ({
  seq: 1, id: "e1", kind: "answer-ready", target: { type: "session", id: targetID },
  displayName: targetID, payload: {}, createdAt: "2026-09-22T00:00:00Z", seen,
});

const dot = () => host.querySelector<HTMLElement>(".unread-dot");

beforeEach(() => {
  localStorage.clear();
  useNotificationStore.setState({ items: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("SessionRow state chip", () => {
  it("keeps waiting-for-input to an icon", async () => {
    await render({});
    expect(chip()?.className).toContain("mini");
    expect(chip()?.textContent?.trim()).toBe("");
  });

  it("shows the short wording when a handoff is waiting to be launched", async () => {
    await render({ handoffPending: true });
    expect(chip()?.className).not.toContain("mini");
    expect(chip()?.textContent?.trim()).toBe(t("state.handoff_short"));
    // The full wording — which run state, and what is waiting — stays on the tooltip, where
    // there is room for it.
    expect(chip()?.title).toBe(t("state.idle_handoff"));
    expect(host.querySelector<HTMLElement>(".sess-btn")?.title).toContain(t("state.idle_handoff"));
  });

  it("shows it on a stopped row too", async () => {
    await render({ alive: false, handoffPending: true });
    expect(chip()?.textContent?.trim()).toBe(t("state.handoff_short"));
    expect(chip()?.title).toBe(t("state.stopped_handoff"));
  });

  // The states that were already loud must keep their own full text, not a short form they
  // never asked for.
  it("leaves a question showing its own wording", async () => {
    await render({ state: "question" });
    expect(chip()?.textContent?.trim()).toBe(t("state.question"));
  });
});

// The unread dot: a notification for this session that nobody has opened yet. It rides the kind
// icon's CORNER rather than a column of its own — a leading dot would indent the unread rows
// alone and break the icon alignment down a narrow rail — so the test pins where it sits, not
// just that it exists. It also has to survive the stopped-row dimming, which is an opacity on
// .sess-kic: a dot nested inside would fade with the icon it marks.
describe("SessionRow unread dot", () => {
  it("marks a session whose notification is still unseen", async () => {
    useNotificationStore.setState({ items: [notify("s1", false)] });
    await render({});
    expect(dot()).not.toBeNull();
    expect(dot()?.getAttribute("aria-label")).toBe(t("noti.unread_session"));
    expect(dot()?.parentElement?.className).toBe("sess-kic-wrap");
    expect(dot()?.closest(".sess-kic")).toBeNull();
  });

  it("shows nothing once the notification is seen, or when it belongs to another session", async () => {
    useNotificationStore.setState({ items: [notify("s1", true)] });
    await render({});
    expect(dot()).toBeNull();
    useNotificationStore.setState({ items: [notify("other", false)] });
    await render({});
    expect(dot()).toBeNull();
  });

  it("stays on a stopped row — a report that arrived as the session folded away is exactly the one to chase", async () => {
    useNotificationStore.setState({ items: [notify("s1", false)] });
    await render({ alive: false });
    expect(dot()).not.toBeNull();
  });
});
