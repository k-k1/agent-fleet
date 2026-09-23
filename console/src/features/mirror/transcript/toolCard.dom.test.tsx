// Pins TranscriptCaps.toolCard: a host's card replaces the one call's faint trace, the calls
// around it stay a foldable run, and a folded turn still shows the card (the image studio's
// set_image_draft, ADR 0100 decision 9 — a card inside the closed "work" summary is a card the
// member never sees).
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(turns: Turn[], caps: TranscriptCaps, props: { working?: boolean; autoCollapseWork?: boolean } = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ToastProvider>
        <TranscriptView groups={groupTurns(turns)} caps={caps} {...props} />
      </ToastProvider>,
    ),
  );
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const TURN: Turn[] = [
  {
    role: "assistant",
    idx: 1,
    ts: "2026-09-24T01:00:00Z",
    parts: [
      { kind: "tool", tool: "mcp__af_1a2b3c4d__get_image_studio" },
      { kind: "tool", tool: "mcp__af_1a2b3c4d__set_image_draft" },
      { kind: "tool", tool: "Read", info: "notes.md" },
      { kind: "tool", tool: "mcp__af_1a2b3c4d__set_image_draft" },
      { kind: "text", text: "cfg を下げました。" },
    ],
  },
];

const seen: number[] = [];
const caps: TranscriptCaps = {
  agentName: "Claude",
  session: "s1",
  toolCard: (p, _turn, nth) => {
    if (!p.tool?.endsWith("set_image_draft")) return null;
    seen.push(nth);
    return <div className="test-card">card {nth}</div>;
  },
};

describe("a host's tool card", () => {
  it("stands in for its call, counting the calls of that tool", () => {
    seen.length = 0;
    // A live turn: nothing is folded, the cards sit where the calls were.
    const el = render(TURN, caps, { working: true });
    expect(el.querySelector(".mt-work")).toBeNull();
    expect([...el.querySelectorAll(".test-card")].map((c) => c.textContent)).toEqual(["card 0", "card 1"]);
    expect(seen).toEqual([0, 1]);
    // The other tools stay traces; none of them is the carded tool.
    const names = [...el.querySelectorAll(".mt-tool-name")].map((n) => n.textContent);
    expect(names.some((n) => n?.includes("set_image_draft"))).toBe(false);
  });

  it("is lifted out of a folded work trace", () => {
    const el = render(TURN, caps, { working: false, autoCollapseWork: true });
    const fold = el.querySelector(".mt-work");
    expect(fold).not.toBeNull();
    const cards = [...el.querySelectorAll(".test-card")];
    expect(cards.map((c) => c.textContent)).toEqual(["card 0", "card 1"]);
    for (const c of cards) expect(fold!.contains(c)).toBe(false);
  });
});
