import { describe, it, expect, vi, afterEach } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { MarkLayer } from "./MarkLayer.tsx";
import { MARK_CLASS } from "./markPaint.ts";
import { SELECT_DEBOUNCE } from "../../../lib/selectionCapture.ts";
import type { TranscriptMarksWiring } from "./useMarks.ts";
import type { NewMark, TranscriptMark } from "./marks.ts";

// Two mirrors side by side each mount a MarkLayer, and both hear the same document-wide
// selection. Only the one whose transcript holds the selection may offer the pill: otherwise the
// pill mounted last (the other pane's) covers it, takes the click and paints into that session.

const MARK: TranscriptMark = { id: "m1", turn: "t1", part: -1, kind: "text", quote: "hello", nth: 0, color: "yellow" };

function wiring() {
  return {
    byRoot: new Map(),
    all: [],
    canEdit: true,
    add: vi.fn<(m: NewMark) => void>(),
    remove: vi.fn(),
    canRemove: () => true,
    authorLabel: () => "",
    authorSlot: () => 0,
    find: vi.fn((id: string): TranscriptMark | undefined => (id === "m1" ? MARK : undefined)),
  } satisfies TranscriptMarksWiring;
}

function mountTranscript(marks: TranscriptMarksWiring): { host: HTMLElement; unmount: () => void } {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  act(() =>
    root.render(
      <>
        <div data-mark-root="t1#b" data-mark-kind="text">
          hello world <mark className={MARK_CLASS} data-mark-id="m1">hello</mark>
        </div>
        <MarkLayer marks={marks} />
      </>,
    ),
  );
  return {
    host,
    unmount: () => {
      act(() => root.unmount());
      host.remove();
    },
  };
}

function selectIn(host: HTMLElement): void {
  const text = host.querySelector("[data-mark-root]")!.firstChild!;
  const range = document.createRange();
  range.setStart(text, 0);
  range.setEnd(text, 5);
  const sel = window.getSelection()!;
  sel.removeAllRanges();
  sel.addRange(range);
  act(() => void document.dispatchEvent(new Event("selectionchange")));
  act(() => vi.advanceTimersByTime(SELECT_DEBOUNCE + 10));
}

afterEach(() => {
  window.getSelection()?.removeAllRanges();
  vi.useRealTimers();
});

describe("MarkLayer with two transcripts open", () => {
  it("offers one pill, and paints into the transcript that holds the selection", () => {
    vi.useFakeTimers();
    const a = wiring();
    const b = wiring();
    const ta = mountTranscript(a);
    const tb = mountTranscript(b);

    selectIn(ta.host);
    const pills = document.querySelectorAll(".tmark-pill");
    expect(pills.length).toBe(1);
    act(() => (pills[0].querySelector<HTMLButtonElement>(".tmark-swatch")!).click());
    expect(a.add).toHaveBeenCalledTimes(1);
    expect(a.add.mock.calls[0][0]).toMatchObject({ turn: "t1", quote: "hello" });
    expect(b.add).not.toHaveBeenCalled();

    tb.unmount();
    ta.unmount();
  });

  it("opens the card of a clicked mark only in its own transcript", () => {
    const a = wiring();
    const b = wiring();
    const ta = mountTranscript(a);
    const tb = mountTranscript(b);

    act(() => ta.host.querySelector<HTMLElement>("mark." + MARK_CLASS)!.click());
    expect(a.find).toHaveBeenCalledWith("m1");
    expect(b.find).not.toHaveBeenCalled();
    expect(document.querySelectorAll(".tmark-card").length).toBe(1);

    tb.unmount();
    ta.unmount();
  });
});
