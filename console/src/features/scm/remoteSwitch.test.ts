import { describe, expect, it } from "vitest";
import { remoteSwitchTargets } from "./remoteSwitch.ts";

const rem = (name: string, remote: string, branch: string) => ({ name, type: "remote" as const, remote, branch });

describe("remoteSwitchTargets", () => {
  it("uses the agent's split, so slashes in remote and branch names survive", () => {
    expect(remoteSwitchTargets([rem("team/upstream/feat/x", "team/upstream", "feat/x")])).toEqual([
      { ref: "team/upstream/feat/x", remote: "team/upstream", branch: "feat/x" },
    ]);
  });

  it("offers each remote's copy separately", () => {
    const got = remoteSwitchTargets([rem("aaa/main", "aaa", "main"), rem("origin/main", "origin", "main")]);
    expect(got.map((t) => t.remote)).toEqual(["aaa", "origin"]);
  });

  it("skips unattributed refs, symbolic HEAD, tags and branches already local here", () => {
    expect(
      remoteSwitchTargets([
        { name: "stale/main", type: "remote" },
        rem("origin/HEAD", "origin", "HEAD"),
        { name: "v1", type: "tag" },
        { name: "main", type: "head" },
        rem("origin/main", "origin", "main"),
      ]),
    ).toEqual([]);
  });
});
