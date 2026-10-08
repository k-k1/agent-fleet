import { describe, expect, it } from "vitest";
import { repoOfFilePath } from "./repoOfPath.ts";

describe("repoOfFilePath", () => {
  it("names the working copy under repos/", () => {
    expect(repoOfFilePath("repos/fleet/CHANGELOG.md")).toBe("fleet");
    expect(repoOfFilePath("repos/fleet@wip-a/docs/a/b.md")).toBe("fleet@wip-a");
    expect(repoOfFilePath("./repos/fleet/x.md")).toBe("fleet");
  });
  it("is null outside ~/repos", () => {
    expect(repoOfFilePath("notes/todo.md")).toBeNull();
    expect(repoOfFilePath("/tmp/repos/fleet/x.md")).toBeNull();
    expect(repoOfFilePath("repos/fleet")).toBeNull();
    expect(repoOfFilePath("repos/fleet/")).toBeNull();
  });
});
