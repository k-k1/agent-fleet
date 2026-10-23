// Inbox attachments as a reader sees them (ADR 0111 decisions 4 and 5).
//
// What is held: a session the server marked unavailable is drawn as a placeholder with
// nothing to open, a recipe shows its repository apart from the prompt and offers take-in
// only where the server says so, and a share is requested only for the sender's own session
// with the box ticked.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const takeIn = vi.fn(async () => ({ ok: true }));
vi.mock("./store.ts", () => ({ useInboxStore: { getState: () => ({ takeIn }) } }));
vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn() }));
vi.mock("../sharing/open.ts", () => ({ openSharedSession: vi.fn() }));
vi.mock("../sessions/store.ts", () => ({ useSessionsStore: (f: (s: { sessions: unknown[] }) => unknown) => f({ sessions: [] }) }));
vi.mock("../sharing/store.ts", () => ({ useSharedSessionsStore: (f: (s: { sessions: unknown[] }) => unknown) => f({ sessions: [] }) }));

import { AttachmentView, buildAttachments, INBOX_MAX_SESSION_ATTACH, SessionAttachEditor } from "./Attachments.tsx";
import type { InboxAttachment, InboxItem } from "./store.ts";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const item = { id: "item-1", role: "recipient", kind: "request" } as InboxItem;

async function render(att: InboxAttachment) {
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <AttachmentView item={item} att={att} />
      </ToastProvider>,
    );
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  takeIn.mockClear();
});

describe("AttachmentView", () => {
  it("draws an unavailable session as a placeholder with nothing to open", async () => {
    await render({ type: "session", available: false });
    expect(document.querySelector(".inbox-placeholder")).toBeTruthy();
    expect(document.querySelectorAll("button").length).toBe(0);
  });

  it("shows a recipe's repository apart from its prompt and takes it in", async () => {
    await render({
      type: "recipe",
      available: true,
      recipe: { prompt: "fix the race", kind: "claude", model: "", effort: "", repo: "github.com/k-k1/agent-fleet", canTakeIn: true, memoId: "" },
    });
    expect(document.querySelector(".inbox-recipe-repo code")?.textContent).toBe("github.com/k-k1/agent-fleet");
    expect(document.querySelector(".inbox-recipe .inbox-body")?.textContent).toBe("fix the race");
    const btn = [...document.querySelectorAll("button")].find((b) => /取り込む|Take into/.test(b.textContent || ""));
    await act(async () => btn!.click());
    expect(takeIn).toHaveBeenCalledWith("item-1");
  });

  it("offers no take-in where the server says the reader may not", async () => {
    await render({
      type: "recipe",
      available: true,
      recipe: { prompt: "x", kind: "", model: "", effort: "", repo: "", canTakeIn: false, memoId: "" },
    });
    expect(document.querySelectorAll("button").length).toBe(0);
  });
});

describe("buildAttachments", () => {
  const to = [
    { userKey: "b", email: "b@x", removed: false },
    { userKey: "c", email: "c@x", removed: false },
  ];
  const own = (name: string, share: boolean) => ({ choice: { own: true as const, name, label: name }, share });
  it("asks for a share only for the sender's own session with the box ticked, naming every recipient", () => {
    expect(buildAttachments(null, [own("s1", true)], to)).toEqual([
      { type: "session", sessionName: "s1", share: { recipients: ["b", "c"], scope: "session" } },
    ]);
    expect(buildAttachments(null, [own("s1", false)], to)).toEqual([{ type: "session", sessionName: "s1" }]);
    expect(buildAttachments(null, [{ choice: { own: false, catalogId: "cat", label: "x" }, share: true }], to)).toEqual([
      { type: "session", catalogId: "cat" },
    ]);
  });
  it("carries several sessions, each with its own share choice, in order", () => {
    expect(buildAttachments(null, [own("s1", true), { choice: { own: false, catalogId: "cat", label: "x" }, share: false }, own("s2", false)], to)).toEqual([
      { type: "session", sessionName: "s1", share: { recipients: ["b", "c"], scope: "session" } },
      { type: "session", catalogId: "cat" },
      { type: "session", sessionName: "s2" },
    ]);
  });
  it("never sends more sessions than the Control Plane accepts", () => {
    const many = Array.from({ length: INBOX_MAX_SESSION_ATTACH + 2 }, (_, i) => own("s" + i, false));
    expect(buildAttachments(null, many, to)).toHaveLength(INBOX_MAX_SESSION_ATTACH);
    expect(INBOX_MAX_SESSION_ATTACH).toBe(5);
  });
  it("drops an empty recipe", () => {
    expect(buildAttachments({ prompt: "  ", kind: "", model: "", effort: "", repo: "" }, [], to)).toEqual([]);
  });
});

describe("SessionAttachEditor", () => {
  it("lets the sender pick a second session and stops offering the picker at the cap", async () => {
    const onChange = vi.fn();
    host = document.createElement("div");
    document.body.appendChild(host);
    const attached = [{ choice: { own: true as const, name: "s1", label: "s1" }, share: false }];
    await act(async () => {
      root = createRoot(host!);
      root.render(<SessionAttachEditor value={attached} recipients={[]} onChange={onChange} />);
    });
    // Already attached: a picker for another one is still there.
    expect(host.querySelector("select")).toBeTruthy();
    const full = Array.from({ length: INBOX_MAX_SESSION_ATTACH }, (_, i) => ({ choice: { own: true as const, name: "f" + i, label: "f" + i }, share: false }));
    await act(async () => root!.render(<SessionAttachEditor value={full} recipients={[]} onChange={onChange} />));
    expect(host.querySelector("select")).toBeNull();
    const remove = [...host.querySelectorAll("button")][0];
    await act(async () => remove.click());
    expect(onChange).toHaveBeenCalledWith(full.slice(1));
  });
});
