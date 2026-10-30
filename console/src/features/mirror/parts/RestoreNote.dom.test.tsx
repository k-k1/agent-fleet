import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RestoreNote, restoreNoteCards, restoreNoteStamps } from "./RestoreNote.tsx";
import { setLocale, t as tr } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setLocale("en");
});

describe("restoreNoteStamps", () => {
  it("keeps only non-empty strings and tolerates a missing field", () => {
    expect(restoreNoteStamps(undefined)).toEqual([]);
    expect(restoreNoteStamps("2026-10-27T10:00:00Z")).toEqual([]);
    expect(restoreNoteStamps(["2026-10-27T10:00:00Z", "", 3, null])).toEqual(["2026-10-27T10:00:00Z"]);
  });
});

describe("restoreNoteCards", () => {
  it("makes one card per stamp, placed at the restore time", () => {
    const cards = restoreNoteCards(["2026-10-27T10:00:00Z", "2026-10-28T10:00:00Z"]);
    expect(cards.map((c) => c.at)).toEqual([Date.parse("2026-10-27T10:00:00Z"), Date.parse("2026-10-28T10:00:00Z")]);
    expect(restoreNoteCards([])).toEqual([]);
  });
});

describe("RestoreNote", () => {
  for (const loc of ["en", "ja"] as const) {
    it(`renders the recovered-from-backup wording (${loc})`, () => {
      setLocale(loc);
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      act(() => root!.render(<RestoreNote />));
      const text = host.textContent || "";
      expect(text).toContain(tr("mirror.transcript_restored_note"));
      expect(text).toContain(loc === "en" ? "backup" : "バックアップ");
      expect(host.querySelector("[role=note]")).not.toBeNull();
    });
  }
});
