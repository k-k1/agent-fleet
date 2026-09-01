// 通知センターの行見出しは、wording.ts が扱う kind を**全部**訳せていなければならない。
//
// 実害: 引き継ぎ（docs/log/77）の 3 種と carried-interaction は wording() には分岐があるのに
// 行見出しの表だけに無く、通知センターには `handoff-offer` と生の識別子が出ていた——日本語に
// しても英語にしても訳が出ない、という報告の正体がこれ。型では気付けない（表は
// Record<string, MsgKey> で、kind は文字列リテラルの分岐）ので、wording.ts の**ソースから**
// kind を拾って突き合わせる。分岐を足して訳を忘れたら、ここが落ちる。
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { NOTIFICATION_KIND_LABELS, notificationKindLabel, notificationWording } from "./wording.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import { ja } from "../../lib/i18n/locales/ja.ts";
import { en } from "../../lib/i18n/locales/en.ts";

/** wording() の `n.kind === "…"` 分岐に出てくる kind をソースから集める。 */
function kindsInWording(): string[] {
  const src = readFileSync(path.resolve(__dirname, "wording.ts"), "utf8");
  const out = new Set<string>();
  for (const m of src.matchAll(/n\.kind === "([a-z-]+)"/g)) out.add(m[1]);
  return [...out];
}

describe("通知の行見出し", () => {
  it("wording() が扱う kind はすべて訳を持つ", () => {
    const kinds = kindsInWording();
    // 拾えなくなったら（書き方が変わった）検査が黙って空回りするので、下限も見る。
    expect(kinds.length).toBeGreaterThan(10);
    expect(kinds.filter((k) => !NOTIFICATION_KIND_LABELS[k])).toEqual([]);
  });

  it("表の訳キーは ja/en 両方に実在する", () => {
    for (const key of Object.values(NOTIFICATION_KIND_LABELS)) {
      expect(ja[key], key).toBeTruthy();
      expect(en[key as keyof typeof en], key).toBeTruthy();
    }
  });

  it("引き継ぎの通知が生の識別子のまま出ない", () => {
    setLocale("ja");
    expect(notificationKindLabel("handoff-offer")).toBe("引き継ぎが届きました");
    setLocale("en");
    expect(notificationKindLabel("handoff-offer")).toBe("A handoff arrived");
    setLocale("ja");
  });

  it("未知の kind は識別子へ落とす（新しい CP と古い Console）", () => {
    expect(notificationKindLabel("brand-new-kind")).toBe("brand-new-kind");
  });

  it("引き継ぎが「利用上限がリセットされました」へ落ちない（末尾分岐の取り違え）", () => {
    setLocale("ja");
    const w = notificationWording({ kind: "handoff-offer", displayName: "残作業の続き", payload: {} });
    expect(w.title).toBe(ja["notif.handoff_offer.title"]);
    expect(w.body).toBe("残作業の続き");
  });
});
