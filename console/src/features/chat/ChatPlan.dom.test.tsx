// 作業計画パネル（docs/log/33 第5段）の描画・操作テスト。
//
// 守りたいのは「原文キャリーフォワードの誤上書きを人が直せる」経路そのもの:
// 編集中に外から計画が動いても打ちかけの文字を消さない、保存は打った本文をそのまま
// 送る、クリアは確認を通す。ここが壊れると、自動更新が暴走したときの逃げ道が無くなる。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const setPlan = vi.fn();
const refreshPlan = vi.fn();
const confirmAnswer = { value: true };

vi.mock("./api.ts", () => ({
  chatSetPlan: (id: string, plan: string) => setPlan(id, plan),
  chatRefreshPlan: (id: string) => refreshPlan(id),
}));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => async () => confirmAnswer.value,
}));
// MarkdownView は本文を DOM へ流し込むだけで十分（描画経路の検証は本題ではない）。
vi.mock("../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <pre>{source}</pre>,
}));

const { ChatPlan } = await import("./ChatPlan.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

const CONV = { id: "c1", agent: "claude", title: "t", created_at: 0, updated_at: 0, messages: [] };

// React は value プロパティの変更を握っているので、ネイティブ setter 経由で書かないと
// onChange が発火しない（LaunchModal.dom.test.tsx と同じ流儀）。
async function type(el: HTMLTextAreaElement, v: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const btn = (label: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(label))!;
const area = () => host.querySelector<HTMLTextAreaElement>(".cp-edit");

async function render(props: Record<string, unknown> = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <ChatPlan
        conversationId="c1"
        plan="## これからやること\n- Lane A"
        onUpdated={() => {}}
        onClose={() => {}}
        {...props}
      />,
    );
  });
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
}

beforeEach(() => {
  setPlan.mockReset().mockResolvedValue(CONV);
  refreshPlan.mockReset().mockResolvedValue(CONV);
  confirmAnswer.value = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("ChatPlan", () => {
  it("shows the plan body and hides the clear action when there is no plan", async () => {
    await render();
    expect(host.textContent).toContain("Lane A");
    expect(btn("クリア")).toBeTruthy();
    await render({ plan: "" });
    expect(btn("クリア")).toBeUndefined();
  });

  it("saves exactly what was typed", async () => {
    await render();
    await click(btn("編集"));
    await type(area()!, "## これからやること\n- Lane 2 を先に");
    await click(btn("保存"));
    expect(setPlan).toHaveBeenCalledWith("c1", "## これからやること\n- Lane 2 を先に");
  });

  // ★編集中に圧縮や別ペインが計画を書き換えても、打ちかけの本文を奪わない。
  it("does not overwrite the draft while editing", async () => {
    await render();
    await click(btn("編集"));
    await type(area()!, "打ちかけ");
    await render({ plan: "## 外から入った新しい計画" });
    expect(area()!.value).toBe("打ちかけ");
    // 編集を取り消せば、外から入った最新版に追随する。
    await click(btn("キャンセル"));
    expect(host.textContent).toContain("外から入った新しい計画");
  });

  it("clears only after the confirmation is accepted", async () => {
    confirmAnswer.value = false;
    await render();
    await click(btn("クリア"));
    expect(setPlan).not.toHaveBeenCalled();
    confirmAnswer.value = true;
    await click(btn("クリア"));
    expect(setPlan).toHaveBeenCalledWith("c1", "");
  });

  it("blocks the LLM refresh while the conversation is busy, but never the local edit path", async () => {
    await render({ disabled: true });
    expect(btn("更新").disabled).toBe(true);
    await click(btn("更新"));
    expect(refreshPlan).not.toHaveBeenCalled();
    await render({ disabled: false });
    await click(btn("更新"));
    expect(refreshPlan).toHaveBeenCalledWith("c1");
  });

  it("surfaces a failed update instead of silently keeping the old plan", async () => {
    refreshPlan.mockResolvedValue({ error: "boom" });
    await render();
    await click(btn("更新"));
    expect(host.querySelector(".chat-error")).toBeTruthy();
  });
});
