// ui/Button (.ui-btn) is inline-flex and nowrap. Two bar controls must undo that, and jsdom does
// not apply CSS, so the rule text is checked instead:
//   - a notification row's title wraps (only its subtitle ellipsizes); nowrap inherited from
//     .ui-btn would cut a long English title at 300px (#1640 review: 76.5px → 58.5px row);
//   - a browser attachment's title ellipsizes on the button itself, which only draws when the
//     button is not a flex box (its text would be an anonymous flex item).
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const read = (f: string) =>
  fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

/** Bodies of every top-level rule whose selector list includes `sel`. */
const bodies = (css: string, sel: string) =>
  [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
    .filter((m) => m[1].split(",").map((s) => s.trim().replace(/\s+/g, " ")).includes(sel))
    .map((m) => m[2]);

describe("bar controls that undo ui/Button's layout", () => {
  it("lets a notification row's title wrap, keeping the subtitle's ellipsis", () => {
    const css = read("topbar.css");
    const item = bodies(css, ":where(.topbar) .notification-item");
    expect(item.length).toBe(1);
    expect(item[0]).toMatch(/white-space:\s*normal/);
    expect(bodies(css, ".notification-item small")[0]).toMatch(/white-space:\s*nowrap/);
  });

  it("lays a browser attachment out as a block so its ellipsis draws", () => {
    const att = bodies(read("wsbar.css"), ".pv-attachment");
    expect(att.length).toBe(1);
    expect(att[0]).toMatch(/display:\s*(block|inline-block)/);
    expect(att[0]).toMatch(/text-overflow:\s*ellipsis/);
  });
});
