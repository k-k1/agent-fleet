// カタログ分割（ADR 0067 決定 4）の不変条件を守るテスト。
//
// 分割の狙いは「各フロントセッションが自分のドメインのファイルにだけ追記する＝衝突しない」
// ことで、それが成立する条件は 2 つある。どちらも壊れても **アプリは動いてしまう**ので、
// テストで止めるしかない:
//
//  1. 同じキーが 2 つのドメインファイルに在らないこと。合成は spread なので**後勝ちで
//     無言に上書き**される —— 2 セッションが同じキーを別ファイルに足すと、片方の文言が
//     消えたまま緑になる。
//  2. 1 つの接頭辞（"chat" / "settings" …）は 1 ファイルだけが持つこと。これが崩れると
//     「自分のドメインのファイル」がどれか決まらなくなり、全員が同じファイルを触る元の
//     状態に戻る（＝分割した意味が消える）。
//
// ja と en のファイル構成が一致していること（キーの網羅そのものは tsc と i18n.test.ts が
// 見ている）もここで確かめる。
import { describe, it, expect } from "vitest";

type Catalog = Record<string, string>;
const jaModules = import.meta.glob<{ [k: string]: Catalog }>("./locales/ja/*.ts", { eager: true });
const enModules = import.meta.glob<{ [k: string]: Catalog }>("./locales/en/*.ts", { eager: true });

// 1 ファイル = 1 named export（ドメイン名）という取り決めに従って中身を取り出す。
function catalogsOf(mods: Record<string, { [k: string]: Catalog }>): Map<string, Catalog> {
  const out = new Map<string, Catalog>();
  for (const [path, mod] of Object.entries(mods)) {
    const values = Object.values(mod).filter((v) => v && typeof v === "object");
    expect(values, `${path} は named export をちょうど 1 つ持つこと`).toHaveLength(1);
    out.set(path.replace(/^.*\//, "").replace(/\.ts$/, ""), values[0] as Catalog);
  }
  return out;
}

const jaCatalogs = catalogsOf(jaModules);
const enCatalogs = catalogsOf(enModules);

describe("i18n カタログの分割", () => {
  it("ドメインファイルが 1 つ以上あり、ja と en で構成が一致する", () => {
    expect(jaCatalogs.size).toBeGreaterThan(1);
    expect([...enCatalogs.keys()].sort()).toEqual([...jaCatalogs.keys()].sort());
  });

  it.each(["ja", "en"] as const)("%s: 同じキーが 2 つのファイルに存在しない", (locale) => {
    const owner = new Map<string, string>();
    const dup: string[] = [];
    for (const [domain, cat] of locale === "ja" ? jaCatalogs : enCatalogs) {
      for (const key of Object.keys(cat)) {
        const prev = owner.get(key);
        if (prev) dup.push(`${key}: ${prev} と ${domain}`);
        else owner.set(key, domain);
      }
    }
    expect(dup).toEqual([]);
  });

  it("1 つのキー接頭辞を持つファイルは 1 つだけ", () => {
    const owner = new Map<string, string>();
    const split: string[] = [];
    for (const [domain, cat] of jaCatalogs) {
      for (const key of Object.keys(cat)) {
        const prefix = key.split(".")[0];
        const prev = owner.get(prefix);
        if (prev === undefined) owner.set(prefix, domain);
        else if (prev !== domain) split.push(`${prefix}: ${prev} と ${domain}（キー ${key}）`);
      }
    }
    expect([...new Set(split)]).toEqual([]);
  });

  it("ja と en は同じファイルに同じキーを持つ（片側だけ別ドメインに置かれていない）", () => {
    for (const [domain, jaCat] of jaCatalogs) {
      const enCat = enCatalogs.get(domain);
      expect(enCat, `en/${domain}.ts が無い`).toBeDefined();
      expect(Object.keys(enCat as Catalog).sort()).toEqual(Object.keys(jaCat).sort());
    }
  });
});
