import { describe, expect, it } from "vitest";
import { splitMiddle } from "./middleEllipsis.ts";

describe("splitMiddle", () => {
  it("keeps the extension and the end of the stem", () => {
    expect(splitMiddle("SMS-A2P-LN_IBSS_SMPP_Interface_v2.0.0.pdf")).toEqual([
      "SMS-A2P-LN_IBSS_SMPP_Interf",
      "ace_v2.0.0.pdf",
    ]);
  });

  it("keeps a short file name whole at the end of a path", () => {
    expect(splitMiddle("console/src/features/files/files.css")).toEqual(["console/src/features/files/", "files.css"]);
  });

  it("falls back to the stem tail when the file name itself is long", () => {
    expect(splitMiddle("docs/SMS-A2P-LN_IBSS_SMPP_Interface_v2.0.0.md")).toEqual([
      "docs/SMS-A2P-LN_IBSS_SMPP_Interf",
      "ace_v2.0.0.md",
    ]);
  });

  it("does not split names too short to gain anything", () => {
    expect(splitMiddle("README.md")).toEqual(["", "README.md"]);
    expect(splitMiddle("build.gradle.kts")).toEqual(["", "build.gradle.kts"]);
  });

  it("round-trips the text", () => {
    for (const s of ["a", "Makefile", ".gitignore", "a/b/c", "x".repeat(60), "very-long-directory-name/with/child"]) {
      expect(splitMiddle(s).join("")).toBe(s);
    }
  });
});
