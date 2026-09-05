// Pins the fix for a card badged answered whose body was the decline boilerplate
// (docs/build/92 §6): claude's own AskUserQuestion decline boilerplate (an Escape out of the
// modal — e.g. the preview free-text bug, where a free-text answer lands on the unnumbered
// "Chat about this" row) used to render as an ordinary answered card with the raw rejection
// prose dumped in as if it were the user's answer. A declined question must instead badge
// distinctly and never try to parse the rejection prose as a pick.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";
import { t as tr } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(turns: Turn[], caps: TranscriptCaps) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} />));
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const OWNER: TranscriptCaps = { agentName: "Claude", session: "s1" };

const DECLINE_TEXT =
  "The user doesn't want to proceed with this tool use. The tool use was rejected " +
  "(eg. if it was a file edit, the new_string was NOT written to the file). To tell you how " +
  "to proceed, the user said:\nThe user wants to clarify these questions.\n\n" +
  '    Questions asked:\n- "どれにしますか？"\n  (No answer provided)';

describe("QuestionBlock — declined AskUserQuestion", () => {
  it("badges it rejected, not answered, and shows no option as selected", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          {
            kind: "question",
            questions: [{ header: "方式", question: "どれにしますか？", options: [{ label: "案A" }, { label: "案B" }] }],
            answer: DECLINE_TEXT,
            declined: true,
          },
        ],
      },
    ];
    const el = render(turns, OWNER);
    expect(el.querySelector(".mt-question.declined")).not.toBeNull();
    expect(el.querySelector(".mq-done.declined")?.textContent).toBe(tr("mirror.rejected"));
    expect(el.querySelector(".mq-done")?.textContent).not.toBe(tr("mirror.answered"));
    expect(el.querySelectorAll(".mq-opt.selected").length).toBe(0);
  });

  it("does not dump claude's rejection prose into the card as a free-text answer", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          {
            kind: "question",
            questions: [{ header: "方式", question: "どれにしますか？", options: [{ label: "案A" }, { label: "案B" }] }],
            answer: DECLINE_TEXT,
            declined: true,
          },
        ],
      },
    ];
    const el = render(turns, OWNER);
    // The old behaviour rendered the whole rejection paragraph as a free-text answer callout
    // (.mq-free) — that must be gone; a short fixed note replaces it instead.
    expect(el.querySelector(".mq-free")).toBeNull();
    expect(el.textContent).not.toContain("wants to clarify");
    expect(el.querySelector(".mq-declined-note")).not.toBeNull();
  });

  it("leaves a genuinely answered question alone — still badged answered, real pick highlighted", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          {
            kind: "question",
            questions: [{ header: "方式", question: "どれにしますか？", options: [{ label: "案A" }, { label: "案B" }] }],
            answer: 'Your questions have been answered: "どれにしますか？"="案B". You can now continue.',
            declined: false,
          },
        ],
      },
    ];
    const el = render(turns, OWNER);
    expect(el.querySelector(".mt-question.declined")).toBeNull();
    expect(el.querySelector(".mq-done")?.textContent).toBe(tr("mirror.answered"));
    const selected = el.querySelectorAll(".mq-opt.selected");
    expect(selected.length).toBe(1);
    expect(selected[0].textContent).toContain("案B");
    expect(el.querySelector(".mq-declined-note")).toBeNull();
  });

  it("shows one decline note per card, not once per question, for a multi-question decline", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          {
            kind: "question",
            questions: [
              { header: "配置", question: "どこに置く？", options: [{ label: "A" }, { label: "B" }] },
              { header: "条件", question: "条件は？", options: [{ label: "C" }, { label: "D" }] },
            ],
            answer: DECLINE_TEXT,
            declined: true,
          },
        ],
      },
    ];
    const el = render(turns, OWNER);
    expect(el.querySelectorAll(".mq-done.declined").length).toBe(2); // per-question badge, unchanged
    expect(el.querySelectorAll(".mq-declined-note").length).toBe(1); // one card-level note
  });
});
