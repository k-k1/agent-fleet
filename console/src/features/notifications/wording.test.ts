// The notification center's row headings must translate every kind wording.ts handles.
//
// Damage: the three handoff kinds (docs/log/77) and carried-interaction had branches in wording()
// but were missing from the heading table, so the center showed the raw identifier
// `handoff-offer` in either locale. The types cannot catch it (the table is
// Record<string, MsgKey> while the kinds are string-literal branches), so the kinds are scraped
// out of wording.ts's *source* and compared. Add a branch, forget the translation, this fails.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { NOTIFICATION_KIND_LABELS, notificationKindLabel, notificationWording } from "./wording.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import { ja } from "../../lib/i18n/locales/ja.ts";
import { en } from "../../lib/i18n/locales/en.ts";

/** Collect the kinds appearing in wording()'s `n.kind === "…"` branches from its source. */
function kindsInWording(): string[] {
  const src = readFileSync(path.resolve(__dirname, "wording.ts"), "utf8");
  const out = new Set<string>();
  for (const m of src.matchAll(/n\.kind === "([a-z-]+)"/g)) out.add(m[1]);
  return [...out];
}

describe("notification row headings", () => {
  it("every kind wording() handles has a translation", () => {
    const kinds = kindsInWording();
    // If the scrape stops matching (the code was written differently) the check would silently
    // measure nothing, so assert a lower bound too.
    expect(kinds.length).toBeGreaterThan(10);
    expect(kinds.filter((k) => !NOTIFICATION_KIND_LABELS[k])).toEqual([]);
  });

  it("the table's translation keys exist in both ja and en", () => {
    for (const key of Object.values(NOTIFICATION_KIND_LABELS)) {
      expect(ja[key], key).toBeTruthy();
      expect(en[key as keyof typeof en], key).toBeTruthy();
    }
  });

  it("a handoff notification is not shown as a raw identifier", () => {
    setLocale("ja");
    expect(notificationKindLabel("handoff-offer")).toBe("引き継ぎが届きました");
    setLocale("en");
    expect(notificationKindLabel("handoff-offer")).toBe("A handoff arrived");
    setLocale("ja");
  });

  it("an unknown kind falls back to the identifier (new CP, old Console)", () => {
    expect(notificationKindLabel("brand-new-kind")).toBe("brand-new-kind");
  });

  it("a handoff does not fall through to the usage-reset wording (the last-branch mix-up)", () => {
    setLocale("ja");
    const w = notificationWording({ kind: "handoff-offer", displayName: "残作業の続き", payload: {} });
    expect(w.title).toBe(ja["notif.handoff_offer.title"]);
    expect(w.body).toBe("残作業の続き");
  });

  // Notification about residue an architecture change could not restore automatically
  // (docs/decisions/0068). Its subject is the workspace, not a session, so displayName is empty:
  // taking the default "body = displayName" would produce a notification with no content. The row
  // must show the payload's contents themselves.
  it("architecture residue puts the payload subject in the row (displayName is empty)", () => {
    setLocale("ja");
    const w = notificationWording({
      kind: "arch-residue",
      displayName: "",
      payload: { from: "amd64", repos: ["demo/node_modules"], bins: ["mytool"] },
    });
    expect(w.title).toBe(ja["notif.arch_residue.title"]);
    expect(w.body).toBe("demo/node_modules, mytool");
    // It must not fall through to the usage-reset wording at the end (the trap schedule-* hit).
    expect(w.body).not.toContain("上限");
    expect(w.speech).toBe(ja["notif.arch_residue.speech"]);
  });

  it("architecture residue with an empty payload still has a body", () => {
    setLocale("ja");
    const w = notificationWording({ kind: "arch-residue", displayName: "", payload: {} });
    expect(w.body).toBe(ja["notif.arch_residue.body_generic"]);
  });
});
