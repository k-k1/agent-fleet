import { describe, expect, it } from "vitest";
import { shellLaunchTarget } from "./shellTarget.ts";

const names = ["app", "app@wip-a"];

describe("shellLaunchTarget", () => {
  it("launches a folder inside a working copy in the copy, with the folder as subdir", () => {
    expect(shellLaunchTarget("repos/app@wip-a/docs/x", names)).toEqual({ dir: "repos/app@wip-a", subdir: "docs/x", inCopy: true });
  });
  it("launches the working copy root without a subdir", () => {
    expect(shellLaunchTarget("repos/app", names)).toEqual({ dir: "repos/app", subdir: "", inCopy: true });
  });
  it("launches by path when the folder belongs to no known working copy", () => {
    expect(shellLaunchTarget("repos/scratch/x", names)).toEqual({ dir: "repos/scratch/x", subdir: "", inCopy: false });
    expect(shellLaunchTarget("notes", names)).toEqual({ dir: "notes", subdir: "", inCopy: false });
  });
  it("spells a subdir that starts with ~ as ./~ so the Agent does not read a home expansion", () => {
    expect(shellLaunchTarget("repos/app/~backup/x", names).subdir).toBe("./~backup/x");
    expect(shellLaunchTarget("repos/app/a/~b", names).subdir).toBe("a/~b");
  });
  it("launches a folder inside a space in the space's root, so the shell is the space's", () => {
    expect(shellLaunchTarget("spaces/q3-abcdef/notes", names)).toEqual({ dir: "spaces/q3-abcdef", subdir: "notes", inCopy: false });
    expect(shellLaunchTarget("spaces/q3-abcdef", names)).toEqual({ dir: "spaces/q3-abcdef", subdir: "", inCopy: false });
    expect(shellLaunchTarget("spaces/q3-abcdef/~x/y", names).subdir).toBe("./~x/y");
    // ...whatever a working copy of the same name is called.
    expect(shellLaunchTarget("spaces/app/docs", names)).toEqual({ dir: "spaces/app", subdir: "docs", inCopy: false });
  });
  it("launches an absolute path by path, even when it looks like a working copy", () => {
    expect(shellLaunchTarget("/repos/app/x", names)).toEqual({ dir: "/repos/app/x", subdir: "", inCopy: false });
  });
});
