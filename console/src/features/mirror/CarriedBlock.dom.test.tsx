// 持ち越しカード（docs/log/75）。押さえるのは 2 点だけで、どちらも「壊れると静かに害が出る」型。
//
// ①**キー列を 1 つも送らないこと**。持ち越しには当てる先のモーダルが無いので、Down/Enter は
//   行き場を失い、再開したペインに落ちれば別のもの（新しい質問、コンポーザ）を決めてしまう。
//   保留カードと同じ選択 UI を使い回している以上、キー経路へ落ちる回帰は起こりうる。
// ②**承認は確認を挟むこと**。文章の承認だけで claude はそのまま実行する（docs/log/75 §75.10 E の
//   実測）ので、これは取り消せない決定である。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const sessionCarriedAnswer = vi.fn(async () => ({ ok: true }));
const apiJSON = vi.fn(async () => ({}));
vi.mock("../../core/api/client.ts", () => ({
  sessionCarriedAnswer: (...args: unknown[]) => sessionCarriedAnswer(...(args as [])),
  apiJSON: (...args: unknown[]) => apiJSON(...(args as [])),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { CarriedBlock } from "./CarriedBlock.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const errors: string[] = [];
let done = 0;

function mount(carried: Parameters<typeof CarriedBlock>[0]["carried"]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <CarriedBlock
        carried={carried}
        session="s1"
        agentName="claude"
        onDone={() => done++}
        onError={(m) => errors.push(m)}
      />,
    ),
  );
}

beforeEach(() => {
  sessionCarriedAnswer.mockClear();
  apiJSON.mockClear();
  errors.length = 0;
  done = 0;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

const click = (el: Element | null) => act(() => (el as HTMLElement).click());
// React は value プロパティを握っているので、ネイティブ setter 経由で書かないと onChange が
// 発火しない（ChatPlan.dom.test.tsx と同じ流儀）。
const type = (el: HTMLTextAreaElement, v: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const buttons = () => Array.from(document.querySelectorAll("button"));
const byText = (re: RegExp) => buttons().find((b) => re.test(b.textContent || "")) || null;

describe("CarriedBlock", () => {
  it("質問に答えると /input ではなく carried-answer へラベルが渡る", async () => {
    mount({ kind: "question", questions: [{ question: "どっち？", options: [{ label: "A" }, { label: "B" }] }] });
    click(document.querySelectorAll(".mq-opt")[1]); // B を選ぶ
    click(document.querySelector(".mq-submit"));
    await act(async () => {});

    expect(sessionCarriedAnswer).toHaveBeenCalledTimes(1);
    const [session, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(session).toBe("s1");
    expect(body.decision).toBe("answer");
    expect(body.answers).toEqual([{ labels: ["B"], notes: "" }]);
    // ★キー経路へは一切落ちない。
    expect(apiJSON).not.toHaveBeenCalled();
    expect(errors).toEqual([]);
    expect(done).toBe(1);
  });

  it("自由入力だけでも送れる（preview 付き AUQ の notes 形）", async () => {
    mount({ kind: "question", questions: [{ question: "どっち？", options: [{ label: "A" }] }] });
    type(document.querySelector<HTMLTextAreaElement>(".mq-freetext")!, "どちらでもない");
    click(document.querySelector(".mq-submit"));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.answers).toEqual([{ labels: [], notes: "どちらでもない" }]);
  });

  it("プランの承認は確認を挟み、拒否すれば送らない", async () => {
    mount({ kind: "plan", plan: "# 計画" });
    vi.stubGlobal("confirm", vi.fn(() => false));
    click(byText(/承認して実行|Approve and run/));
    await act(async () => {});
    expect(sessionCarriedAnswer).not.toHaveBeenCalled();

    vi.stubGlobal("confirm", vi.fn(() => true));
    click(byText(/承認して実行|Approve and run/));
    await act(async () => {});
    expect(sessionCarriedAnswer).toHaveBeenCalledTimes(1);
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.decision).toBe("approve");
  });

  it("却下は確認なしで、入力した指示が一緒に飛ぶ", async () => {
    mount({ kind: "plan", plan: "# 計画" });
    type(document.querySelector<HTMLTextAreaElement>(".mq-freetext")!, "手順 2 を分けて");
    click(byText(/承認しない|Do not approve/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body).toMatchObject({ decision: "reject", feedback: "手順 2 を分けて" });
  });

  it("許可は「続けて再開」だけを出す（許可の答えは届かないので）", async () => {
    mount({ kind: "permission", permission: "Bash · npm ci" });
    expect(document.body.textContent).toContain("Bash · npm ci");
    click(byText(/続けて再開|Resume and continue/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.decision).toBe("continue");
  });

  it("破棄はセッションを起こさない（discard を送るだけ）", async () => {
    mount({ kind: "question", questions: [{ question: "どっち？", options: [{ label: "A" }] }] });
    click(byText(/破棄|Discard/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.decision).toBe("discard");
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("送信が失敗したらカードは残り、理由が出る（沈黙は成功と区別できない）", async () => {
    sessionCarriedAnswer.mockResolvedValueOnce({ ok: false, message: "workspace is stopping" } as never);
    mount({ kind: "permission", permission: "Bash · ls" });
    click(byText(/続けて再開|Resume and continue/));
    await act(async () => {});
    expect(errors).toEqual(["workspace is stopping"]);
    expect(done).toBe(0);
    expect(document.querySelector(".mt-carried")).toBeTruthy();
  });
});
