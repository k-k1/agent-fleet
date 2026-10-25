// "Hand off to a member" in the session menu (#2651 / #2499): the same gate as the ordinary
// handoff row (transcript kind, folder alive, Agent reachable), so it never opens a dialog the
// CP would refuse; and it opens the offer dialog for THIS session with no proposal behind it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../gallery/open.ts", () => ({ openGallery: () => {} }));
vi.mock("./open.ts", () => ({
  openSessionFromList: () => true,
  openSessionChat: () => {},
  openSessionChatSplit: () => {},
  openSessionTerminal: () => {},
}));
vi.mock("./useSessionActions.tsx", () => ({
  useSessionActions: () => new Proxy({}, { get: () => async () => {} }),
}));
vi.mock("../sharing/HandoffOfferModal.tsx", () => ({
  HandoffOfferModal: (p: { session: string; initialPrompt: string }) => (
    <div data-testid="offer-modal">{p.session}|{p.initialPrompt}|</div>
  ),
}));

const { SessionMenu } = await import("./SessionMenu.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;
let root: Root;
let host: HTMLDivElement;

const base = (over: Partial<Session> = {}): Session =>
  ({ name: "sk7f3q9", kind: "claude", alive: true, state: "idle", title: "t", ...over }) as Session;
const render = async (s: Session, running = true) => {
  await act(async () => {
    root.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running={running} open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};
const row = () => [...document.querySelectorAll<HTMLButtonElement>(".sess-menu .ui-menu-item")].find((b) => /Hand off to a member/.test(b.textContent!));

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("SessionMenu — hand off to a member", () => {
  it("is offered on a live transcript session and opens the offer dialog for it", async () => {
    await render(base());
    const b = row();
    expect(b).toBeTruthy();
    await act(async () => b!.click());
    expect(document.querySelector("[data-testid=offer-modal]")!.textContent).toBe("sk7f3q9||");
  });

  it("is absent for a kind with no transcript (shell)", async () => {
    await render(base({ kind: "shell" }));
    expect(row()).toBeFalsy();
  });

  it("is absent while the Agent is unreachable", async () => {
    await render(base(), false);
    expect(row()).toBeFalsy();
  });

  it("is absent for a session whose folder is gone", async () => {
    await render(base({ alive: false, resumable: false }));
    expect(row()).toBeFalsy();
  });

  it("stays on a stopped session that can still be resumed", async () => {
    await render(base({ alive: false, resumable: true }));
    expect(row()).toBeTruthy();
  });
});
