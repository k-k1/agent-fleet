// "Suggest memories now" in the session menu (ADR 0117, "Review now"): shown only where the review
// can run, and it asks the Agent to start the review for this very session.
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
const reviewSessionNow = vi.fn(async (..._a: unknown[]) => {});
vi.mock("./memoryReviewNow.ts", () => ({ reviewSessionNow: (...a: unknown[]) => reviewSessionNow(...a) }));

let reviewUnavailable = false;
vi.mock("../../lib/aiAssistResolution.ts", () => ({ useMemoryReviewUnavailable: () => reviewUnavailable }));

const { SessionMenu } = await import("./SessionMenu.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { setSetting } = await import("../../lib/settings.ts");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;
let root: Root;
let host: HTMLDivElement;

const session = (over: Partial<Session> = {}): Session =>
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
const item = () =>
  [...document.querySelectorAll<HTMLElement>(".ui-menu-item")].find((e) => e.textContent?.includes(t("srow.memory_review")));

beforeEach(() => {
  localStorage.clear();
  reviewSessionNow.mockClear();
  reviewUnavailable = false;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("the session menu's memory review item", () => {
  it("is hidden while the review is off or AF memory is off", async () => {
    setSetting("agentMemory", true);
    setSetting("memoryReview", "off");
    await render(session());
    expect(item()).toBeUndefined();
    setSetting("agentMemory", false);
    setSetting("memoryReview", "propose");
    await render(session());
    expect(item()).toBeUndefined();
  });

  it("is hidden when the Agent is unreachable or the kind has no transcript", async () => {
    setSetting("agentMemory", true);
    setSetting("memoryReview", "propose");
    await render(session(), false);
    expect(item()).toBeUndefined();
    await render(session({ kind: "shell" }));
    expect(item()).toBeUndefined();
  });

  it("starts the review of this session", async () => {
    setSetting("agentMemory", true);
    setSetting("memoryReview", "propose");
    await render(session());
    expect(item()).toBeDefined();
    await act(async () => item()!.click());
    expect(reviewSessionNow).toHaveBeenCalledTimes(1);
    expect(reviewSessionNow.mock.calls[0][0]).toBe("sk7f3q9");
  });

  it("is disabled with the reason when no backend can run the review", async () => {
    setSetting("agentMemory", true);
    setSetting("memoryReview", "propose");
    reviewUnavailable = true;
    await render(session());
    const el = item() as HTMLButtonElement;
    expect(el.disabled).toBe(true);
    expect(el.title).toBe(t("srow.memory_review_no_backend"));
    await act(async () => el.click());
    expect(reviewSessionNow).not.toHaveBeenCalled();
  });
});
