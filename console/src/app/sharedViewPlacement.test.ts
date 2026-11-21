// The shared sessions belong to their own rail view, not to the bottom of the Sessions view.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const src = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const block = (id: string) => {
  const start = src.indexOf(`<RailView id="${id}"`);
  return src.slice(start, src.indexOf("</RailView>", start));
};

describe("shared sessions placement", () => {
  it("renders the section once, in the shared view only", () => {
    expect(src.match(/<SharedSessionsSection \/>/g)).toHaveLength(1);
    expect(block("shared")).toContain("<SharedSessionsSection />");
    expect(block("sessions")).not.toContain("SharedSessionsSection");
  });
});
