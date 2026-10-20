// The import/export views of the two memory entries share one verb pair (取り込み / 書き出し) and one
// separator, ordered by each view's first action: Agent memory leads with import, Memory snapshots
// with export. "持ち出し" and the slash are not used for these tab labels.
import { describe, it, expect } from "vitest";
import { tools as en } from "./locales/en/tools.ts";
import { tools as ja } from "./locales/ja/tools.ts";

describe("memory import/export view labels", () => {
  it("name Agent memory's tab import-first and Memory snapshots' tab export-first, with one separator", () => {
    expect(en["mem.view_importexport"]).toBe("Import & export");
    expect(en["mem.view_transfer"]).toBe("Export & import");
    expect(ja["mem.view_importexport"]).toBe("取り込み・書き出し");
    expect(ja["mem.view_transfer"]).toBe("書き出し・取り込み");
  });
});
