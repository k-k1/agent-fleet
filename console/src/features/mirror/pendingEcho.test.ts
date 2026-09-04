import { describe, expect, it } from "vitest";
import { echoLanded, echoNeedsResync, ECHO_RESYNC_MS } from "./pendingEcho.ts";

const notNoise = () => false;

describe("echoLanded", () => {
  it("a normal send resolves against the real turn that follows it", () => {
    expect(echoLanded({ text: "確認して", sinceIdx: 10 }, [{ role: "user", text: "確認して", idx: 11 }], notNoise)).toBe(true);
  });

  it("a managed Codex image marker resolves via the attachment path", () => {
    const path = "/home/dev/.cache/agent-fleet/pasted/sid/paste-1.png";
    const actual = `確認して <image name=[Image #1] path="${path}">`;
    // Even when app-server materializes the image in a separate response_item, an echo must
    // not stay Pending once the real turn is visible.
    expect(echoLanded({ text: "確認して", sinceIdx: 99, attachmentPaths: [path] }, [{ role: "user", text: actual, idx: 42 }], notNoise)).toBe(true);
  });

  it("does not resolve on a different attachment or an earlier turn with the same text", () => {
    expect(
      echoLanded(
        { text: "確認して", sinceIdx: 99, attachmentPaths: ["/pasted/sid/paste-new.png"] },
        [{ role: "user", text: "確認して <image path=\"/pasted/sid/paste-old.png\">", idx: 42 }],
        notNoise,
      ),
    ).toBe(false);
  });

  // A slash command is recorded not as the raw "/model opus" but as <command-name>…</command-name>,
  // which isNoise hides. Text matching would leave it Pending forever, so commandTurnName resolves it.
  const cmdNoise = (t: { text?: string }) => (t.text || "").replace(/^\s+/, "").startsWith("<command-name>");

  it("a slash command resolves against its <command-name> turn", () => {
    expect(
      echoLanded(
        { text: "/model opus", sinceIdx: 10 },
        [{ role: "user", text: "<command-name>/model</command-name><command-args>opus</command-args>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(true);
  });

  it("resolves by command name even when a sentence follows the slash-command line", () => {
    // A prompt whose first line is the command and whose real turn carries only <command-name>.
    expect(
      echoLanded(
        { text: "/model opus\n続けて", sinceIdx: 10 },
        [{ role: "user", text: "<command-name>/model</command-name><command-args>opus</command-args>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(true);
  });

  it("resolves against a skill-shaped command turn (<command-message> first)", () => {
    // A skill invocation reverses the tag order and puts <command-message> first (measured on
    // 2.1.215); assuming name-first leaves the echo Pending forever.
    expect(
      echoLanded(
        { text: "/scout", sinceIdx: 10 },
        [{ role: "user", text: "<command-message>scout</command-message>\n<command-name>/scout</command-name>", idx: 11 }],
        (t) => (t.text || "").replace(/^\s+/, "").startsWith("<command-message>"),
      ),
    ).toBe(true);
  });

  it("does not resolve against a command turn from before the send", () => {
    expect(
      echoLanded(
        { text: "/model opus", sinceIdx: 20 },
        [{ role: "user", text: "<command-name>/model</command-name>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(false);
  });

  it("does not resolve against a turn for a different command", () => {
    expect(
      echoLanded(
        { text: "/model opus", sinceIdx: 10 },
        [{ role: "user", text: "<command-name>/clear</command-name>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(false);
  });

  it("text starting with '/' with no real command turn falls back to the plain text match", () => {
    // Plain text that merely starts with "/" (not a command) must not be hidden by mistake.
    expect(echoLanded({ text: "/etc/hosts を見て", sinceIdx: 10 }, [], notNoise)).toBe(false);
  });

  // When codex managed refuses turn/start with an error (measured with the usage limit
  // exhausted), no turn is created at all and the user's prompt never reaches the rollout. The
  // synthetic error turn (driver.go managedEnrich) therefore also resolves the echo on the
  // assistant side.
  it("resolves against an assistant error turn after the send (turn/start refused, so there is no user turn)", () => {
    expect(
      echoLanded(
        { text: "続けて", sinceIdx: 10 },
        [{ role: "assistant", idx: 11, parts: [{ kind: "error" }] }],
        notNoise,
      ),
    ).toBe(true);
  });

  it("does not resolve against an error turn from before the send", () => {
    expect(
      echoLanded({ text: "続けて", sinceIdx: 20 }, [{ role: "assistant", idx: 11, parts: [{ kind: "error" }] }], notNoise),
    ).toBe(false);
  });

  it("does not resolve against an assistant turn whose parts are not errors", () => {
    expect(
      echoLanded({ text: "続けて", sinceIdx: 10 }, [{ role: "assistant", idx: 11, parts: [{ kind: "text" }] }], notNoise),
    ).toBe(false);
  });
});

describe("echoNeedsResync", () => {
  // When the real turn never reaches the client (e.g. the Agent counted a half-written line as
  // one line and moved the cursor past it), there is nothing to match the text against and the
  // echo stays Pending forever. After a fixed delay, ask for one full re-read to fill the hole so
  // the echo can resolve.
  const now = 1_000_000;

  it("does not re-read right after the send", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0, at: now - 1000 }, now)).toBe(false);
  });

  it("re-reads once the echo has been unresolved for the whole window", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0, at: now - ECHO_RESYNC_MS }, now)).toBe(true);
  });

  it("re-reads only once, not on every poll", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0, at: now - 60000, resyncedAt: now - 30000 }, now)).toBe(false);
  });

  it("an older echo with no send time is out of scope", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0 }, now)).toBe(false);
  });
});

describe("the first instruction at launch (launchSeed's display echo)", () => {
  // Delivery happens on the Agent side (create's initial_prompt / when_ready) and the mirror only
  // shows the text that was sent, so by the time the echo appears the real turn may already be in
  // the transcript (fast delivery, or the pane opened later). Taking the anchor from newestIdx()
  // then never satisfies idx > sinceIdx and the echo stays Pending until a reload - which showed
  // up as the same instruction appearing twice with one stuck Pending, because a pane is keyed by
  // cell, so the session can be swapped while the previous session's transcript is still held and
  // the anchor becomes another session's last idx. sinceIdx=-1 always resolves.
  it("resolves even against a real turn that was already there before the echo", () => {
    expect(echoLanded({ text: "検討して", sinceIdx: -1 }, [{ role: "user", text: "検討して", idx: 7 }], notNoise)).toBe(true);
  });

  it("cannot resolve when the anchor came from a previous session (the regression shape)", () => {
    expect(echoLanded({ text: "検討して", sinceIdx: 500 }, [{ role: "user", text: "検討して", idx: 7 }], notNoise)).toBe(false);
  });
});
