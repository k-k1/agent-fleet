// The skill picker stays up as an argument hint while a skill's arguments are typed. A tap on
// a composer control next to it (send) has to reach that control on the first try, while the
// active candidate list, which overlaps the transcript, still withholds an outside tap.
import { describe, it, expect, afterEach, vi } from "vitest";
import { useRef, useState } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../core/api/client.ts", () => ({
  sessionSkills: vi.fn(async () => ({
    skills: [{ name: "issue-to-pr", invoke: "/issue-to-pr ", source: "user", description: "" }],
  })),
}));

import { useSkillPicker } from "./useSkillPicker.ts";
import { AGENTS } from "../../../agents/registry.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function Composer({ onSend }: { onSend: () => void }) {
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const picker = useSkillPicker({
    session: "s1",
    agent: AGENTS.claude,
    managed: false,
    draft,
    setDraft,
    setHistIdx: () => {},
    inputRef,
    composerLocked: false,
  });
  return (
    <>
      {picker.listVisible && <div data-testid="hint" ref={picker.popRef} />}
      <textarea
        data-testid="input"
        ref={inputRef}
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value);
          picker.trackTyping(e.target.value, e.target.selectionStart ?? e.target.value.length);
        }}
      />
      <button data-testid="send" onClick={onSend}>
        send
      </button>
    </>
  );
}

const q = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`);

function type(el: HTMLTextAreaElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  set.call(el, value);
  el.setSelectionRange(value.length, value.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function tap(el: HTMLElement) {
  const down = new MouseEvent("pointerdown", { bubbles: true, cancelable: true, button: 0 });
  Object.assign(down, { pointerType: "touch", isPrimary: true });
  const up = new MouseEvent("pointerup", { bubbles: true, cancelable: true, button: 0 });
  Object.assign(up, { pointerType: "touch", isPrimary: true });
  el.dispatchEvent(down);
  el.dispatchEvent(up);
  el.dispatchEvent(new Event("touchend", { bubbles: true, cancelable: true }));
  el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, detail: 1 }));
}

describe("useSkillPicker outside tap", () => {
  it("the first tap on send goes through while the hint is shown", async () => {
    const onSend = vi.fn();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<Composer onSend={onSend} />));
    await act(async () => type(q("input") as HTMLTextAreaElement, "/issue-to-pr opus"));
    expect(q("hint")).not.toBeNull(); // the passive hint is up, so a dismiss layer is live
    act(() => tap(q("send")!));
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it("the active list still only closes on an outside tap", async () => {
    const onSend = vi.fn();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<Composer onSend={onSend} />));
    await act(async () => type(q("input") as HTMLTextAreaElement, "/iss"));
    expect(q("hint")).not.toBeNull(); // candidates for "/iss" are listed
    act(() => tap(q("send")!));
    expect(onSend).not.toHaveBeenCalled();
  });

  // The dismissed token has to trigger a render on its own: a typing-initiated list never set
  // skillBtnOpen, so closing it changes no other state and the list would stay drawn.
  it("an outside tap closes the argument hint at once", async () => {
    const onSend = vi.fn();
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<Composer onSend={onSend} />));
    await act(async () => type(q("input") as HTMLTextAreaElement, "/issue-to-pr opus"));
    expect(q("hint")).not.toBeNull();
    act(() => tap(q("send")!));
    expect(q("hint")).toBeNull();
  });

  it("Esc closes a typed list at once", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<Composer onSend={() => {}} />));
    await act(async () => type(q("input") as HTMLTextAreaElement, "/iss"));
    expect(q("hint")).not.toBeNull();
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(q("hint")).toBeNull();
  });
});
