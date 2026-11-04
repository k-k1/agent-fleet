// Pins TranscriptCaps.toolNote and a shell call's whole command: the host's note (the cloud runs a
// call started) sits under that call's trace inside the work fold, and a trace whose label was cut
// opens to the command it ran.
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

function render(turns: Turn[], caps: TranscriptCaps) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ToastProvider>
        <TranscriptView groups={groupTurns(turns)} caps={caps} working />
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

const LONG = "af-aws-exec --profile prod --account 123456789012 -- bash -c 'aws s3 ls; aws sts get-caller-identity'";
const TURNS: Turn[] = [
  { role: "user", idx: 0, ts: "2026-10-30T12:00:00Z", text: "go" },
  { role: "assistant", idx: 1, ts: "2026-10-30T12:00:01Z", parts: [{ kind: "tool", tool: "Bash", info: "ls" }] },
  { role: "assistant", idx: 2, ts: "2026-10-30T12:00:02Z", parts: [{ kind: "tool", tool: "Bash", info: LONG.slice(0, 80) + "…", command: LONG }] },
];

describe("toolNote and the whole command", () => {
  it("keeps each row's time per part, and draws the note under the call it names", () => {
    const g = groupTurns(TURNS);
    expect(g[1].partTs).toEqual(["2026-10-30T12:00:01Z", "2026-10-30T12:00:02Z"]);
    const seen: Array<[number | undefined, number]> = [];
    const el = render(TURNS, {
      agentName: "Claude",
      session: "s1",
      toolNote: (turn, i) => {
        seen.push([turn.idx, i]);
        return i === 1 ? <div className="test-note">2 runs</div> : null;
      },
    });
    // Two calls fold into one run; the note lives inside it.
    expect(el.querySelector(".test-note")).toBeNull();
    act(() => (el.querySelector(".mt-toolrun-head") as HTMLElement).click());
    const noted = el.querySelector(".mt-tool-noted")!;
    expect(noted.querySelector(".test-note")).not.toBeNull();
    expect(noted.textContent).toContain("af-aws-exec");
    expect(seen).toContainEqual([1, 0]);
    expect(seen).toContainEqual([1, 1]);
    expect(el.querySelectorAll(".mt-tool-noted")).toHaveLength(1);
  });

  it("opens a cut label to the whole command", () => {
    const el = render(TURNS, { agentName: "Claude", session: "s1" });
    act(() => (el.querySelector(".mt-toolrun-head") as HTMLElement).click());
    expect(el.querySelector(".mt-tool-command")).toBeNull();
    const heads = el.querySelectorAll(".mt-toolrun-body .mt-tool-outhead");
    expect(heads).toHaveLength(1);
    act(() => (heads[0] as HTMLElement).click());
    expect(el.querySelector(".mt-tool-command")?.textContent).toBe(LONG);
  });
});
