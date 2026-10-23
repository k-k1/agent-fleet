// DocView must opt its Markdown into work-item links, so a "#N" in a plan or document opens the
// ticket the way it does in chat and the mirror.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const mdProps: { workItemRefs?: boolean }[] = [];
vi.mock("./MarkdownView.tsx", () => ({
  MarkdownView: (props: { workItemRefs?: boolean }) => {
    mdProps.push(props);
    return <div data-surface="md" />;
  },
}));

const { DocView } = await import("./DocView.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  mdProps.length = 0;
});

describe("DocView", () => {
  it("renders its Markdown with work-item refs on", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<DocView title="Plan" content="see #12" />);
    });
    expect(mdProps.length).toBeGreaterThan(0);
    expect(mdProps.every((p) => p.workItemRefs === true)).toBe(true);
  });
});
