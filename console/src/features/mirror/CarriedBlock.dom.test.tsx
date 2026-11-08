// The carried-interaction card (docs/log/75). Only two things are pinned here, both of which
// fail silently and harmfully:
//
// 1. It must send no key sequence at all. A carried interaction has no modal to aim at, so a
//    Down/Enter landing in the resumed pane would decide something else (a new question, the
//    composer). The card reuses the pending card's option UI, so a regression back onto the
//    key path is possible.
// 2. Approval must go through a confirm. A prose approval alone makes claude execute the plan
//    (measured, docs/log/75 §75.10 E), so it is an irreversible decision.
//
// It also pins that the lead text renders as Markdown, as on the live question card.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { answerConfirm, confirmDialog } from "../../test/confirmDialog.ts";

const sessionCarriedAnswer = vi.fn(async () => ({ ok: true }));
const apiJSON = vi.fn(async () => ({}));
vi.mock("../../core/api/client.ts", () => ({
  // Read when the preview stores load (MarkdownView links loopback URLs to the workspace preview).
  getTenant: () => "",
  sessionCarriedAnswer: (...args: unknown[]) => sessionCarriedAnswer(...(args as [])),
  apiJSON: (...args: unknown[]) => apiJSON(...(args as [])),
  // A repo link checks its target with a parent listing before it opens (statRepoTarget).
  api: async () => ({ entries: [{ name: "a.md", type: "file" }] }),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { CarriedBlock } from "./CarriedBlock.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const errors: string[] = [];
let done = 0;

// MarkdownView (the lead text, an inline plan) needs a toast host, as in the app.
const providers = (node: ReactNode) => (
  <ToastProvider>
    <ConfirmProvider>{node}</ConfirmProvider>
  </ToastProvider>
);

function mount(
  carried: Parameters<typeof CarriedBlock>[0]["carried"],
  onReviewPlan?: (plan: string) => Promise<void>,
  extra: Partial<Parameters<typeof CarriedBlock>[0]> = {},
) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      providers(
        <CarriedBlock
          carried={carried}
          session="s1"
          agentName="claude"
          onDone={() => done++}
          onError={(m) => errors.push(m)}
          onReviewPlan={onReviewPlan}
          {...extra}
        />,
      ),
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

  it("renders a carried question's lead text as Markdown, not as raw markup", () => {
    mount({
      kind: "question",
      text: "**大事**なこと:\n- `opus` は断る\n- ID を固定する",
      questions: [{ question: "どっち？", options: [{ label: "A" }, { label: "B" }] }],
    });
    const lead = document.querySelector(".mt-carried-text")!;
    expect(lead.querySelector("strong")?.textContent).toBe("大事");
    expect(lead.querySelector("code")?.textContent).toBe("opus");
    expect(lead.querySelectorAll("li")).toHaveLength(2);
    expect(lead.textContent).not.toContain("**");
  });

  it("opens a repository file linked from the lead text through onOpenFile", async () => {
    const opened: string[] = [];
    mount(
      { kind: "question", text: "[設計](/docs/a.md) を見て", questions: [{ question: "どっち？", options: [{ label: "A" }] }] },
      undefined,
      { repo: "fleet", onOpenFile: (path) => opened.push(path) },
    );
    const a = document.querySelector<HTMLAnchorElement>(".mt-carried-text a.repo-link");
    expect(a).not.toBeNull();
    await act(async () => {
      a!.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(opened).toHaveLength(1);
    expect(opened[0]).toContain("docs/a.md");
  });

  // Text is the turn's pending-text for every kind (status.PromoteCarried), i.e. the
  // assistant's prose, so a plan or permission card renders it as Markdown too.
  it("renders the lead text of a carried plan as Markdown as well", () => {
    mount({ kind: "plan", plan: "# 計画", text: "**前置き**" });
    expect(document.querySelector(".mt-carried-text strong")?.textContent).toBe("前置き");
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
    click(byText(/承認して実行|Approve and run/));
    await act(async () => {});
    expect(confirmDialog()).not.toBeNull();
    expect(sessionCarriedAnswer).not.toHaveBeenCalled();
    await answerConfirm(false);
    expect(confirmDialog()).toBeNull();
    expect(sessionCarriedAnswer).not.toHaveBeenCalled();

    click(byText(/承認して実行|Approve and run/));
    await act(async () => {});
    await answerConfirm(true);
    expect(sessionCarriedAnswer).toHaveBeenCalledTimes(1);
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body.decision).toBe("approve");
  });

  it("approving does nothing when the plan was replaced or the card went away while the dialog was open", async () => {
    const card = (plan: string) =>
      providers(
        <CarriedBlock carried={{ kind: "plan", plan }} session="s1" agentName="claude" onDone={() => done++} onError={(m) => errors.push(m)} />,
      );
    mount({ kind: "plan", plan: "# 計画" });
    click(byText(/承認して実行|Approve and run/));
    await act(async () => {});
    act(() => root!.render(card("# 別の計画")));
    await answerConfirm(true);
    expect(sessionCarriedAnswer).not.toHaveBeenCalled();

    click(byText(/承認して実行|Approve and run/));
    await act(async () => {});
    await act(async () => root!.render(providers(null)));
    await answerConfirm(true);
    expect(sessionCarriedAnswer).not.toHaveBeenCalled();
  });

  it("rejection needs no confirm and carries the typed instructions with it", async () => {
    mount({ kind: "plan", plan: "# 計画" });
    type(document.querySelector<HTMLTextAreaElement>(".mq-freetext")!, "手順 2 を分けて");
    click(byText(/承認しない|Do not approve/));
    await act(async () => {});
    const [, body] = sessionCarriedAnswer.mock.calls[0] as unknown as [string, Record<string, unknown>];
    expect(body).toMatchObject({ decision: "reject", feedback: "手順 2 を分けて" });
  });

  it("a carried plan offers review in another session and sends nothing to the session itself", async () => {
    const review = vi.fn(async () => {});
    mount({ kind: "plan", plan: "# 計画" }, review);
    click(byText(/別セッションでレビュー|Review in another session/));
    await act(async () => {});
    expect(review).toHaveBeenCalledWith("# 計画");
    // Discarding is the container's job, after the reviewer session exists: opening (or
    // cancelling) the dialog must neither discard nor lock approval.
    expect(sessionCarriedAnswer).not.toHaveBeenCalled();
    expect((byText(/承認して実行|Approve and run/) as HTMLButtonElement).disabled).toBe(false);
  });

  it("without a review handler (other kinds, shared view) no review button is shown", () => {
    mount({ kind: "plan", plan: "# 計画" });
    expect(byText(/別セッションでレビュー|Review in another session/)).toBeNull();
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
