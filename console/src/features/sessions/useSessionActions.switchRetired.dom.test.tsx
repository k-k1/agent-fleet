// The execution-method switch of a stopped Managed session that auto-run stopped: the Agent
// refuses with 409 session_retired, and the toast must carry the catalogue line for a switch
// (not the Agent's raw "session_retired: this session was retired; ..." text). A generic code
// keeps showing the server's message on purpose (the CLI's stderr tail).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toasts: unknown[] = [];
let reply: { status: number; body: unknown } = { status: 409, body: {} };

vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: unknown) => void toasts.push(m) }));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  raw: vi.fn(async () => new Response(JSON.stringify(reply.body), { status: reply.status })),
}));

const { useSessionActions } = await import("./useSessionActions.tsx");
const { setLocale, t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;
type SessionActions = import("./useSessionActions.tsx").SessionActions;

const RAW = "session_retired: this session was retired; resume it from the Console first";
const s: Session = { name: "s1", kind: "claude", alive: false, state: "stopped", driver: "managed" };

let root: Root | null = null;
let host: HTMLDivElement;
let actions: SessionActions;
const Probe = () => {
  actions = useSessionActions();
  return null;
};

const refuse = async (code: string, message: string) => {
  reply = { status: 409, body: { error: { code, message } } };
  await act(async () => {
    await actions.switchDriver(s);
  });
};

beforeEach(() => {
  toasts.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<Probe />));
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  setLocale("ja");
});

describe("switchDriver refusals", () => {
  it.each([
    ["ja", "このセッションは自動実行によって停止されています。先にセッションを開始してから、もう一度切り替えてください。"],
    ["en", "Auto-run stopped this session. Start the session first, then switch again."],
  ])("session_retired is worded from the catalogue (%s), never the raw text", async (loc, line) => {
    setLocale(loc);
    await refuse("session_retired", RAW);
    expect(toasts).toEqual([line]);
    expect(t("sess.switch_retired")).toBe(line);
    expect(JSON.stringify(toasts)).not.toContain("retired");
  });

  it("a generic code still shows the server's message", async () => {
    await refuse("runtime_failed", "cli exited: boom");
    expect(toasts).toEqual(["cli exited: boom"]);
  });
});
