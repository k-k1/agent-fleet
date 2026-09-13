// The translate affordance in a rendered turn (docs/log/97): when it is offered, what a press
// swaps out, and what it must NOT touch.
//
// The rules under test are the ones that are invisible until they break:
//   - no wiring (the shared view) = no button at all, ever;
//   - an answer the reader can already read is not offered one;
//   - a shown translation replaces the prose but leaves tool traces, code and marks alone;
//   - flipping back to the original never calls the server.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// MarkdownView pulls in all of remark/rehype; the source string is all this file needs to see.
vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source, markRoot }: { source?: string; markRoot?: string }) => (
    <div className="markdown" data-markroot={markRoot ?? ""}>
      {source}
    </div>
  ),
}));

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";
import type { TranscriptTranslateWiring } from "../useTranslate.ts";
import { translateHash } from "../translate.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(turns: Turn[], caps: TranscriptCaps, working = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} working={working} />));
  return host;
}

/** The assistant's prose is the LAST rendered Markdown block: the first belongs to the user's
 *  own prompt, which is never translated. */
const prose = (): HTMLElement => Array.from(host!.querySelectorAll<HTMLElement>(".markdown")).at(-1)!;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const ENGLISH = "Done — the tests are green and the branch is pushed.";
const JAPANESE = "テストは全部緑で、ブランチも push 済みです。";

const answer = (text: string, withTool = false): Turn[] => [
  { role: "user", text: "やって", idx: 1, anchorId: "u1", ts: "2026-09-13T10:00:00Z" },
  {
    role: "assistant",
    idx: 2,
    anchorId: "a1",
    ts: "2026-09-13T10:01:00Z",
    parts: withTool
      ? [{ kind: "tool", tool: "Bash", info: "go test", output: "ok" }, { kind: "text", text }]
      : [{ kind: "text", text }],
  },
];

/** A wiring whose state the test drives by hand, so a press is observable without a server. */
function wiring(entries: Record<string, string> = {}): TranscriptTranslateWiring & { calls: string[][] } {
  const held = new Map(Object.entries(entries));
  const shown = new Set<string>();
  const calls: string[][] = [];
  return {
    lang: "ja",
    calls,
    get: (text: string) => held.get(translateHash(text)),
    shown: (key: string) => shown.has(key),
    busy: () => false,
    error: () => undefined,
    toggle: (key: string, texts: string[]) => {
      if (shown.has(key)) {
        shown.delete(key);
        return;
      }
      if (!texts.every((t) => held.has(translateHash(t)))) calls.push(texts);
      for (const t of texts) if (!held.has(translateHash(t))) held.set(translateHash(t), "訳: " + t);
      shown.add(key);
    },
  };
}

const capsWith = (tx?: TranscriptTranslateWiring): TranscriptCaps => ({
  agentName: "Claude",
  session: "s1",
  translate: tx,
});

describe("the mirror's translate button", () => {
  it("is offered on an answer the reader cannot read", () => {
    const el = render(answer(ENGLISH), capsWith(wiring()));
    expect(el.querySelector(".mt-translate")).not.toBeNull();
  });

  it("is not offered when the answer is already in the reader's language", () => {
    const el = render(answer(JAPANESE), capsWith(wiring()));
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("is not offered at all without the wiring (a shared session is read as written)", () => {
    const el = render(answer(ENGLISH), capsWith(undefined));
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("is not offered while the turn is still streaming", () => {
    // working=true hands the live exchange an unfolded work trace; translating a half-written
    // answer spends tokens on text that is about to change.
    const el = render(answer(ENGLISH), capsWith(wiring()), true);
    expect(el.querySelector(".mt-translate")).toBeNull();
  });

  it("swaps the prose for the translation on a press, and back on the next one", () => {
    const tx = wiring();
    const caps = capsWith(tx);
    const el = render(answer(ENGLISH), caps);
    expect(prose().textContent).toBe(ENGLISH);

    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    // The wiring the mirror supplies re-renders through caps; here the test re-renders itself.
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    expect(tx.calls).toHaveLength(1);

    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe(ENGLISH);
    // Going back to the original is a display flip, never a request.
    expect(tx.calls).toHaveLength(1);
  });

  it("shows a held translation without asking the server again", () => {
    const tx = wiring({ [translateHash(ENGLISH)]: "保持していた訳" });
    const caps = capsWith(tx);
    const el = render(answer(ENGLISH), caps);
    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("保持していた訳");
    expect(tx.calls).toHaveLength(0);
  });

  it("drops the mark root off translated prose", () => {
    const tx = wiring();
    const caps = { ...capsWith(tx), marks: { byRoot: new Map(), authorSlot: () => 0 } as never };
    const el = render(answer(ENGLISH), caps);
    expect(prose().dataset.markroot).not.toBe("");

    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    // A mark anchors by quoted text (docs/log/69 §69.3), so it cannot be painted over a
    // translation: it would underline whatever happened to match.
    expect(prose().dataset.markroot).toBe("");
  });

  it("anchors marks on the final answer of a tool-using turn", () => {
    // Regression: a work split hands renderAssistantParts a SLICE, and foldParts numbers from 0
    // inside it, while turn.origins is indexed by the position in the whole turn. Without the
    // offset the answer took the first part's root — a tool's, i.e. "" — and the paragraph
    // readers most want to mark could not be marked at all.
    const caps = { ...capsWith(undefined), marks: { byRoot: new Map(), authorSlot: () => 0 } as never };
    render(answer(ENGLISH, true), caps);
    expect(prose().dataset.markroot).toBe("a1#1");
  });

  it("translates the prose of a tool-using turn and leaves the work trace alone", () => {
    const tx = wiring();
    const caps = capsWith(tx);
    const el = render(answer(ENGLISH, true), caps);
    act(() => el.querySelector<HTMLButtonElement>(".mt-translate")!.click());
    act(() => root!.render(<TranscriptView groups={groupTurns(answer(ENGLISH, true))} caps={{ ...caps }} />));
    expect(prose().textContent).toBe("訳: " + ENGLISH);
    // Only prose is ever sent: the trace is the agent's own output, and a translated command
    // line is not something a reader can run.
    expect(tx.calls).toEqual([[ENGLISH]]);
    expect(el.textContent).toContain("go test");
  });
});
