// The send step with a memo taken in from the inbox in the selection (ADR 0111 decision 3,
// docs/log/130 T-4): the member's own memos go as one edited message, each taken-in memo as a
// message of its own, and a target that would concatenate them is not offered.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const memoFlush = vi.fn(async (_s: string, ids: string[], _text: string, _keep?: boolean) => ({ sent: ids.length }));
vi.mock("./api.ts", () => ({ memoFlush: (s: string, ids: string[], text: string, keep?: boolean) => memoFlush(s, ids, text, keep) }));
vi.mock("../chat/api.ts", () => ({ assistantList: async () => ({ assistants: [] }), chatCreate: vi.fn() }));

import { SendMemoModal } from "./SendMemoModal.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import type { Memo } from "../../types/memo.ts";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

const memo = (over: Partial<Memo>): Memo => ({
  id: "m", repo: "", category: "", kind: "text", body: "note", refPath: "", position: 0, createdAt: "", sentAt: "", ...over,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function render(memos: Memo[]) {
  useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", alive: true, state: "idle" } as never] });
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <SendMemoModal memos={memos} onClose={() => {}} onSent={() => {}} />
      </ToastProvider>,
    );
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  memoFlush.mockClear();
  localStorage.clear();
});

describe("SendMemoModal with a taken-in memo", () => {
  it("sends the own memos as edited and the taken-in one on its own", async () => {
    await render([memo({ id: "own", body: "my note" }), memo({ id: "taken", body: "colleague's", origin: { itemId: "i1", sender: "a" } })]);
    const ta = document.querySelector<HTMLTextAreaElement>(".memo-send-text")!;
    expect(ta.value).toBe("my note");
    expect(ta.value).not.toContain("colleague's");
    // Only running sessions are offered: a new session or an assistant would get one text.
    expect(document.body.textContent).not.toMatch(/NEW/);
    const send = [...document.querySelectorAll("footer button")].at(-1) as HTMLButtonElement;
    await act(async () => send.click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["own"], ["taken"]]);
    expect(memoFlush.mock.calls[1][2]).toBe("");
  });

  it("leaves a selection without taken-in memos as one message", async () => {
    await render([memo({ id: "a", body: "one" }), memo({ id: "b", body: "two" })]);
    const send = [...document.querySelectorAll("footer button")].at(-1) as HTMLButtonElement;
    await act(async () => send.click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["a", "b"]]);
  });
});

describe("SendMemoModal keep option", () => {
  const sendBtn = () => [...document.querySelectorAll("footer button")].at(-1) as HTMLButtonElement;
  const keepBox = () => document.querySelector<HTMLInputElement>(".memo-send-keep input")!;

  it("is off by default and sends without keep", async () => {
    await render([memo({ id: "a" })]);
    expect(keepBox().checked).toBe(false);
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls[0][3]).toBe(false);
  });

  it("sends keep and remembers the choice for the next dialog", async () => {
    await render([memo({ id: "a" })]);
    await act(async () => keepBox().click());
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls[0][3]).toBe(true);
    act(() => root?.unmount());
    host?.remove();
    await render([memo({ id: "a" })]);
    expect(keepBox().checked).toBe(true);
  });

  it("notes that the queue still runs a kept queue message", async () => {
    await render([memo({ id: "q", state: "pending" })]);
    expect(document.body.textContent).not.toContain("キュー上の状態は変わりません");
    await act(async () => keepBox().click());
    expect(document.body.textContent).toContain("キュー上の状態は変わりません");
  });

  it("does not offer keep for a selection with a sent memo", async () => {
    localStorage.setItem("af.memo-send-keep", "true");
    await render([memo({ id: "a", sentAt: "2026-11-01T00:00:00Z" })]);
    expect(keepBox().disabled).toBe(true);
    expect(keepBox().checked).toBe(false);
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls[0][3]).toBe(false);
  });

  it("passes keep to every send of a mixed selection", async () => {
    await render([memo({ id: "own" }), memo({ id: "taken", origin: { itemId: "i1", sender: "a" } })]);
    await act(async () => keepBox().click());
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[3])).toEqual([true, true]);
  });
});
