// The carried-interaction card (docs/log/75). Only two things are pinned here, both of which
// fail silently and harmfully:
//
// 1. It must send no key sequence at all. A carried interaction has no modal to aim at, so a
//    Down/Enter landing in the resumed pane would decide something else (a new question, the
//    composer). The card reuses the pending card's option UI, so a regression back onto the
//    key path is possible.
// 2. Approval must go through a confirm. A prose approval alone makes claude execute the plan
//    (measured, docs/log/75 §75.10 E), so it is an irreversible decision.
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
// React owns the value property, so onChange only fires when the write goes through the
// native setter (same idiom as ChatPlan.dom.test.tsx).
const type = (el: HTMLTextAreaElement, v: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const buttons = () => Array.from(document.querySelectorAll("button"));
const byText = (re: RegExp) => buttons().find((b) => re.test(b.textContent || "")) || null;

describe("CarriedBlock", () => {
  it("answering a question passes the labels to carried-answer, not to /input", async () => {
    mount({ kind: "question", questions: [{ question: "どっち？", options: [{ label: "A" }, { label: "B" }] }] });
    click(document.querySelectorAll(".mq-opt")[1]); // pick B
    click(document.querySelector(".mq-submit"));
    await act(async () => {});

    expect(sessionCarriedAnswer).toHaveBeenCalledTimes(1);
    const [session, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(session).toBe("s1");
    expect(body.decision).toBe("answer");
    expect(body.answers).toEqual([{ labels: ["B"], notes: "" }]);
    // Never falls through to the key path.
    expect(apiJSON).not.toHaveBeenCalled();
    expect(errors).toEqual([]);
    expect(done).toBe(1);
  });

  it("sends with free text alone (the notes shape of an AUQ with previews)", async () => {
    mount({ kind: "question", questions: [{ question: "どっち？", options: [{ label: "A" }] }] });
    type(document.querySelector<HTMLTextAreaElement>(".mq-freetext")!, "どちらでもない");
    click(document.querySelector(".mq-submit"));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.answers).toEqual([{ labels: [], notes: "どちらでもない" }]);
  });

  it("plan approval goes through a confirm and sends nothing when it is declined", async () => {
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

  it("rejection needs no confirm and carries the typed instructions with it", async () => {
    mount({ kind: "plan", plan: "# 計画" });
    type(document.querySelector<HTMLTextAreaElement>(".mq-freetext")!, "手順 2 を分けて");
    click(byText(/承認しない|Do not approve/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body).toMatchObject({ decision: "reject", feedback: "手順 2 を分けて" });
  });

  it("a permission offers only resume-and-continue, since the answer cannot be delivered", async () => {
    mount({ kind: "permission", permission: "Bash · npm ci" });
    expect(document.body.textContent).toContain("Bash · npm ci");
    click(byText(/続けて再開|Resume and continue/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.decision).toBe("continue");
  });

  it("discard does not wake the session — it only sends discard", async () => {
    mount({ kind: "question", questions: [{ question: "どっち？", options: [{ label: "A" }] }] });
    click(byText(/破棄|Discard/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.decision).toBe("discard");
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("a failed send keeps the card and surfaces the reason (silence reads as success)", async () => {
    sessionCarriedAnswer.mockResolvedValueOnce({ ok: false, message: "workspace is stopping" } as never);
    mount({ kind: "permission", permission: "Bash · ls" });
    click(byText(/続けて再開|Resume and continue/));
    await act(async () => {});
    expect(errors).toEqual(["workspace is stopping"]);
    expect(done).toBe(0);
    expect(document.querySelector(".mt-carried")).toBeTruthy();
  });
});
