// The member's side of the CP dead-man check (#2518): the bar says the workspace is not
// responding, explains why a tap is worth it, and the action is the confirmed stop.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { AgentSilentBadge } from "./AgentSilentBadge.tsx";
import { setLocale } from "../../lib/i18n/index.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("AgentSilentBadge", () => {
  it("names the problem, tells the member to stop and start, and stops on the button", async () => {
    setLocale("en");
    const onStop = vi.fn(async () => {});
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<AgentSilentBadge onStop={onStop} />));
    expect(host.textContent).toContain("Not responding");
    expect(host.querySelector(".ws-stale-pop")).toBeNull();
    await act(async () => host!.querySelector<HTMLButtonElement>(".ws-stale-pill")!.click());
    expect(host.querySelector(".ws-stale-pop")?.textContent).toContain("stop the workspace, then start it again");
    await act(async () => host!.querySelector<HTMLButtonElement>(".ws-stale-go")!.click());
    expect(onStop).toHaveBeenCalledTimes(1);
  });
});
