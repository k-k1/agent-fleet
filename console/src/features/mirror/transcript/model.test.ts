import { describe, expect, it } from "vitest";
import {
  coalesceUserActions,
  foldParts,
  groupTurns,
  isNoise,
  latestContext,
  mergeTurns,
  parseCommand,
  peerIntentOf,
  peerSenderOf,
} from "./model.ts";
import type { Turn } from "./types.ts";

// This pipeline is the one render path both the mirror and the shared-session view go through,
// so a break here surfaces as the owner and the recipient seeing different conversations.

const user = (text: string, extra: Partial<Turn> = {}): Turn => ({ role: "user", text, ...extra });
const asst = (text: string, extra: Partial<Turn> = {}): Turn => ({ role: "assistant", text, ...extra });

describe("isNoise", () => {
  it("drops system-injected user rows and keeps real utterances", () => {
    expect(isNoise(user("<system-reminder>foo"))).toBe(true);
    expect(isNoise(user("  <bash-input>ls</bash-input>"))).toBe(true); // leading whitespace is ignored
    expect(isNoise(user("[Request interrupted by user for tool use]"))).toBe(true);
    expect(isNoise(user("これは普通の指示です"))).toBe(false);
  });

  it("never treats anything on the assistant side as noise", () => {
    expect(isNoise(asst("<system-reminder>これは本文の一部"))).toBe(false);
  });
});

describe("coalesceUserActions", () => {
  it("turns a `!` run into a bash block, absorbing its paired output turn", () => {
    const out = coalesceUserActions([
      user("<bash-input>ls -la</bash-input>"),
      user("<bash-stdout>a.txt\nb.txt</bash-stdout><bash-stderr>warn</bash-stderr>"),
      asst("見ました"),
    ]);
    expect(out).toHaveLength(2); // the output turn has been consumed
    expect(out[0].bash).toBe(true);
    expect(out[0].text).toBe("$ ls -la"); // plain text, for copying
    expect(out[0].parts).toEqual([{ kind: "bash", text: "ls -la", output: "a.txt\nb.txt", stderr: "warn" }]);
  });

  it("keeps a `!` run as a bash block even with no output turn", () => {
    const out = coalesceUserActions([user("<bash-input>pwd</bash-input>")]);
    expect(out).toHaveLength(1);
    expect(out[0].parts?.[0]).toEqual({ kind: "bash", text: "pwd", output: "", stderr: "" });
  });

  it("turns a `/` run into a cmd chip", () => {
    const out = coalesceUserActions([user("<command-name>/scout</command-name><command-args>--deep</command-args>")]);
    expect(out[0].cmd).toBe(true);
    expect(out[0].parts).toEqual([{ kind: "cmd", text: "/scout", info: "--deep" }]);
  });

  it("reads the name whatever the tag order, as a skill invocation logs it", () => {
    // Requiring the name first left this shape unparseable: isNoise dropped the turn whole, the
    // user-turn boundary went with it, and every following reply merged into one block.
    const t = user("<command-message>scout is running…</command-message><command-name>/scout</command-name>");
    expect(parseCommand(t)).toEqual({ name: "/scout", args: "" });
    expect(coalesceUserActions([t])[0].cmd).toBe(true);
  });
});

describe("groupTurns", () => {
  it("folds consecutive same-role turns into one block, merging text and tokens", () => {
    const groups = groupTurns([
      asst("前半", { ts: "2026-08-13T10:00:00Z", outTok: 10, idx: 1 }),
      asst("後半", { ts: "2026-08-13T10:05:00Z", outTok: 5, inTok: 100, cacheRead: 20 }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].text).toBe("前半\n\n後半");
    expect(groups[0].outTok).toBe(15); // output tokens are summed
    expect(groups[0].inTok).toBe(100); // input/cache take the last value
    expect(groups[0].ts).toBe("2026-08-13T10:00:00Z"); // ordering uses the start
    expect(groups[0].endTs).toBe("2026-08-13T10:05:00Z"); // the footer uses the end
  });

  it("starts a new block whenever the role changes", () => {
    expect(groupTurns([user("お願い"), asst("はい"), user("もう一度")])).toHaveLength(3);
  });

  it("keeps compact/bash/cmd blocks standalone, never merged with a neighbour", () => {
    for (const flag of ["compact", "bash", "cmd"] as const) {
      const groups = groupTurns([
        asst("前", { parts: [{ kind: "text", text: "前" }] }),
        asst("特殊", { [flag]: true, parts: [{ kind: "text", text: "特殊" }] }),
        asst("後", { parts: [{ kind: "text", text: "後" }] }),
      ]);
      expect(groups.map((g) => g.text)).toEqual(["前", "特殊", "後"]);
    }
  });

  it("drops noise and sidechain turns (delegation is shown as a card instead)", () => {
    const groups = groupTurns([user("<system-reminder>x"), asst("子の作業", { sidechain: true }), asst("答え")]);
    expect(groups).toHaveLength(1);
    expect(groups[0].text).toBe("答え");
  });

  it("synthesises one part from the text of an old Agent turn that has none", () => {
    expect(groupTurns([asst("本文だけ")])[0].parts).toEqual([{ kind: "text", text: "本文だけ" }]);
  });

  it("keeps a source such as operator across a same-role merge", () => {
    const groups = groupTurns([user("一言目"), user("二言目", { source: "operator" })]);
    expect(groups[0].source).toBe("operator");
  });
});

describe("foldParts", () => {
  it("groups consecutive tools into one toolrun and passes everything else through", () => {
    const items = foldParts([
      { kind: "text", text: "始めます" },
      { kind: "tool", tool: "Edit" },
      { kind: "tool", tool: "Edit" },
      { kind: "text", text: "できました" },
      { kind: "tool", tool: "Bash" },
    ]);
    expect(items.map((i) => i.kind)).toEqual(["part", "toolrun", "part", "toolrun"]);
    expect(items[1].kind === "toolrun" && items[1].tools).toHaveLength(2);
    // A lone tool becomes a toolrun too, so the caller only ever branches two ways.
    expect(items[3].kind === "toolrun" && items[3].tools).toHaveLength(1);
  });

  it("preserves the original ordering as an index", () => {
    const items = foldParts([{ kind: "tool" }, { kind: "text", text: "x" }]);
    expect(items[0].kind === "toolrun" && items[0].tools[0].i).toBe(0);
    expect(items[1].kind === "part" && items[1].i).toBe(1);
  });
});

describe("latestContext", () => {
  it("returns the newest assistant block that carries usage", () => {
    const groups = groupTurns([
      asst("古い", { inTok: 10, cacheRead: 1, ctxWindow: 200000, model: "old" }),
      user("次"),
      asst("新しい", { inTok: 50, cacheRead: 5, cacheCreate: 2, ctxWindow: 200000, model: "new" }),
    ]);
    expect(latestContext(groups)).toMatchObject({ fresh: 50, read: 5, create: 2, model: "new" });
  });

  it("is null when nothing recorded usage", () => {
    expect(latestContext(groupTurns([asst("記録なし")]))).toBeNull();
  });
});

describe("peerSenderOf", () => {
  it("reads the sending session name out of a peer envelope", () => {
    expect(peerSenderOf("[agent-fleet:peer from=build-api] お願い")).toBe("build-api");
    expect(peerSenderOf("ふつうの発話")).toBeNull();
  });

  it("still reads the name when the envelope grows more words", () => {
    // Pinning "]" straight after the name silently degraded the badge to the unnamed one the
    // moment intent/reply were added (docs/log/58 §58.14). This shape survives further additions.
    expect(
      peerSenderOf("[agent-fleet:peer from=build-api intent=request reply=only-if-blocked] 直して"),
    ).toBe("build-api");
  });
});

describe("peerIntentOf", () => {
  it("reads the kind; unknown values and non-peer text give null", () => {
    expect(peerIntentOf("[agent-fleet:peer from=a intent=notice reply=none] 出た")).toBe("notice");
    expect(peerIntentOf("[agent-fleet:peer from=a intent=request reply=only-if-blocked] 直して")).toBe(
      "request",
    );
    expect(peerIntentOf("[agent-fleet:peer from=a] 旧い封筒")).toBeNull();
    expect(peerIntentOf("[agent-fleet:peer from=a intent=fyi] 未知")).toBeNull();
    expect(peerIntentOf("ふつうの発話")).toBeNull();
  });
});

describe("mergeTurns", () => {
  it("replaces rather than stacks a resend of the same idx", () => {
    // opencode/codex re-send the assistant turn still growing on every poll, so a naive append
    // lines the same answer up over and over (seen in the shared view).
    const held = [user("お願い", { idx: 1 }), asst("考え中", { idx: 2 })];
    const merged = mergeTurns(held, [asst("考え中… できました", { idx: 2 })]);
    expect(merged).toHaveLength(2);
    expect(merged[1].text).toBe("考え中… できました");
  });

  it("appends a non-overlapping increment", () => {
    const merged = mergeTurns([user("A", { idx: 1 })], [asst("B", { idx: 2 })]);
    expect(merged.map((t) => t.text)).toEqual(["A", "B"]);
  });

  it("still sorts by ascending idx when a backward page overlaps (loading earlier messages)", () => {
    const held = [user("新しい", { idx: 10 }), asst("新しい返事", { idx: 11 })];
    const older = [user("古い", { idx: 4 }), asst("古い返事", { idx: 5 }), user("新しい", { idx: 10 })];
    const merged = mergeTurns(older, held);
    expect(merged.map((t) => t.idx)).toEqual([4, 5, 10, 11]);
  });

  it("returns the same array when nothing changed, so no re-render happens", () => {
    const held = [user("A", { idx: 1 })];
    expect(mergeTurns(held, [])).toBe(held);
    expect(mergeTurns(held, [user("A", { idx: 1 })])).toBe(held);
  });

  it("leaves turns without an idx (an old Agent) in arrival order", () => {
    const merged = mergeTurns([user("A")], [asst("B")]);
    expect(merged.map((t) => t.text)).toEqual(["A", "B"]);
  });
});
