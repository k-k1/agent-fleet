// The owner's approval card must show everything approval will relay (#3483), and a proposal
// that is still processing has no primary action.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ProposalCard, type Proposal } from "./ProposalCard.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(() => { act(() => root?.unmount()); host?.remove(); root = null; host = null; });

function mount(p: Proposal, onDecide = vi.fn()) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<ProposalCard p={p} onDecide={onDecide} />));
  return host;
}

const base: Proposal = {
  id: "p1", sessionId: "cat-1", sessionName: "s-1", sessionTitle: "Fix login", proposerUserKey: "bob",
  proposerEmail: "bob@example.com", action: "turn", status: "pending", expiresAt: "",
  payload: { op: "start", prompt: "please run it" },
};

describe("ProposalCard", () => {
  it("shows session, action, text and the proposer by login id, with a primary approve", () => {
    const el = mount(base);
    expect(el.textContent).toContain("bob@example.com");
    expect(el.textContent).toContain("Fix login");
    expect(el.textContent).toContain("turn / start");
    expect(el.textContent).toContain("please run it");
    expect(el.querySelector("button.ui-btn-primary")).not.toBeNull();
  });

  it("lists every attachment, the reply scope and unknown fields", () => {
    const el = mount({ ...base, payload: { ...base.payload, attachments: ["/a/one", "/a/two"], scope: "thread", mystery: 1 } });
    expect(el.textContent).toContain("/a/one");
    expect(el.textContent).toContain("/a/two");
    expect(el.textContent).toContain("thread");
    expect(el.textContent).toContain("mystery");
  });

  it("offers no primary button on a processing proposal, and none on a refused one", () => {
    const processing = mount({ ...base, status: "processing" });
    expect(processing.querySelector("button.ui-btn-primary")).toBeNull();
    expect(processing.querySelectorAll("button")).toHaveLength(1);
    act(() => root?.unmount()); host?.remove();
    const refused = mount({ ...base, refused: "attachments are not accepted" });
    expect(refused.querySelector("button.ui-btn-primary")).toBeNull();
  });

  it("shows what each of the four actions will send, including nested answers", () => {
    const answer = mount({ ...base, action: "answer-question", payload: { choices: [2, 1] } });
    expect(answer.textContent).toContain("2, 1");
    act(() => root?.unmount()); host?.remove();
    const respond = mount({
      ...base, action: "respond",
      payload: { id: "int-9", decision: "answer", scope: "turn", answers: [{ text: "use blue", options: [3] }, { options: [1, 2] }] },
    });
    for (const want of ["int-9", "answer", "turn", "use blue", "[3]", "[1, 2]"]) expect(respond.textContent).toContain(want);
    act(() => root?.unmount()); host?.remove();
    const plan = mount({ ...base, action: "plan-respond", payload: { decision: "reject", feedback: "split it up" } });
    expect(plan.textContent).toContain("plan-respond / reject");
    expect(plan.textContent).toContain("split it up");
    act(() => root?.unmount()); host?.remove();
    const turn = mount({ ...base, payload: { op: "steer", prompt: "go", clientMessageID: "cm-1" } });
    expect(turn.textContent).toContain("turn / steer");
    expect(turn.textContent).toContain("cm-1");
  });

  it("renders a refused legacy proposal with malformed answers and still lets it be rejected", () => {
    const onDecide = vi.fn();
    const el = mount({
      ...base, action: "respond", refused: "legacy",
      payload: { id: "i", decision: "answer", answers: [null, { text: { evil: 1 } }, "str", { options: ["x"] }, { text: "ok", options: [1] }] },
    }, onDecide);
    expect(el.textContent).toContain("ok");
    expect(el.textContent).toContain("evil");
    // A refused pending proposal offers exactly one button: reject.
    expect(el.querySelectorAll("button")).toHaveLength(1);
    const reject = el.querySelector("button");
    expect(reject).toBeTruthy();
    act(() => reject!.click());
    expect(onDecide).toHaveBeenCalledWith("p1", "reject");
  });
});
