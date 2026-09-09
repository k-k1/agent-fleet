// Render test for the rail row's state chip. The rail is narrow, so every calm state collapses
// to an icon and only the states that want the user keep their text — which makes "does this
// chip show its text" a real behaviour, not styling: a handoff nobody has launched is the next
// step of the work, and as a bare icon in a list of bare icons it is not findable.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { SessionRow } = await import("./SessionRow.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;

let root: Root | null = null;
let host: HTMLDivElement;

const render = async (over: Partial<Session>): Promise<void> => {
  const s: Session = { name: "s1", kind: "claude", alive: true, state: "idle", title: "作業", ...over };
  await act(async () => {
    root!.render(
      <ul>
        <SessionRow s={s} selected={false} opens={[]} multi={false} running readOnly />
      </ul>,
    );
  });
};

const chip = () => host.querySelector<HTMLElement>(".session-state");

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("SessionRow state chip", () => {
  it("keeps waiting-for-input to an icon", async () => {
    await render({});
    expect(chip()?.className).toContain("mini");
    expect(chip()?.textContent?.trim()).toBe("");
  });

  it("shows the short wording when a handoff is waiting to be launched", async () => {
    await render({ handoffPending: true });
    expect(chip()?.className).not.toContain("mini");
    expect(chip()?.textContent?.trim()).toBe(t("state.handoff_short"));
    // The full wording — which run state, and what is waiting — stays on the tooltip, where
    // there is room for it.
    expect(chip()?.title).toBe(t("state.idle_handoff"));
    expect(host.querySelector<HTMLElement>(".sess-btn")?.title).toContain(t("state.idle_handoff"));
  });

  it("shows it on a stopped row too", async () => {
    await render({ alive: false, handoffPending: true });
    expect(chip()?.textContent?.trim()).toBe(t("state.handoff_short"));
    expect(chip()?.title).toBe(t("state.stopped_handoff"));
  });

  // The states that were already loud must keep their own full text, not a short form they
  // never asked for.
  it("leaves a question showing its own wording", async () => {
    await render({ state: "question" });
    expect(chip()?.textContent?.trim()).toBe(t("state.question"));
  });
});
