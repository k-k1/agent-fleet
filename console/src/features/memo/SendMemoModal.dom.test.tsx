// The send step with a memo taken in from the inbox in the selection (ADR 0111 decision 3,
// docs/log/130 T-4): the member's own memos go as one edited message, each taken-in memo as a
// message of its own, and a target that would concatenate them is not offered.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// The CP's answer to a send that went: the count and the ids, as memoFlushFor writes them.
const went = (ids: string[]) => ({ sent: ids.length, sentAt: "2026-11-24T00:00:00Z", ids });
const memoFlush = vi.fn(async (_s: string, ids: string[], _text: string, _keep?: boolean): Promise<unknown> => went(ids));
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

async function render(memos: Memo[], cb: { onSent?: () => void; onProgress?: () => void; sessions?: string[] } = {}) {
  useSessionsStore.setState({
    sessions: (cb.sessions ?? ["s1"]).map((name) => ({ name, kind: "claude", alive: true, state: "idle" }) as never),
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <SendMemoModal memos={memos} onClose={() => {}} onSent={cb.onSent ?? (() => {})} onProgress={cb.onProgress} />
      </ToastProvider>,
    );
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  memoFlush.mockReset();
  memoFlush.mockImplementation(async (_s: string, ids: string[]) => went(ids));
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

// A mixed selection is sent in batches. When a later batch fails, the dialog stays open and a
// retry sends only the batches that did not go: the earlier ones would otherwise reach the
// session twice (with keep, nothing on the server records that they went).
describe("SendMemoModal retry after a failed batch", () => {
  const sendBtn = () => [...document.querySelectorAll("footer button")].at(-1) as HTMLButtonElement;
  const mixed = () => [
    memo({ id: "own", body: "my note" }),
    memo({ id: "t1", origin: { itemId: "i1", sender: "a" } }),
    memo({ id: "t2", origin: { itemId: "i2", sender: "b" } }),
  ];
  type FlushResult = { sent?: number; error?: { code: string; message?: string } };
  const failOn = (id: string, fail: () => Promise<FlushResult>) =>
    memoFlush.mockImplementation((async (_s: string, ids: string[]) => (ids.includes(id) ? fail() : went(ids))) as never);

  it("skips the batches that went when a refused one is retried", async () => {
    const onSent = vi.fn();
    const onProgress = vi.fn();
    await render(mixed(), { onSent, onProgress });
    failOn("t2", async () => ({ error: { code: "not_running", message: "x" } }));
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["own"], ["t1"], ["t2"]]);
    expect(onSent).not.toHaveBeenCalled();
    expect(onProgress).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toMatch(/3 件中 2 件は s1\b.*送信済み/);
    expect(document.body.textContent).not.toContain("届いている可能性");
    // The own text already went, so it is no longer edited.
    expect(document.querySelector<HTMLTextAreaElement>(".memo-send-text")!.readOnly).toBe(true);

    memoFlush.mockClear();
    memoFlush.mockImplementation(async (_s: string, ids: string[]) => went(ids));
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["t2"]]);
    expect(onSent).toHaveBeenCalledTimes(1);
    // The success toast counts every memo the dialog sent, across both attempts.
    expect(document.body.textContent).toMatch(/3 件を s1\b.*に送信しました/);
  });

  it("says when a batch's outcome is unknown: a lost request, flush_failed, a proxy 5xx", async () => {
    for (const fail of [
      async (): Promise<FlushResult> => {
        throw new TypeError("network");
      },
      async (): Promise<FlushResult> => ({ error: { code: "flush_failed", message: "agent unreachable" } }),
      async (): Promise<FlushResult> => ({ error: { code: "http_502", message: "bad gateway" } }),
    ]) {
      await render(mixed());
      failOn("t1", fail);
      await act(async () => sendBtn().click());
      expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["own"], ["t1"]]);
      expect(document.body.textContent).toContain("届いたかどうか分かりません");
      expect(document.body.textContent).toContain("届いている可能性");
      // A retry sends the unknown batch again (the person was told) but never the own batch.
      memoFlush.mockClear();
      memoFlush.mockImplementation(async (_s: string, ids: string[]) => went(ids));
      await act(async () => sendBtn().click());
      expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["t1"], ["t2"]]);
      act(() => root?.unmount());
      host?.remove();
      memoFlush.mockClear();
    }
  });

  it("keeps an unsent selection's keep choice for the rest after a stamped batch", async () => {
    // Without keep the own batch is stamped sent; the list refresh then shows it sent. It is
    // not sent again, so it does not take keep away from the batches that are left.
    const memos = mixed();
    await render(memos);
    failOn("t1", async () => ({ error: { code: "not_running" } }));
    await act(async () => sendBtn().click());
    const refreshed = [{ ...memos[0], sentAt: "2026-11-24T00:00:00Z" }, memos[1], memos[2]];
    await act(async () =>
      root!.render(
        <ToastProvider>
          <SendMemoModal memos={refreshed} onClose={() => {}} onSent={() => {}} />
        </ToastProvider>,
      ),
    );
    const keepBox = document.querySelector<HTMLInputElement>(".memo-send-keep input")!;
    expect(keepBox.disabled).toBe(false);
    await act(async () => keepBox.click());
    memoFlush.mockClear();
    memoFlush.mockImplementation(async (_s: string, ids: string[]) => went(ids));
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => [c[1], c[3]])).toEqual([
      [["t1"], true],
      [["t2"], true],
    ]);
  });

  it("reports a refused single send as before and sends it whole on retry", async () => {
    await render([memo({ id: "a" }), memo({ id: "b" })]);
    failOn("a", async () => ({ error: { code: "not_running" } }));
    await act(async () => sendBtn().click());
    expect(document.body.textContent).not.toContain("送信済みです");
    memoFlush.mockClear();
    memoFlush.mockImplementation(async (_s: string, ids: string[]) => went(ids));
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["a", "b"]]);
  });
});

// The plan of a send that went part way is fixed: the batches, their memos and the session. A
// list refresh, a memo removed elsewhere or another target picked cannot make a retry send a
// batch that went, or send the rest somewhere else.
describe("SendMemoModal retry keeps the first attempt's plan", () => {
  const sendBtn = () => [...document.querySelectorAll("footer button")].at(-1) as HTMLButtonElement;
  const own = memo({ id: "own", body: "my note" });
  const t1 = memo({ id: "t1", origin: { itemId: "i1", sender: "a" } });
  const t2 = memo({ id: "t2", origin: { itemId: "i2", sender: "b" } });
  type FlushResult = { sent?: number; ids?: string[]; error?: { code: string; message?: string; status?: number }; message?: string };
  const failOn = (id: string, fail: () => Promise<FlushResult>) =>
    memoFlush.mockImplementation((async (_s: string, ids: string[]) => (ids.includes(id) ? fail() : went(ids))) as never);
  const allGo = () => {
    memoFlush.mockClear();
    memoFlush.mockImplementation(async (_s: string, ids: string[]) => went(ids));
  };
  const rerender = (memos: Memo[], onSent: () => void = () => {}) =>
    act(async () =>
      root!.render(
        <ToastProvider>
          <SendMemoModal memos={memos} onClose={() => {}} onSent={onSent} />
        </ToastProvider>,
      ),
    );

  it("does not resend a batch that went when the rest left the list", async () => {
    const onSent = vi.fn();
    await render([own, t1], { onSent });
    await act(async () => document.querySelector<HTMLInputElement>(".memo-send-keep input")!.click());
    failOn("t1", async () => ({ error: { code: "no_memos", status: 404 } }));
    await act(async () => sendBtn().click());
    // The refresh after the partial send: t1 was deleted elsewhere, the selection is own alone.
    await rerender([own], onSent);
    expect(document.body.textContent).toMatch(/2 件中 1 件は s1\b/);
    allGo();
    await act(async () => sendBtn().click());
    expect(memoFlush).not.toHaveBeenCalled();
    expect(onSent).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("1 件のメモは一覧から無くなっていた");
  });

  it("sends the rest to the session the first batches went to", async () => {
    await render([own, t1, t2], { sessions: ["s1", "s2"] });
    const tgt = (name: string) =>
      [...document.querySelectorAll<HTMLButtonElement>(".memo-tgt")].find((b) => b.textContent?.includes(name))!;
    expect(memoFlush).not.toHaveBeenCalled();
    failOn("t2", async () => ({ error: { code: "question_pending", status: 409 } }));
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[0])).toEqual(["s1", "s1", "s1"]);
    // Another session cannot be picked for the rest.
    expect(tgt("s2").disabled).toBe(true);
    await act(async () => tgt("s2").click());
    allGo();
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => [c[0], c[1]])).toEqual([["s1", ["t2"]]]);
    expect(document.body.textContent).toMatch(/3 件を s1\b/);
  });

  it("treats an answer without the count and ids as unknown", async () => {
    for (const odd of [
      async (): Promise<FlushResult> => ({}),
      async (): Promise<FlushResult> => ({ message: "Bad Gateway" }),
      async (): Promise<FlushResult> => ({ sent: 1 }),
      async (): Promise<FlushResult> => ({ sent: 1, ids: [] }),
      async (): Promise<FlushResult> => ({ sent: 1, ids: ["not-asked"] }),
      async (): Promise<FlushResult> => ({ sent: 2, ids: ["t1"] }),
      async (): Promise<FlushResult> => ({ sent: 2, ids: ["t1", "t1"] }),
      async (): Promise<FlushResult> => ({ sent: 1, ids: ["t1", "t1"] }),
    ]) {
      await render([own, t1, t2]);
      failOn("t1", odd);
      await act(async () => sendBtn().click());
      // The send stops at t1: t2 is not sent while t1's outcome is unknown.
      expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["own"], ["t1"]]);
      expect(document.body.textContent).toContain("届いたかどうか分かりません");
      act(() => root?.unmount());
      host?.remove();
      memoFlush.mockClear();
    }
  });

  it("treats tmux_failed and delivery_unconfirmed as unknown, a refusal as not", async () => {
    const cases: [FlushResult["error"], boolean][] = [
      [{ code: "tmux_failed", status: 500 }, true],
      [{ code: "delivery_unconfirmed", status: 502 }, true],
      [{ code: "http_504", status: 504 }, true],
      [{ code: "runtime_failed", status: 502 }, false],
      [{ code: "internal", status: 500 }, false],
      [{ code: "not_running", status: 409 }, false],
    ];
    for (const [error, isUnknown] of cases) {
      await render([own, t1]);
      failOn("t1", async () => ({ error }));
      await act(async () => sendBtn().click());
      expect(document.body.textContent?.includes("届いている可能性"), error!.code).toBe(isUnknown);
      act(() => root?.unmount());
      host?.remove();
      memoFlush.mockClear();
    }
  });

  it("keeps an unknown batch flagged when its retry is refused", async () => {
    await render([own, t1, t2]);
    failOn("t1", async () => ({ error: { code: "flush_failed", status: 502 } }));
    await act(async () => sendBtn().click());
    expect(document.body.textContent).toMatch(/s1\b.*にはメッセージがすでに届いている可能性/);
    memoFlush.mockClear();
    failOn("t1", async () => ({ error: { code: "question_pending", status: 409 } }));
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["t1"]]);
    // The refusal says nothing about the first attempt, which may still have gone.
    expect(document.body.textContent).toMatch(/s1\b.*にはメッセージがすでに届いている可能性/);
    allGo();
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["t1"], ["t2"]]);
  });

  it("leaves a send refused before it went free to go elsewhere", async () => {
    // runtime_failed is mostly a runtime that could not be resumed before the send: nothing is
    // pinned, and the same text can go to another session as it could before.
    await render([memo({ id: "a", body: "one" })], { sessions: ["s1", "s2"] });
    failOn("a", async () => ({ error: { code: "runtime_failed", status: 502 } }));
    await act(async () => sendBtn().click());
    expect(document.body.textContent).not.toContain("届いたかどうか分かりません");
    const s2 = [...document.querySelectorAll<HTMLButtonElement>(".memo-tgt")].find((b) => b.textContent?.includes("s2"))!;
    expect(s2.disabled).toBe(false);
    await act(async () => s2.click());
    expect(document.querySelector<HTMLTextAreaElement>(".memo-send-text")!.readOnly).toBe(false);
    allGo();
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => [c[0], c[1]])).toEqual([["s2", ["a"]]]);
  });

  it("counts the ids the CP confirmed, not the ones asked for", async () => {
    // A was deleted elsewhere just before the send: the CP sends the own text for B alone.
    const a = memo({ id: "a", body: "one" });
    const b = memo({ id: "b", body: "two" });
    await render([a, b, t1]);
    memoFlush.mockImplementation((async (_s: string, ids: string[]) =>
      ids.includes("t1") ? { error: { code: "not_running", status: 409 } } : went(ids.filter((id) => id !== "a"))) as never);
    await act(async () => sendBtn().click());
    expect(document.body.textContent).toMatch(/3 件中 1 件は s1\b/);
    await rerender([b, t1]);
    allGo();
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["t1"]]);
    expect(document.body.textContent).toMatch(/2 件を s1\b/);
  });

  it("says a dropped memo's last send may have gone", async () => {
    await render([own, t1, t2]);
    failOn("t1", async () => ({ error: { code: "flush_failed", status: 502 } }));
    await act(async () => sendBtn().click());
    expect(document.body.textContent).toContain("送信を押すともう一度送る");
    // t1 is deleted elsewhere: it cannot be sent again, and whether it went is still open.
    await rerender([own, t2]);
    expect(document.body.textContent).not.toContain("送信を押すともう一度送る");
    expect(document.body.textContent).toContain("送信を押しても再送しない");
    allGo();
    await act(async () => sendBtn().click());
    expect(memoFlush.mock.calls.map((c) => c[1])).toEqual([["t2"]]);
    expect(document.body.textContent).toMatch(/s1\b.*に届いた可能性のあるメモが一覧から無くなっていたため/);
    expect(document.body.textContent).not.toContain("件のメモは一覧から無くなっていたため送信しませんでした");
  });
});
