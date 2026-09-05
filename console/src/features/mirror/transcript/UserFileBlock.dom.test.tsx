// Pins how a shared-file card routes a click. Looking at a picture the agent shared is the
// common case and must not cost a pane, so an image card enlarges in the lightbox and only its
// corner button opens the pane; every other file keeps the whole card as the open-in-pane
// target. The two targets are separate <button>s in one card — nesting them would be invalid
// HTML that React renders anyway and the browser then reparents, so the structure is asserted
// here too.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";

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

const turnsWith = (...files: string[]): Turn[] => [
  { role: "assistant", idx: 1, parts: [{ kind: "userfile", files, caption: "できました" }] },
];

// The mirror's own capability set for this panel: a URL builder for the bytes, a pane and a
// lightbox. (The shared-session view has none of them and drops the panel entirely — that is
// TranscriptView.dom.test.tsx.)
function ownerCaps(sink: { opened: string[]; zoomed: string[] }): TranscriptCaps {
  return {
    agentName: "Claude",
    session: "s1",
    fileURL: (p: string) => `https://cp.example/api/fs/download?path=${encodeURIComponent(p)}`,
    openFile: (p: string) => sink.opened.push(p),
    openImage: (u: string) => sink.zoomed.push(u),
  };
}

const click = (el: Element | null) => act(() => void (el as HTMLElement).click());

describe("a shared image card", () => {
  it("enlarges on the card and opens the pane only from the corner button", () => {
    const sink = { opened: [] as string[], zoomed: [] as string[] };
    const el = render(turnsWith("out/shot.png"), ownerCaps(sink));

    const card = el.querySelector(".mt-file-item.image");
    expect(card).not.toBeNull();
    expect(card!.querySelector("button button")).toBeNull(); // the targets are siblings, not nested

    click(el.querySelector(".mt-file-zoom"));
    expect(sink.zoomed).toEqual(["https://cp.example/api/fs/download?path=out%2Fshot.png"]);
    expect(sink.opened).toEqual([]); // enlarging must not also open a pane

    click(el.querySelector(".mt-file-pane"));
    expect(sink.opened).toEqual(["out/shot.png"]);
  });

  it("falls back to the plain open-in-pane card when the thumbnail cannot load", () => {
    const sink = { opened: [] as string[], zoomed: [] as string[] };
    const el = render(turnsWith("out/shot.png"), ownerCaps(sink));

    // A path outside a servable root answers with an error, not bytes. With no picture on
    // screen there is nothing to enlarge, so the card must go back to opening the pane.
    const img = el.querySelector(".mt-file-thumb img")!;
    act(() => void img.dispatchEvent(new Event("error")));

    expect(el.querySelector(".mt-file-thumb")).toBeNull();
    expect(el.querySelector(".mt-file-zoom")).toBeNull();
    expect(el.querySelector(".mt-file-pane")).toBeNull();
    click(el.querySelector(".mt-file-item"));
    expect(sink.opened).toEqual(["out/shot.png"]);
    expect(sink.zoomed).toEqual([]);
  });

  it("keeps the whole card as the pane target for a file that is not an image", () => {
    const sink = { opened: [] as string[], zoomed: [] as string[] };
    const el = render(turnsWith("out/report.md"), ownerCaps(sink));

    expect(el.querySelector(".mt-file-thumb")).toBeNull();
    expect(el.querySelector(".mt-file-zoom")).toBeNull();
    click(el.querySelector(".mt-file-item"));
    expect(sink.opened).toEqual(["out/report.md"]);
    expect(sink.zoomed).toEqual([]);
  });

  it("keeps the whole card as the pane target when there is no lightbox to enlarge into", () => {
    const sink = { opened: [] as string[], zoomed: [] as string[] };
    const caps = { ...ownerCaps(sink), openImage: undefined };
    const el = render(turnsWith("out/shot.png"), caps);

    expect(el.querySelector(".mt-file-thumb")).not.toBeNull(); // the thumbnail still renders
    expect(el.querySelector(".mt-file-zoom")).toBeNull();
    click(el.querySelector(".mt-file-item"));
    expect(sink.opened).toEqual(["out/shot.png"]);
  });
});
