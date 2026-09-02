// カタログ分割（ADR 0067 決定 4）の不変条件を守るテスト。
//
// 分割の狙いは「各フロントセッションが自分のドメインのファイルにだけ追記する＝衝突しない」
// ことで、それが成立する条件は 3 つある。どれが壊れても **アプリはそれなりに動いてしまう**
// ので、テストで止めるしかない:
//
//  1. 同じキーが 2 つのドメインファイルに在らないこと。合成は spread なので**後勝ちで
//     無言に上書き**される —— 2 セッションが同じキーを別ファイルに足すと、片方の文言が
//     消えたまま緑になる。
//  2. 1 つの接頭辞（"chat" / "settings" …）は 1 ファイルだけが持つこと。これが崩れると
//     「自分のドメインのファイル」がどれか決まらなくなり、全員が同じファイルを触る元の
//     状態に戻る（＝分割した意味が消える）。
//  3. **ディレクトリに在るドメインが、合成にも入っていること。** これは分割して初めて
//     生まれた失敗の形で、しかも全ゲートをすり抜ける（下記）。
//
// ja と en のファイル構成が一致していること（キーの網羅そのものは tsc と i18n.test.ts が
// 見ている）もここで確かめる。
import { describe, it, expect } from "vitest";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

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

  // 🔥 ここが無いと、新しいドメインを足したのに ja.ts / en.ts の import と spread を
  // 忘れても **すべてのゲートが緑のまま通る**（レビュー指摘 R-1・実測で再現済み）:
  // このファイルの glob はディレクトリを直接読むので「在る」ように見え、tsc も
  // 未使用ファイルを咎めず、i18n.test.ts の網羅ガードは**合成後の** ja/en どうしを
  // 突き合わせるので両方足し忘れると差が出ない。t() は画面に生キーを出すだけ。
  // 新ドメインの追加＝新規 2 ファイル＋合成 2 ファイルなので、後半 2 つを忘れるのが
  // 最も普通の抜け方で、ウェーブ A 以降は文言追加を伴う＝必ず起きる。
  //
  // 逆向き（合成にしか無いキー）も同時に見る。ja.ts / en.ts に直接キーを書くと、
  // また全員が触る 1 ファイルに戻る＝分割の意味が消えるため。
  it.each([
    ["ja", jaCatalogs, ja as Catalog],
    ["en", enCatalogs, en as Catalog],
    // ⚠️ タイトルの %s は 1 つだけにする。it.each は残りの引数も順に埋めるので、
    // 2 つ書くとカタログ Map 全体（4,112 キー）がテスト名に展開される。
  ] as const)("%s: ドメインファイルと合成ファイルのキー集合が一致する", (locale, catalogs, composed) => {
    const notComposed: string[] = [];
    for (const [domain, cat] of catalogs) {
      const miss = Object.keys(cat).filter((k) => !(k in composed));
      // ファイルごとにまとめて報告する。1 ドメイン丸ごと欠けているのか、キーが数個
      // 落ちているのかで、直す場所（import と spread か、ドメインファイル自身か）が違う。
      if (miss.length > 0) {
        notComposed.push(
          `${locale}/${domain}.ts: ${miss.length}/${Object.keys(cat).length} キーが locales/${locale}.ts に無い` +
            `（例 ${miss.slice(0, 3).join(", ")}）— import と ...${domain} を足し忘れていないか`,
        );
      }
    }
    expect(notComposed).toEqual([]);

    const fromDomains = new Set<string>();
    for (const cat of catalogs.values()) for (const k of Object.keys(cat)) fromDomains.add(k);
    const onlyComposed = Object.keys(composed).filter((k) => !fromDomains.has(k));
    expect(onlyComposed, `locales/${locale}.ts に直接書かれたキー（ドメインファイルへ移すこと）`).toEqual([]);
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
