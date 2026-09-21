// The schedule editor's AGENT PICKER, which had no test at all while it was a hand-kept array
// of six inside the component — and that is how it lost agy, a kind whose scheduled runs ship
// and which could therefore not be chosen in the Console at all (ADR 0095 P2-13's closing note,
// fixed in P2-14).
//
// A unit test over `scheduledKinds` alone would not have caught it either: the list can be right
// while the component ignores it. So this renders the real modal and reads the real <select>.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./api.ts", () => ({ scheduleUpdate: vi.fn() }));

const { ScheduleDetailModal } = await import("./ScheduleDetailModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

const base = { id: "sch_1", spec_kind: "cron", spec: "0 9 * * *", prompt: "hi", enabled: true };

async function render(agentKind?: string): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ScheduleDetailModal
          s={{ ...base, agent_kind: agentKind } as never}
          onClose={() => {}}
          onSaved={() => {}}
        />
      </ToastProvider>,
    );
  });
}

// The modal renders into a portal, so the options are read off the document rather than off the
// host element the root was created on.
const agentOptions = (): string[] =>
  Array.from(document.querySelectorAll("#sched-agent option")).map((o) => o.textContent || "");

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("ScheduleDetailModal agent picker", () => {
  it("offers agy, the kind the hand-kept list had lost", async () => {
    await render("claude");
    expect(agentOptions()).toContain("agy");
  });

  it("offers the kinds whose scheduled runs are verified, and no terminal kinds", async () => {
    await render("claude");
    const opts = agentOptions();
    for (const k of ["claude", "codex", "opencode", "copilot", "cursor", "kiro", "agy", "muse"]) {
      expect(opts).toContain(k);
    }
    // shell / ssm run what you send verbatim and have no conversation to schedule into; lcpp is
    // an agent kind whose scheduled runs nobody has watched work end to end, which is why the
    // guide's capability table leaves that row blank.
    for (const k of ["shell", "ssm", "lcpp"]) {
      expect(opts).not.toContain(k);
    }
  });

  it("keeps an existing schedule's own kind selectable even when the picker would not offer it", async () => {
    // The rule that stops an edit of one field from silently rewriting another: a schedule
    // already pointed at lcpp must not be re-pointed at claude just because the member opened
    // the modal to change its label.
    await render("lcpp");
    const opts = agentOptions();
    expect(opts[0]).toBe("lcpp");
    expect(opts.filter((k) => k === "lcpp")).toHaveLength(1);
    expect((document.querySelector("#sched-agent") as HTMLSelectElement).value).toBe("lcpp");
  });
});
