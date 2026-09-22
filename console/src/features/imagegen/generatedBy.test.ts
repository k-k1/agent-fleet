// Which session made a picture. The rule is one line of code and two traps, so it is tested
// here rather than through a rendered panel: the folder must be the WHOLE folder (a prefix
// match would claim every sibling session's pictures), and a session that has generated
// nothing carries no folder at all — matching those against an empty string would hand every
// root-level file to whichever such session happens to come first in the list.
import { describe, expect, it } from "vitest";
import { folderOf, sessionOfGeneratedFolder } from "./generatedBy.ts";
import type { Session } from "../../types/session.ts";

const session = (name: string, generatedImagesPath?: string): Session =>
  ({ name, kind: "claude", ...(generatedImagesPath ? { generatedImagesPath } : {}) }) as Session;

describe("folderOf", () => {
  it("returns the folder a path sits in", () => {
    expect(folderOf("gen/uuid-1/image-1.png")).toBe("gen/uuid-1");
  });

  it("returns the empty string for a file at the browse root", () => {
    expect(folderOf("image-1.png")).toBe("");
  });
});

describe("sessionOfGeneratedFolder", () => {
  const sessions = [session("aaa", "gen/uuid-1"), session("bbb", "gen/uuid-2"), session("ccc")];

  it("matches the session whose generated-images folder it is", () => {
    expect(sessionOfGeneratedFolder(sessions, "gen/uuid-2")?.name).toBe("bbb");
  });

  it("does not match a parent or a child of that folder", () => {
    expect(sessionOfGeneratedFolder(sessions, "gen")).toBeUndefined();
    expect(sessionOfGeneratedFolder(sessions, "gen/uuid-1/sub")).toBeUndefined();
  });

  it("never matches a session that has generated nothing", () => {
    // The trap: `generatedImagesPath` is omitempty, so an unguarded `s.generatedImagesPath ===
    // dir` hands every picture at the browse root to session "ccc".
    expect(sessionOfGeneratedFolder(sessions, "")).toBeUndefined();
  });
});
