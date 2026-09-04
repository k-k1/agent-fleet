import { describe, expect, it } from "vitest";
import { displayName, remainingShort, resumeClock, stateInfo, stripLabelTag } from "./sessionview.ts";
import { t } from "./i18n/index.ts";

// The state-chip mapping. What is pinned here is that "auth expired" (docs/log/47 §4-8) gets a chip
// of its own: shown as idle, users keep sending to a session that cannot move; folded into the
// rate-limited chip, it reads as "wait and it will fix itself", which an expired login never does.
describe("stateInfo", () => {
  const claude = { kind: "claude", alive: true };

  it("auth expired gets a chip distinct from both idle and blocked", () => {
    const auth = stateInfo({ ...claude, state: "auth" });
    expect(auth.text).toBe(t("state.auth_expired"));
    expect(auth.text).not.toBe(t("state.idle"));
    expect(auth.text).not.toBe(t("state.blocked"));
    // An attention colour (the question family), not on (green = healthy).
    expect(auth.cls).toBe("question");
  });

  it("leaves the existing states unchanged", () => {
    expect(stateInfo({ ...claude, state: "idle" }).text).toBe(t("state.idle"));
    expect(stateInfo({ ...claude, state: "blocked" }).text).toBe(t("state.blocked"));
    expect(stateInfo({ ...claude, state: "working" }).text).toBe(t("state.working"));
  });

  it("a stopped session reads as stopped whatever its state says", () => {
    expect(stateInfo({ kind: "claude", alive: false, state: "auth" }).text).toBe(t("state.stopped"));
  });

  // Waiting for a usage limit to reset (docs/log/47 §4-9). The original complaint was that this
  // looked like idle, so neither "why it is stopped" nor "when it will move" was on screen.
  it("waiting for a limit reset is a chip of its own and shows the scheduled time", () => {
    const bare = stateInfo({ ...claude, state: "limited" });
    expect(bare.text).toBe(t("state.rate_limited"));
    expect(bare.text).not.toBe(t("state.idle"));
    // Also distinct from blocked, which needs the user to choose something in the pane first.
    expect(bare.text).not.toBe(t("state.blocked"));

    const at = new Date();
    at.setHours(at.getHours() + 1, 30, 0, 0);
    const timed = stateInfo({ ...claude, state: "limited", rateLimitResumeAt: at.toISOString() });
    expect(timed.text).toContain(resumeClock(at.toISOString()));
  });

  // Even a limit with no schedule (auto-resume off, or a per-model cap) can still say it is waiting.
  it("falls back to a chip without a time when the resume time is missing or unreadable", () => {
    expect(stateInfo({ ...claude, state: "limited", rateLimitResumeAt: "" }).text).toBe(t("state.rate_limited"));
    expect(stateInfo({ ...claude, state: "limited", rateLimitResumeAt: "not-a-time" }).text).toBe(
      t("state.rate_limited"),
    );
  });

  // It must not sit as loudly as an unanswered question; the limited class takes the bold back off.
  // The colour stays in the question family.
  it("a limit wait borrows the question colour but adds the limited class", () => {
    const cls = stateInfo({ ...claude, state: "limited" }).cls;
    expect(cls).toContain("question");
    expect(cls).toContain("limited");
  });
});

// Spend and balance limits (docs/log/47 §4-10). They arrive as the same 429, so confusing the two
// leaves the user waiting for a reset that never comes.
describe("stateInfo (balance and spend limits)", () => {
  const claude = { kind: "claude", alive: true };

  it("gets a chip distinct from both the rate-limit wait and idle", () => {
    const spend = stateInfo({ ...claude, state: "spend_limit" });
    expect(spend.text).toBe(t("state.spend_limit"));
    expect(spend.text).not.toBe(t("state.rate_limited"));
    expect(spend.text).not.toBe(t("state.idle"));
    // A person has to act now (raise the cap), so it takes the question family's attention colour
    // rather than the calmer limited look.
    expect(spend.cls).toBe("question");
  });

  it("shows no resume time, because there is no schedule", () => {
    const at = new Date(Date.now() + 3600_000).toISOString();
    expect(stateInfo({ ...claude, state: "spend_limit", rateLimitResumeAt: at }).text).toBe(t("state.spend_limit"));
  });
});

// Build every date with the local-time constructor: a fixed-offset string would be rendered in the
// runner's own TZ and the test would fail anywhere outside JST.
describe("resumeClock", () => {
  const now = new Date(2026, 7, 19, 21, 0, 0);

  it("shows the time alone when it lands on the same day", () => {
    expect(resumeClock(new Date(2026, 7, 19, 23, 50, 0).toISOString(), now)).toBe("23:50");
  });

  // A weekly window can land days away; a bare time would read as "a few minutes from now".
  it("adds the date when it lands on another day", () => {
    expect(resumeClock(new Date(2026, 7, 21, 7, 15, 0).toISOString(), now)).toBe("08/21 07:15");
  });

  it("returns an empty string for an empty or broken value", () => {
    expect(resumeClock(undefined, now)).toBe("");
    expect(resumeClock("", now)).toBe("");
    expect(resumeClock("nonsense", now)).toBe("");
  });
});

// Name what is running in the background. With only a generic "running in background", the user
// cannot tell five minutes of a subagent writing a long answer from an idle session doing nothing.
describe("stateInfo (reason for background activity)", () => {
  const idleBg = { kind: "claude", alive: true, state: "idle", backgroundBusy: true };

  it("varies the wording per reason", () => {
    expect(stateInfo({ ...idleBg, backgroundBusyReason: "subagent" }).text).toBe(t("state.idle_bg_subagent"));
    expect(stateInfo({ ...idleBg, backgroundBusyReason: "shell" }).text).toBe(t("state.idle_bg_shell"));
  });

  // The reason only picks the wording. An unknown value, an older Agent or a relay that dropped it
  // must never cost the badge itself and fall back to idle; that was the original bug.
  it("still lights up with the generic wording when the reason is missing or unknown", () => {
    expect(stateInfo(idleBg).text).toBe(t("state.idle_bg"));
    expect(stateInfo({ ...idleBg, backgroundBusyReason: "" }).text).toBe(t("state.idle_bg"));
    expect(stateInfo({ ...idleBg, backgroundBusyReason: "process" }).text).toBe(t("state.idle_bg"));
    expect(stateInfo({ ...idleBg, backgroundBusyReason: "なにか新しい種別" }).text).toBe(t("state.idle_bg"));
  });

  it("ignores the reason when the background flag is not set", () => {
    expect(stateInfo({ ...idleBg, backgroundBusy: false, backgroundBusyReason: "subagent" }).text).toBe(
      t("state.idle"),
    );
  });
});

// A stopped session must still say that a conversation is waiting for an answer (docs/log/75
// §75.6.5). Now that sessions waiting on a person can be folded away too, rounding everything down
// to the single word "stopped" makes an unanswered question disappear silently.
describe("stateInfo (carried-over state while stopped)", () => {
  const dead = { kind: "claude", alive: false };

  it("varies the badge per kind of carried-over state", () => {
    expect(stateInfo({ ...dead, carried: "question" }).text).toBe(t("state.stopped_question"));
    expect(stateInfo({ ...dead, carried: "plan" }).text).toBe(t("state.stopped_plan"));
    expect(stateInfo({ ...dead, carried: "permission" }).text).toBe(t("state.stopped_permission"));
  });

  it("keeps reading as stopped: off is retained and the attention colour is added on top", () => {
    const chip = stateInfo({ ...dead, carried: "question" });
    expect(chip.cls).toContain("off");
    expect(chip.cls).toContain("question");
  });

  it("is plain stopped when nothing was carried over", () => {
    expect(stateInfo(dead).text).toBe(t("state.stopped"));
    expect(stateInfo({ ...dead, carried: "" }).text).toBe(t("state.stopped"));
  });

  // On an abnormal exit, why it died comes first; a carried-over state must not hide that warning.
  it("shows the crash reason in preference to a carried-over state", () => {
    expect(stateInfo({ ...dead, carried: "question", exitReason: "oom" }).text).toBe(t("exit.oom.text"));
  });

  // For a live row, state already describes the modal currently on screen; do not show it twice.
  it("shows no carried-over state on a live row", () => {
    expect(stateInfo({ kind: "claude", alive: true, state: "idle", carried: "question" }).text).toBe(t("state.idle"));
  });
});

// Time left on a keep-alive pin (docs/log/75). The point is that an expired pin must not stay on
// the badge: left there, the user believes the session is protected and leaves it, and the next
// sweep folds it away.
describe("remainingShort", () => {
  const now = new Date(2026, 7, 24, 12, 0, 0);

  it("renders the remaining time in a human-readable form", () => {
    expect(remainingShort(new Date(2026, 7, 24, 12, 30, 0).toISOString(), now)).toBe("30m");
    expect(remainingShort(new Date(2026, 7, 24, 16, 0, 0).toISOString(), now)).toBe("4h");
    expect(remainingShort(new Date(2026, 7, 24, 14, 15, 0).toISOString(), now)).toBe("2h15m");
  });

  it("is empty when the pin has expired, was never set, or is broken", () => {
    expect(remainingShort(new Date(2026, 7, 24, 11, 59, 0).toISOString(), now)).toBe("");
    expect(remainingShort(undefined, now)).toBe("");
    expect(remainingShort("", now)).toBe("");
    expect(remainingShort("いつまでも", now)).toBe("");
  });
});

// The label's leading tag (workspace/agent/internal/session/label.go). The point is that the old
// and new forms appear on screen side by side: the label is baked into meta at creation, so after
// the switch to a session-name tag, existing sessions keep the old `[AF] `. Stripping only one form
// leaves the tag visible on those rows.
describe("stripLabelTag", () => {
  it("strips both the old and the new tag", () => {
    expect(stripLabelTag("[AF:s6bbilu] 94-freeze 試走2本")).toBe("94-freeze 試走2本");
    expect(stripLabelTag("[AF] 旧形式のまま残ったラベル")).toBe("旧形式のまま残ったラベル");
  });

  it("leaves a string that is not an AF tag alone", () => {
    expect(stripLabelTag("どこか他所で付いた --name")).toBe("どこか他所で付いた --name");
    // The character set of the name part follows the Agent's ValidName. Loosening it risks eating
    // part of the user's title as if it were a session name.
    expect(stripLabelTag("[AF:日本語] t")).toBe("[AF:日本語] t");
  });

  it("displayName falls back to the tag-stripped label when there is no title", () => {
    const s = { name: "s6bbilu", kind: "claude", label: "[AF:s6bbilu] agent-fleet @0831-1922" };
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(displayName(s as any)).toBe("agent-fleet @0831-1922");
  });
});
