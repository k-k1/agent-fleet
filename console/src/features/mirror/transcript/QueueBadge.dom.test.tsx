// A user turn whose source is "queue" (a memo category's auto-run, ADR 0137) gets its own
// badge: nobody typed it, so it must not read as the member's own input.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

import { TranscriptView, resetWatchStarts } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";
import { t } from "../../../lib/i18n/index.ts";
import { originLabel } from "../parts/DiscardNotice.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(turns: Turn[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={{} as TranscriptCaps} working={false} />));
  return host;
}
afterEach(() => {
  act(() => root?.unmount());
  resetWatchStarts();
  host?.remove();
  root = null;
  host = null;
});

const turn = (source?: string): Turn[] => [
  { role: "user", text: "do the memos", idx: 1, anchorId: "u1", ts: "2026-11-14T10:00:00Z", source },
  { role: "assistant", idx: 2, anchorId: "a1", ts: "2026-11-14T10:01:00Z", parts: [{ kind: "text", text: "ok" }] },
];

describe("queue turn badge", () => {
  it("badges a turn whose source is queue", () => {
    const el = render(turn("queue"));
    const badge = el.querySelector(".mirror-turn.user .mt-op");
    expect(badge?.textContent).toContain(t("mirror.from_queue"));
    expect(badge?.getAttribute("title")).toBe(t("mirror.from_queue_title"));
  });
  it("badges nothing on the member's own turn", () => {
    expect(render(turn()).querySelector(".mirror-turn.user .mt-op")).toBeNull();
  });
  it("names the origin of a discarded queue input in the same words", () => {
    expect(originLabel({ kind: "queue" })).toBe(t("mirror.from_queue"));
  });
});
