// focusRegion("rail"): the left pane keeps every view mounted and shows one, and the pane can
// be folded away while the activity bar stays. Focus must land somewhere that is on screen.
import { afterEach, describe, expect, it, vi } from "vitest";

// Region focus stands down on a touch device; jsdom answers no media query, which reads as one.
vi.mock("../../lib/device.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  coarsePointer: () => false,
}));

const { focusRegion } = await import("./focus.ts");

function build(folded: boolean) {
  document.body.innerHTML = `
    <div class="actbar">
      <button class="actbar-btn launch" id="home"></button>
      <button class="actbar-btn" id="v-sessions" aria-pressed="false"></button>
      <button class="actbar-btn" id="v-files" aria-pressed="${folded ? "false" : "true"}"></button>
    </div>
    <div class="app-rail" style="${folded ? "display:none" : ""}">
      <div class="rail-view" hidden><button id="in-sessions"></button></div>
      <div class="rail-view"><button id="in-files"></button></div>
    </div>`;
}
afterEach(() => (document.body.innerHTML = ""));

describe("focusRegion rail", () => {
  it("lands in the view on screen, not in a hidden one that comes first", () => {
    build(false);
    focusRegion("rail");
    expect(document.activeElement?.id).toBe("in-files");
  });

  it("falls back to the activity bar while the pane is folded away", () => {
    build(true);
    focusRegion("rail");
    expect(document.activeElement?.id).toBe("home");
  });

  it("prefers the pressed icon when there is one and no view to land in", () => {
    build(false);
    document.querySelector(".rail-view:not([hidden])")!.innerHTML = "";
    focusRegion("rail");
    expect(document.activeElement?.id).toBe("v-files");
  });
});
