// The last hop of the handoff-proposal lineage (ADR 0073 decision 1, amendment 2026-09-10).
//
// Everything before this was already in place: HandoffProposal stashes WHICH session and WHICH
// proposal seeded the launch, and StartHost badges the proposal from the same pair. What was
// missing is that the create request never carried it, so the Agent had nothing to record and
// the launched session came out with no lineage at all.
//
// Only api and the pane-opening are swapped out; the seed store is the real one, because "the
// dialog badges one proposal and the create names another" is precisely the disagreement this
// pins shut.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, apiJSON: (...a: unknown[]) => apiJSON(...a), pasteImage: vi.fn() };
});
vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));

const { useStartWork } = await import("./useStartWork.ts");
const { useLaunchSeed } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
let start: ReturnType<typeof useStartWork> | null = null;

function Probe() {
  start = useStartWork();
  return null;
}

beforeEach(async () => {
  apiJSON.mockReset();
  apiJSON.mockResolvedValue({ name: "s9abcde" });
  useLaunchSeed.getState().clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <Probe />
      </ToastProvider>,
    );
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  start = null;
  useLaunchSeed.getState().clear();
});

const launch = async () => {
  await act(async () => {
    await start!(
      { dir: "/repos/x", repo: "x" },
      {
        kind: "claude", driver: "", model: "", effort: "", startMode: "normal", prompt: "carry on",
        title: "Part 2", images: [], subdir: "", base: "", newBranch: "", worktree: false,
      },
    );
  });
};

/** The body of the POST /api/sessions the launch sent. */
const createBody = () => {
  const call = apiJSON.mock.calls.find((c) => c[0] === "api/sessions" && c[1] === "POST");
  return (call?.[2] ?? {}) as Record<string, unknown>;
};

describe("引き継ぎ提案から起こしたセッションの系譜", () => {
  it("提案元セッションと提案 ID を create に載せ、origin は user のまま（送らない）", async () => {
    useLaunchSeed.getState().set("carry on", "Part 2", "proposer", "hp_1234abcd");
    await launch();
    const body = createBody();
    expect(body.origin_session).toBe("proposer");
    expect(body.origin_proposal).toBe("hp_1234abcd");
    // The whole design rests on this: a person opened the session, so the accounting axis
    // (ADR 0029 §6) must not move. Sending an origin at all would move it.
    expect(body.origin).toBeUndefined();
  });

  it("提案由来でない起動には何も載せない", async () => {
    await launch();
    const body = createBody();
    expect(body.origin_session).toBeUndefined();
    expect(body.origin_proposal).toBeUndefined();
  });

  it("他メンバーからの引き継ぎ（提案元セッションが無い）にも載せない", async () => {
    // HandoffOfferRow seeds an offer id with an EMPTY session name — there is no local session
    // to be the origin of, and half a pair must not become a lineage claim.
    useLaunchSeed.getState().set("carry on", "Part 2", "", "", "offer_1");
    await launch();
    const body = createBody();
    expect(body.origin_session).toBeUndefined();
    expect(body.origin_proposal).toBeUndefined();
  });
});
