// Changing the time zone setting must repaint the times of turns that are already on screen:
// TranscriptTurn is memo()'d and none of its props change (#2185).
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import { setFormatPrefs } from "../../../lib/intl.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setFormatPrefs("auto", "auto");
});

describe("TranscriptTurn time zone", () => {
  it("repaints an existing turn's time when the zone setting changes", () => {
    const caps: TranscriptCaps = { agentName: "Claude", session: "s1" };
    const turns: Turn[] = [
      { role: "user", idx: 1, ts: "2026-03-08T10:30:00Z", text: "hi" },
      { role: "assistant", idx: 2, ts: "2026-03-08T10:30:00Z", parts: [{ kind: "text", text: "done" }] },
    ];
    const groups = groupTurns(turns); // one array: the props stay identical across the change
    setFormatPrefs("auto", "Asia/Tokyo");
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<TranscriptView groups={groups} caps={caps} working={false} autoCollapseWork={false} />));
    const times = () => [...host!.querySelectorAll(".mt-time")].map((e) => e.textContent ?? "");
    expect(times().join()).toContain("19:30");
    act(() => setFormatPrefs("auto", "America/Los_Angeles"));
    expect(times().join()).toContain("03:30"); // 10:30Z is 03:30 PDT, after the US DST jump
    expect(times().join()).not.toContain("19:30");
  });
});
