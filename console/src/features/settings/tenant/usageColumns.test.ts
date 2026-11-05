// The per-member bar lists must share their columns across rows (#3382). jsdom does no layout, so
// this pins the CSS contract that makes every bar start at the same x: the list is a grid and each
// row is a subgrid of it. Without it the rows fall back to independent flex rows and the bar
// begins wherever that row's name ends.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";

const css = readFileSync("src/features/settings/tenant/tenant.css", "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

function rule(selector: string): string {
  const esc = selector.replace(/[.*+?^${}()|[\]\\>]/g, "\\$&");
  const m = css.match(new RegExp("(?:^|})\\s*" + esc + "\\s*\\{([^}]*)\\}"));
  return m ? m[1] : "";
}

describe("usage list columns", () => {
  it("makes the list a grid and each row a subgrid spanning every column", () => {
    expect(rule(".usage-rows.ur-table")).toMatch(/display:\s*grid/);
    const row = rule(".ur-table > .usage-row");
    expect(row).toMatch(/grid-template-columns:\s*subgrid/);
    expect(row).toMatch(/grid-column:\s*1\s*\/\s*-1/);
  });

  it("has a four-column variant for the tenant column and right-aligns amounts", () => {
    const withTenant = rule(".usage-rows.ur-table.ur-with-tenant");
    expect(withTenant.split("grid-template-columns:")[1].trim().split(/\s+(?=\w)/).length).toBeGreaterThanOrEqual(4);
    expect(rule(".ur-table .ur-hrs")).toMatch(/tabular-nums/);
  });
});
