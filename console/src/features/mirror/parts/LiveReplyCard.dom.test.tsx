// The reply claude is still writing (#1250). It is a transient block: the Agent stops sending it
// once the real turn lands, so what matters here is that it reads as the agent's reply (same turn
// shell as the other pending cards, Markdown rendered) and offers nothing to act on.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { LiveReplyCard } from "./pendingCards.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(text: string) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => {
    root!.render(
      <ToastProvider>
        <LiveReplyCard agentName="Claude" text={text} repo={null} onOpenFile={vi.fn()} />
      </ToastProvider>,
    );
  });
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("LiveReplyCard", () => {
  it("renders the streamed text as the agent's turn, as Markdown", () => {
    const el = mount("1. **first**\n2. second");
    const turn = el.querySelector(".mirror-turn.assistant");
    expect(turn).not.toBeNull();
    expect(turn!.querySelector(".mt-who")?.textContent).toBe("Claude");
    expect(turn!.querySelectorAll("li")).toHaveLength(2);
    expect(turn!.querySelector("strong")?.textContent).toBe("first");
  });

  it("carries no controls of its own", () => {
    const el = mount("still writing");
    expect(el.querySelectorAll("button")).toHaveLength(0);
  });
});
