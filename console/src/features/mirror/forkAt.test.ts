import { describe, it, expect } from "vitest";
import { canBranchFrom, canBranchInSession, carriedUserTurns } from "./forkAt.ts";
import type { BranchableTurn } from "./forkAt.ts";
import { agentOf } from "../../agents/registry.ts";

const user = (anchorId?: string, extra: Partial<BranchableTurn> = {}): BranchableTurn => ({
  role: "user",
  anchorId,
  ...extra,
});
const bot = (): BranchableTurn => ({ role: "assistant", anchorId: "msg_a" });

describe("canBranchFrom", () => {
  it("allows a landed user turn that carries an anchor", () => {
    expect(canBranchFrom(user("msg_1"))).toBe(true);
  });

  it("refuses an assistant turn", () => {
    // v1 forks in order to re-type a message, so an agent reply is never a fork point.
    expect(canBranchFrom(bot())).toBe(false);
  });

  it("refuses a turn with no anchor", () => {
    // Without an anchor the request goes out with an empty `at` and forks the whole conversation.
    expect(canBranchFrom(user(undefined))).toBe(false);
    expect(canBranchFrom(user(""))).toBe(false);
  });

  it("refuses an optimistic echo and a queued prompt", () => {
    // Neither is in the transcript yet, so there is no id to point at.
    expect(canBranchFrom(user("msg_1", { pending: true }))).toBe(false);
    expect(canBranchFrom(user("msg_1", { queued: true }))).toBe(false);
  });

  it("refuses a compaction summary", () => {
    expect(canBranchFrom(user("msg_1", { compact: true }))).toBe(false);
  });
});

describe("canBranchInSession", () => {
  const managedOnly = { forkAt: true, forkAtManagedOnly: true };
  const anyRoute = { forkAt: true, forkAtManagedOnly: false };

  it("hides the affordance for a kind that can't fork at a point", () => {
    expect(
      canBranchInSession({ forkAt: false, forkAtManagedOnly: true }, { managed: true, readOnly: false }),
    ).toBe(false);
  });

  it("hides it in read-only history", () => {
    expect(canBranchInSession(anyRoute, { managed: true, readOnly: true })).toBe(false);
  });

  it("requires managed only for the kinds whose fork point lives in the runtime API", () => {
    expect(canBranchInSession(managedOnly, { managed: true, readOnly: false })).toBe(true);
    // The CLI route has no argument for the fork point, so using it always ends in a 400.
    expect(canBranchInSession(managedOnly, { managed: false, readOnly: false })).toBe(false);
  });

  it("allows a TUI session for a kind that cuts its own transcript", () => {
    // claude has no managed driver; requiring managed here would hide the control forever.
    expect(canBranchInSession(anyRoute, { managed: false, readOnly: false })).toBe(true);
  });

  it("matches the registry: claude branches on the TUI route, opencode/codex do not", () => {
    expect(canBranchInSession(agentOf("claude").caps, { managed: false, readOnly: false })).toBe(true);
    expect(canBranchInSession(agentOf("opencode").caps, { managed: false, readOnly: false })).toBe(false);
    expect(canBranchInSession(agentOf("codex").caps, { managed: false, readOnly: false })).toBe(false);
    expect(canBranchInSession(agentOf("opencode").caps, { managed: true, readOnly: false })).toBe(true);
    expect(canBranchInSession(agentOf("codex").caps, { managed: true, readOnly: false })).toBe(true);
  });
});

describe("carriedUserTurns", () => {
  it("counts the user turns BEFORE the branch point, excluding it", () => {
    const u1 = user("msg_1");
    const u2 = user("msg_3");
    const u3 = user("msg_5");
    const groups = [u1, bot(), u2, bot(), u3];
    expect(carriedUserTurns(groups, u1)).toBe(0); // forking from the first message carries nothing
    expect(carriedUserTurns(groups, u2)).toBe(1);
    expect(carriedUserTurns(groups, u3)).toBe(2);
  });

  it("does not count compaction summaries as user turns", () => {
    const summary = user("msg_2", { compact: true });
    const u2 = user("msg_3");
    const groups = [user("msg_1"), summary, u2];
    expect(carriedUserTurns(groups, u2)).toBe(1);
  });

  it("returns 0 for a turn that is not in the list", () => {
    expect(carriedUserTurns([user("msg_1")], user("msg_9"))).toBe(0);
  });
});
