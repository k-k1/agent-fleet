// The manual Tidy dialog: the in-place tidy for ordinary notes still PATCHes, and a selection inside
// one auto-run category gets the operations: the preview, the one apply call, and what is said for
// an applied run, a stale one, a refused one and a selection that cannot be operated on.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let reply = "";
let replyError: { code: string } | null = null;
const ask = vi.fn(async (_p: string) => (replyError ? { error: replyError } : { reply }));
vi.mock("../chat/api.ts", () => ({ askAssistant: (p: string) => ask(p) }));

let applyAnswer: Record<string, unknown> = {};
const memoTidyApply = vi.fn(async (_b: unknown): Promise<Record<string, unknown>> => applyAnswer);
const memoUpdate = vi.fn(async (_id: string, _p: unknown): Promise<Record<string, unknown>> => ({}));
const memoQueueList = vi.fn(async (): Promise<unknown> => ({ queues: [{ categoryId: "run", tidyDestinations: ["listed"] }] }));
vi.mock("./api.ts", () => ({
  memoTidyApply: (b: unknown) => memoTidyApply(b),
  memoUpdate: (id: string, p: unknown) => memoUpdate(id, p),
  memoQueueList: () => memoQueueList(),
}));

const { MemoTidyModal } = await import("./MemoTidyModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

const cat = (id: string, name: string, queue = false): MemoCategory => ({ id, repo: "app", name, position: 0, createdAt: "", queue });
const cats = [cat("run", "run", true), cat("listed", "listed", true), cat("other", "other", true), cat("plain", "plain")];
const note = (id: string, over: Partial<Memo> = {}): Memo => ({
  id, repo: "app", category: "run", kind: "text", body: "note " + id, refPath: "", position: 0, createdAt: "", sentAt: "",
  state: "pending", provenance: "member", contentHash: "h-" + id, ...over,
});

let root: Root | null = null;
let host: HTMLDivElement;
const onDone = vi.fn();
const onReload = vi.fn();
const onClose = vi.fn();

const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function open(memos: Memo[]) {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <MemoTidyModal memos={memos} categories={cats} onDone={onDone} onReload={onReload} onClose={onClose} />
      </ToastProvider>,
    );
  });
  await settle();
}
const text = () => document.body.textContent ?? "";
const applyBtn = () => [...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].find((b) => b.className.includes("primary"))!;
async function press() {
  await act(async () => applyBtn().click());
  await settle();
}
const ops = (...o: unknown[]) => JSON.stringify({ operations: o });

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  reply = ops();
  replyError = null;
  applyAnswer = { run: { id: "r1", state: "applied", merged: 1, split: 0, moved: 1, held: 1 }, memos: [] };
  for (const f of [ask, memoTidyApply, memoUpdate, memoQueueList, onDone, onReload, onClose]) f.mockClear();
});
afterEach(async () => {
  await act(async () => root!.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("ordinary notes", () => {
  it("still tidy in place: the rows are asked for and PATCHed, and no operation reaches the Control Plane", async () => {
    reply = JSON.stringify([{ id: "a", cleaned: "clean a", suggestedCategory: "plain" }]);
    await open([note("a", { category: "plain" })]);
    expect(ask.mock.calls[0][0]).toContain("suggestedCategory");
    expect(ask.mock.calls[0][0]).not.toContain('"op":"merge"');
    expect(text()).toContain("clean a");
    await press();
    expect(memoUpdate).toHaveBeenCalledWith("a", { body: "clean a" });
    expect(memoTidyApply).not.toHaveBeenCalled();
    expect(memoQueueList).not.toHaveBeenCalled();
  });
  it("says why a selection over several categories gets the in-place tidy", async () => {
    reply = JSON.stringify([{ id: "a", cleaned: "x", suggestedCategory: "" }]);
    await open([note("a"), note("b", { category: "plain" })]);
    expect(text()).toContain(t("memo.tidyop_several_categories"));
    expect(ask.mock.calls[0][0]).not.toContain('"op":"merge"');
  });
});

describe("a selection inside one auto-run category", () => {
  const sel = () => [note("a"), note("b"), note("c"), note("d", { state: "held" })];

  it("asks for operations, previews each, and one approval sends one call", async () => {
    reply = "はい。\n```json\n" + ops(
      { op: "merge", ids: ["a", "b"], text: "ab merged" },
      { op: "move", id: "c", to: "other" },
      { op: "move", id: "d", to: "plain" },
    ) + "\n```";
    await open(sel());
    expect(ask.mock.calls[0][0]).toContain('"op":"merge"');
    expect(ask.mock.calls[0][0]).toContain('"id": "listed"'); // the listed destination is offered
    expect(text()).toContain(t("memo.tidyop_merge", { count: 2 }));
    expect(text()).toContain("ab merged");
    expect(text()).toContain(t("memo.tidyop_result_run"));
    expect(text()).toContain(t("memo.tidyop_move_approve", { from: "app/run" }));
    expect(text()).toContain(t("memo.tidyop_move_note"));
    expect(text()).toContain(t("memo.tidyop_mixed_states"));
    await press();
    expect(memoTidyApply).toHaveBeenCalledTimes(1);
    expect(memoTidyApply).toHaveBeenCalledWith({
      repo: "app",
      category: "run",
      items: sel().map((m) => ({ id: m.id, contentHash: "h-" + m.id })),
      operations: [
        { op: "merge", ids: ["a", "b"], text: "ab merged" },
        { op: "move", id: "c", to: "other" },
        { op: "move", id: "d", to: "plain" },
      ],
    });
    expect(memoUpdate).not.toHaveBeenCalled();
    expect(text()).toContain(t("memo.tidyop_done", { merged: 1, split: 0, moved: 1 }));
    expect(text()).toContain(t("memo.tidyop_done_held", { held: 1 }));
    expect(text()).toContain(t("memo.tidyop_done_undo"));
    expect(onReload).toHaveBeenCalled();
  });

  it("sends only the proposals the member left ticked", async () => {
    reply = ops({ op: "move", id: "a", to: "plain" }, { op: "move", id: "b", to: "plain" });
    await open(sel());
    const boxes = document.querySelectorAll<HTMLInputElement>('.memo-tidy-row input[type="checkbox"]');
    await act(async () => boxes[0].click());
    await press();
    expect((memoTidyApply.mock.calls[0][0] as { operations: unknown[] }).operations).toEqual([{ op: "move", id: "b", to: "plain" }]);
  });

  it("shows a merge across different origins as left out, with its notes, and does not send it", async () => {
    const mixed = [note("a"), note("w", { provenance: "workspace", producer: "s" }), note("b")];
    reply = ops({ op: "merge", ids: ["a", "w"], text: "x" }, { op: "move", id: "b", to: "plain" });
    await open(mixed);
    expect(text()).toContain(t("memo.tidyop_out_title"));
    expect(text()).toContain(t("memo.tidyop_out_origin"));
    expect(document.querySelector('[data-left-out="other_origin"]')!.textContent).toContain("note w");
    await press();
    expect((memoTidyApply.mock.calls[0][0] as { operations: unknown[] }).operations).toEqual([{ op: "move", id: "b", to: "plain" }]);
  });

  it("says the notes changed on a stale answer and reloads them", async () => {
    reply = ops({ op: "move", id: "a", to: "plain" });
    applyAnswer = { error: { code: "memo_tidy_stale", message: "x", stale: ["a"] } };
    await open(sel());
    await press();
    expect(text()).toContain(t("memo.tidyop_stale"));
    expect(onReload).toHaveBeenCalledTimes(1);
    expect(onDone).not.toHaveBeenCalled();
  });

  it("shows the refusal's words and keeps the preview, changing nothing", async () => {
    reply = ops({ op: "move", id: "a", to: "plain" });
    applyAnswer = { error: { code: "memo_tidy_invalid", message: "x", tidyCode: "duplicate_ref" } };
    await open(sel());
    await press();
    expect(text()).toContain(t("memo.tidyop_invalid", { reason: t("memo.tidyop_code_duplicate_ref") }));
    expect(onReload).not.toHaveBeenCalled();
    expect(applyBtn().disabled).toBe(false); // the preview is still there
  });

  it("names a refusal code it has no words for", async () => {
    reply = ops({ op: "move", id: "a", to: "plain" });
    applyAnswer = { error: { code: "memo_tidy_invalid", message: "x", tidyCode: "brand_new" } };
    await open(sel());
    await press();
    expect(text()).toContain("brand_new");
  });

  it("says a tidy is already in progress", async () => {
    reply = ops({ op: "move", id: "a", to: "plain" });
    applyAnswer = { error: { code: "memo_tidy_busy", message: "x" } };
    await open(sel());
    await press();
    expect(text()).toContain(t("err.memo_tidy_busy"));
  });

  it("explains a reply it cannot read without calling the Control Plane", async () => {
    reply = "sorry, no";
    await open(sel());
    expect(text()).toContain(t("memo.tidyop_parse_failed"));
    expect(applyBtn().disabled).toBe(true);
    expect(memoTidyApply).not.toHaveBeenCalled();
  });

  it("refuses a selection with notes a run already holds, in words, and asks nothing", async () => {
    await open([note("a"), note("b", { state: "claimed" })]);
    expect(text()).toContain(t("memo.tidyop_not_waiting", { count: 1 }));
    expect(ask).not.toHaveBeenCalled();
  });

  it("says when the assistant proposed nothing", async () => {
    reply = ops();
    await open(sel());
    expect(text()).toContain(t("memo.tidyop_none"));
    expect(applyBtn().disabled).toBe(true);
  });
});
