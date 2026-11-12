import { describe, expect, it } from "vitest";
import { setLocale, tMaybe } from "./index.ts";

// The dark-slot audit actions (ADR 0121 decision 4) are labelled in both languages; the audit
// view shows the label as a tooltip on the raw action name.
describe("dark-slot audit action labels", () => {
  const actions = ["detected", "rescued", "blocked", "quarantined"].map((a) => "pool.slot_dark_" + a);
  for (const locale of ["en", "ja"]) {
    it("has a label for each action in " + locale, () => {
      setLocale(locale);
      for (const a of actions) expect(tMaybe("audit.action." + a)).toBeTruthy();
      setLocale("ja");
    });
  }
  it("has none for an action nobody labelled", () => {
    expect(tMaybe("audit.action.pool.slot_nonexistent")).toBeUndefined();
  });
});
