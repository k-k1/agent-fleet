// The driver-switch menu item (tui ⇄ managed, docs/log/27 P3 §2) must never be offered for a
// kind with no Terminal (CLI) route at all — lcpp (ADR 0093 決定 2). Reusing the generic
// switchDriver() action against such a kind would round-trip to the server only to 400
// (session_driver.go rejects a "tui" target for lcpp), so the guard belongs in the menu.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { SessionMenu } = await import("./SessionMenu.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const actions = new Proxy({}, { get: () => async () => {} }) as SessionActions;

let root: Root | null = null;
let host: HTMLDivElement;

const render = async (over: Partial<Session>): Promise<void> => {
  const s: Session = { name: "sk7f3q9", kind: "claude", alive: true, state: "idle", driver: "managed", ...over };
  await act(async () => {
    root!.render(
      <ToastProvider>
        <SessionMenu s={s} actions={actions} running open place={() => {}} onClose={() => {}} />
      </ToastProvider>,
    );
  });
};

const switchItem = () =>
  document.querySelector<HTMLElement>(".sess-menu .codicon-terminal, .sess-menu .codicon-server-process")?.closest("button") ?? null;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  document.body.innerHTML = "";
});

describe("セッションメニューの「実行方式の切替」", () => {
  it("managed 対応 kind（terminalDriver 未宣言）では出る — 陰性対照", async () => {
    await render({ kind: "codex", driver: "managed" });
    expect(switchItem()).not.toBeNull();
  });

  it("lcpp（terminalDriver: false）では出ない — 端末経路が無いので切替先が無い", async () => {
    await render({ kind: "lcpp", driver: "managed" });
    expect(switchItem()).toBeNull();
  });
});
