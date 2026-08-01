import { describe, it, expect } from "vitest";
import { groupRefs } from "./gitgraph.ts";
import type { GraphRef } from "./gitgraph.ts";

const head = (name: string): GraphRef => ({ name, type: "head" });
const remote = (name: string): GraphRef => ({ name, type: "remote" });
const tag = (name: string): GraphRef => ({ name, type: "tag" });

describe("groupRefs", () => {
  it("collapses a local branch and its remote twin into one chip", () => {
    const chips = groupRefs([head("develop"), remote("origin/develop")]);
    expect(chips).toHaveLength(1);
    expect(chips[0]).toMatchObject({ type: "head", name: "develop", remotes: ["origin"] });
  });

  // git lists refs in whatever order it likes; the chip must land at the first member's
  // position and still be a head chip when the remote came first.
  it("collapses regardless of ref order and keeps slash-containing names intact", () => {
    const chips = groupRefs([remote("origin/temp/stgausi"), head("temp/stgausi")]);
    expect(chips).toHaveLength(1);
    expect(chips[0]).toMatchObject({ type: "head", name: "temp/stgausi", remotes: ["origin"] });
  });

  it("keeps a remote-only branch as its own chip", () => {
    const chips = groupRefs([head("develop"), remote("origin/feature/x")]);
    expect(chips.map((c) => [c.type, c.name, c.remotes])).toEqual([
      ["head", "develop", []],
      ["remote", "origin/feature/x", []],
    ]);
  });

  it("gathers every remote that agrees with the local branch", () => {
    const chips = groupRefs([head("main"), remote("origin/main"), remote("upstream/main")]);
    expect(chips).toHaveLength(1);
    expect(chips[0].remotes).toEqual(["origin", "upstream"]);
  });

  it("leaves tags alone (their own chip, unmerged with a same-named branch)", () => {
    const chips = groupRefs([tag("v0.5.0"), head("v0.5.0"), remote("origin/v0.5.0")]);
    expect(chips.map((c) => [c.type, c.name])).toEqual([
      ["tag", "v0.5.0"],
      ["head", "v0.5.0"],
    ]);
    expect(chips[1].remotes).toEqual(["origin"]);
  });

  it("passes through a remote name with no branch part", () => {
    const chips = groupRefs([remote("origin")]);
    expect(chips.map((c) => [c.type, c.name])).toEqual([["remote", "origin"]]);
  });
});
