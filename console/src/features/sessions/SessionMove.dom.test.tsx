// A moved or moving source on the rail (ADR 0115 decisions 11.4, 11.6 and 11.7, #2254). Its row
// says where the conversation went and opens it; a pending move's row opens the move dialog,
// which holds retry and cancel. The menu offers the move beside the handoff, and never the
// ordinary resume on a closed source: the Agent refuses it with session_moved, so the only way
// back is the dialog's "resume here anyway".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const opened: string[] = [];
vi.mock("../gallery/open.ts", () => ({ openGallery: () => {} }));
vi.mock("./open.ts", () => ({
  openSessionFromList: () => true,
  openSessionChat: () => {},
  openSessionChatSplit: () => {},
  openSessionTerminal: () => {},
  openSessionByName: (n: string) => opened.push(n),
}));

const { SessionRow } = await import("./SessionRow.tsx");
const { SessionMenu } = await import("./SessionMenu.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useSessionsStore } = await import("./store.ts");
const { useSessionUI } = await import("./ui.ts");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;
let root: Root | null = null;
let host: HTMLDivElement;

const src = (over: Partial<Session> = {}): Session =>
  ({ name: "s1", kind: "claude", alive: false, state: "", title: "作業", resumable: true, ...over }) as Session;

const renderRow = async (s: Session) => {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ul>
          <SessionRow s={s} selected={false} opens={[]} multi={false} running actions={actions} />
        </ul>
      </ToastProvider>,
    ),
  );
};
const renderMenu = async (s: Session) => {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    ),
  );
};
const labels = () => [...document.querySelectorAll<HTMLElement>(".sess-menu .ui-menu-item")].map((e) => e.textContent!.trim());

beforeEach(() => {
  opened.length = 0;
  localStorage.clear();
  useSessionsStore.setState({ sessions: [{ name: "t9", kind: "codex", title: "移動先", alive: true } as Session] } as never);
  useSessionUI.setState({ move: null });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("the row of a moved source", () => {
  it("says where the conversation went, by the target's name, and opens it", async () => {
    await renderRow(src({ movedTo: "t9" }));
    const line = host.querySelector(".sess-move-line.moved");
    expect(line?.textContent).toContain(t("srow.moved_to", { target: "移動先" }));
    expect(host.querySelector("li")?.className).toContain("sess-has-links");
    await act(async () => host.querySelector<HTMLElement>(".sess-move-link")!.click());
    expect(opened).toEqual(["t9"]);
  });

  it("opens the move dialog from a pending move's row", async () => {
    await renderRow(src({ moving: "t9" }));
    expect(host.querySelector(".sess-move-line.sess-move-pending")?.textContent).toContain(t("srow.moving_to", { target: "移動先" }));
    await act(async () => host.querySelector<HTMLElement>(".sess-move-link")!.click());
    expect(useSessionUI.getState().move?.name).toBe("s1");
    expect(opened).toEqual([]);
  });

  it("draws no move line for a session that never moved", async () => {
    await renderRow(src());
    expect(host.querySelector(".sess-move-line")).toBeNull();
  });
});

describe("the session menu", () => {
  it("offers the move right after the handoff", async () => {
    await renderMenu(src({ alive: true, state: "idle" }));
    const l = labels();
    expect(l.indexOf(t("srow.move"))).toBe(l.indexOf(t("srow.handoff")) + 1);
  });

  it("replaces the resume of a moved source with the target and resume-here-anyway", async () => {
    await renderMenu(src({ movedTo: "t9" }));
    const l = labels();
    expect(l).not.toContain(t("srow.resume"));
    expect(l).toContain(t("srow.move_open_target"));
    expect(l).toContain(t("srow.move_reopen"));
    expect(l).not.toContain(t("srow.move"));
  });

  it("offers retry or cancel for a pending move, and no resume", async () => {
    await renderMenu(src({ moving: "t9" }));
    const l = labels();
    expect(l).not.toContain(t("srow.resume"));
    expect(l).toContain(t("srow.move_pending"));
  });

  it("keeps the ordinary resume for a stopped session that never moved", async () => {
    await renderMenu(src());
    expect(labels()).toContain(t("srow.resume"));
  });
});
