// The reviewer's first prompt. Three things are pinned, each of which fails quietly:
//
// 1. The plan BODY must not be in it. Real plans are 12-36 KB and a TUI launch types its
//    first prompt into the pane; the path is the whole point of the endpoint behind it.
// 2. The answer format has to be asked for, or the findings come back as free prose and the
//    planner has nothing to work through.
// 3. The return address has to be the planning session, by name. The findings travel as a
//    peer message, and a reviewer that does not know where to send them just stops.
import { describe, it, expect, afterEach } from "vitest";
import { reviewPrompt, reviewTitle } from "./planReview.ts";
import { getLocale, setLocale } from "../../lib/i18n/index.ts";
import { SESSION_TITLE_MAX } from "../../lib/sessionTitle.ts";

const locale = getLocale();
afterEach(() => setLocale(locale)); // setLocale is module-global — put it back

const PLAN_BODY = "## やること\n\n巨大な本文がここに 12-36 KB 続く";

describe("reviewPrompt", () => {
  it("hands over the path and never the plan body", () => {
    const p = reviewPrompt({
      parent: "planner-x",
      path: "/var/lib/af/claude/plans/eager-twirling-frost.md",
      dir: "/home/dev/repos/app",
    });
    expect(p).toContain("/var/lib/af/claude/plans/eager-twirling-frost.md");
    expect(p).toContain("/home/dev/repos/app");
    expect(p).not.toContain(PLAN_BODY);
  });

  it("asks for a verdict and findings, and names the session to reply to", () => {
    const p = reviewPrompt({ parent: "planner-x", path: "/p/plan.md", dir: "/d" });
    expect(p).toMatch(/^## .+$/m); // the two headings it must answer under
    expect(p).toContain("approve | changes-requested");
    expect(p).toContain("send_to_peer_session");
    expect(p).toContain("af_stop_after_turn");
    // Named twice (the brief and the reply instruction) — both matter, so count rather
    // than merely assert presence.
    expect(p.split("planner-x").length - 1).toBeGreaterThanOrEqual(2);
  });

  // A missing key falls back to the default catalogue, so an English reviewer would be
  // briefed in Japanese and nothing would fail. Pin the whole prompt to one language by
  // asserting that no CJK is left in it.
  it("is fully translated in en", () => {
    setLocale("en");
    const p = reviewPrompt({ parent: "s", path: "/p/plan.md", dir: "/d", focus: "the migration order" });
    expect(p).not.toMatch(/[぀-ヿ一-鿿]/);
    expect(p).toContain("Verdict");
    expect(p).toContain("Findings");
    expect(reviewTitle("Migration")).not.toMatch(/[぀-ヿ一-鿿]/);
  });

  it("carries the focus line only when one was typed", () => {
    const base = reviewPrompt({ parent: "s", path: "/p/plan.md", dir: "/d" });
    const withFocus = reviewPrompt({ parent: "s", path: "/p/plan.md", dir: "/d", focus: "移行手順の可逆性" });
    const blank = reviewPrompt({ parent: "s", path: "/p/plan.md", dir: "/d", focus: "   " });
    expect(withFocus).toContain("移行手順の可逆性");
    expect(withFocus.split("\n").length).toBe(base.split("\n").length + 1);
    expect(blank).toBe(base);
  });
});

describe("reviewTitle", () => {
  it("names the plan and stays within the session title limit", () => {
    expect(reviewTitle("移行計画")).toContain("移行計画");
    expect(reviewTitle("x".repeat(SESSION_TITLE_MAX * 2)).length).toBeLessThanOrEqual(SESSION_TITLE_MAX);
  });
});
