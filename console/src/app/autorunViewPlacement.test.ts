// Auto-run has a rail view of its own, right after Memo queue; the repository tree no longer
// carries an Auto-run group.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { RAIL_VIEWS } from "../core/store/leftRail.ts";

const src = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const block = (id: string) => {
  const start = src.indexOf(`<RailView id="${id}"`);
  return src.slice(start, src.indexOf("</RailView>", start));
};

describe("auto-run view placement", () => {
  it("comes after memo", () => {
    expect(RAIL_VIEWS.indexOf("autorun")).toBe(RAIL_VIEWS.indexOf("memo") + 1);
  });
  it("renders the section once, in its own view and not in Sessions", () => {
    expect(src.match(/<AutoRunSection \/>/g)).toHaveLength(1);
    expect(block("autorun")).toContain("<AutoRunSection />");
    expect(block("sessions")).not.toContain("AutoRunSection");
  });
});
