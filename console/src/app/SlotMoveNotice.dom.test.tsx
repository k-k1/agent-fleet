// The member's side of a slot replacement reservation (#1473): the WS bar says the next start
// moves the workspace to a new slot and takes longer, and explains it on a tap (a title alone
// is invisible on a phone).
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SlotMoveNotice } from "./WsBar.tsx";

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

describe("SlotMoveNotice", () => {
  it("names the move and explains it, files included, when tapped", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<SlotMoveNotice />));
    expect(host.textContent).toContain("Moves to a new slot");
    expect(host.querySelector(".ws-stale-pop")).toBeNull();
    await act(async () => host!.querySelector<HTMLButtonElement>(".ws-stale-pill")!.click());
    const pop = host.querySelector(".ws-stale-pop")?.textContent || "";
    expect(pop).toContain("The next start moves it to a new one");
    expect(pop).toContain("Files, repos and logins come along unchanged");
  });
});
