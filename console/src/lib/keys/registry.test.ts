import { describe, it, expect } from "vitest";
import type { Command, Group, KeyContext } from "./registry.ts";
import { matchDirect, resolveLeader, isLeaderPrefix, leaderChildren, paletteCommands } from "./registry.ts";

const ctx: KeyContext = { region: "main", focusedKind: "other", leaderPending: false, activePaneKind: null };

const GROUPS: Group[] = [
  { id: "p", title: "ペイン" },
  { id: "w", title: "ワークスペース" },
];

let ran = "";
const CMDS: Command[] = [
  { id: "p.right", title: "右に分割", seq: "p r", run: () => (ran = "p.right") },
  { id: "p.down", title: "下に分割", seq: "p d", run: () => (ran = "p.down") },
  { id: "p.focus1", title: "ペイン1", seq: "p 1", keys: ["alt+1"], run: () => (ran = "p.focus1") },
  { id: "w.theme", title: "テーマ", seq: "w t", run: () => (ran = "w.theme") },
  { id: "settings", title: "設定", seq: ",", run: () => (ran = "settings") },
  // Only available when a terminal pane is active — proves `when` filtering.
  { id: "gated", title: "端末専用", keys: ["alt+9"], when: (c) => c.activePaneKind === "terminal", run: () => (ran = "gated") },
];

describe("matchDirect", () => {
  it("matches a canonical chord and respects when()", () => {
    expect(matchDirect(CMDS, "alt+1", ctx)?.id).toBe("p.focus1");
    expect(matchDirect(CMDS, "alt+9", ctx)).toBeUndefined(); // gated: not a terminal pane
    expect(matchDirect(CMDS, "alt+9", { ...ctx, activePaneKind: "terminal" })?.id).toBe("gated");
    expect(matchDirect(CMDS, "alt+2", ctx)).toBeUndefined();
  });
});

describe("resolveLeader / isLeaderPrefix", () => {
  it("resolves a full path and recognizes a partial one", () => {
    expect(resolveLeader(CMDS, ["p", "r"], ctx)?.id).toBe("p.right");
    expect(resolveLeader(CMDS, ["p"], ctx)).toBeUndefined(); // group, not a command
    expect(resolveLeader(CMDS, [","], ctx)?.id).toBe("settings"); // top-level single key
    expect(isLeaderPrefix(CMDS, ["p"], ctx)).toBe(true);
    expect(isLeaderPrefix(CMDS, ["z"], ctx)).toBe(false);
  });
  it("runs the resolved command", () => {
    ran = "";
    resolveLeader(CMDS, ["p", "r"], ctx)?.run(ctx);
    expect(ran).toBe("p.right");
    matchDirect(CMDS, "alt+1", ctx)?.run(ctx);
    expect(ran).toBe("p.focus1");
  });
});

describe("leaderChildren", () => {
  it("lists groups (and top-level actions) at the root", () => {
    const kids = leaderChildren(CMDS, GROUPS, [], ctx);
    const p = kids.find((k) => k.key === "p");
    expect(p?.isGroup).toBe(true);
    expect(kids.find((k) => k.key === ",")).toEqual({ key: ",", title: "設定", isGroup: false });
  });
  it("lists a group's actions once a group key is chosen", () => {
    const kids = leaderChildren(CMDS, GROUPS, ["p"], ctx);
    expect(kids.map((k) => k.key).sort()).toEqual(["1", "d", "r"]);
    expect(kids.find((k) => k.key === "r")?.title).toBe("右に分割");
  });
});

describe("paletteCommands", () => {
  it("returns only currently-available commands", () => {
    expect(paletteCommands(CMDS, ctx).some((c) => c.id === "gated")).toBe(false);
    expect(paletteCommands(CMDS, { ...ctx, activePaneKind: "terminal" }).some((c) => c.id === "gated")).toBe(true);
  });
});
