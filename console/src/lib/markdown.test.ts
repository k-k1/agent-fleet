import { describe, expect, it } from "vitest";
import { Marked } from "marked";
import {
  asciiPunctuationRule,
  isLinkDestination,
  marked,
  repairFullwidthTables,
  splitYamlFrontMatter,
} from "./markdown.ts";

describe("splitYamlFrontMatter", () => {
  it("extracts a leading YAML front matter block", () => {
    expect(splitYamlFrontMatter("---\ntitle: Example\ntags:\n  - docs\n---\n\n# Body")).toEqual({
      attributes: { title: "Example", tags: ["docs"] },
      body: "\n# Body",
    });
  });

  it("supports a YAML end marker and CRLF", () => {
    expect(splitYamlFrontMatter("\uFEFF---\r\ntitle: Example\r\n...\r\n# Body")).toEqual({
      attributes: { title: "Example" },
      body: "# Body",
    });
  });

  it("ignores non-leading, incomplete, and invalid blocks", () => {
    expect(splitYamlFrontMatter("# Body\n\n---\ntitle: Example\n---")).toBeNull();
    expect(splitYamlFrontMatter("---\ntitle: Example\n# Body")).toBeNull();
    expect(splitYamlFrontMatter("---\ntitle: [\n---\n# Body")).toBeNull();
  });

  // YAML reserves ` and @ as the first character of a plain scalar, so a line a
  // human reads as perfectly ordinary throws and used to drop the whole block
  // into the body as one run-on paragraph. Read it line by line instead — and
  // flag it, because every other Markdown viewer still shows the mess.
  it("reads a block YAML rejects as flat key: value lines", () => {
    const source = "---\n用途: 商業化可能性評価\n備考: `レビュー_辛口編集者.md` とは役割が違う\n---\n\n# 本文";
    expect(splitYamlFrontMatter(source)).toEqual({
      attributes: { 用途: "商業化可能性評価", 備考: "`レビュー_辛口編集者.md` とは役割が違う" },
      body: "\n# 本文",
      lenient: true,
    });
  });

  it("keeps valid YAML off the lenient path, Japanese keys included", () => {
    expect(splitYamlFrontMatter("---\n用途: 評価\n---\n# 本文")).toEqual({
      attributes: { 用途: "評価" },
      body: "# 本文",
    });
  });

  it("skips blank and comment lines, and unquotes a quoted value", () => {
    expect(splitYamlFrontMatter('---\n# note\n\nout: `a.md`\nname: "x: y"\n---\n')).toEqual({
      attributes: { out: "`a.md`", name: "x: y" },
      body: "",
      lenient: true,
    });
  });

  // The lenient read only rescues plain text. Anything shaped like real YAML that
  // failed to parse stays prose: a wrong string on screen is worse than the mess.
  it("refuses the lenient read for nesting, lists, and structured values", () => {
    expect(splitYamlFrontMatter("---\nout: `a.md`\ntags:\n  - one\n  - [\n---\n")).toBeNull();
    expect(splitYamlFrontMatter("---\nout: `a.md`\ntags: [one, [\n---\n")).toBeNull();
    expect(splitYamlFrontMatter("---\nout: `a.md`\nplain text line\ntitle: [\n---\n")).toBeNull();
  });
});

describe("repairFullwidthTables", () => {
  it("rewrites a table written entirely with fullwidth pipes", () => {
    const repair = repairFullwidthTables("｜章｜点｜\n｜---｜---｜\n｜A1｜6.5｜\n");
    expect(repair?.body).toBe("|章|点|\n|---|---|\n|A1|6.5|\n");
    expect(repair).toMatchObject({ repaired: [0], total: 1 });
  });

  it("repairs when only the delimiter row is ASCII — it carries no text to judge by", () => {
    expect(repairFullwidthTables("｜章｜点｜\n|---|---|\n｜A1｜6.5｜")?.body).toBe("|章|点|\n|---|---|\n|A1|6.5|");
  });

  it("repairs a half-converted table where only the header row was left fullwidth", () => {
    expect(repairFullwidthTables("｜章｜点｜\n|---|---|\n| A1 | 6.5 |\n| A2 | 7 |")?.body).toBe(
      "|章|点|\n|---|---|\n| A1 | 6.5 |\n| A2 | 7 |",
    );
  });

  it("leaves cell text alone — ー is a prolonged sound mark, not a dash", () => {
    expect(repairFullwidthTables("｜章コード｜点｜\n｜---｜---｜\n｜A1｜ノートとスマートロック｜")?.body).toBe(
      "|章コード|点|\n|---|---|\n|A1|ノートとスマートロック|",
    );
  });

  it("normalizes fullwidth dashes on the delimiter row, where they can only be dashes", () => {
    expect(repairFullwidthTables("｜章コード｜点｜\n｜ーーー｜ーーー｜\n｜A1｜6｜")?.body).toBe(
      "|章コード|点|\n|---|---|\n|A1|6|",
    );
  });

  it("changes nothing but pipes outside the delimiter row", () => {
    const source = "｜章コード｜評価｜\n｜---｜---｜\n｜A1｜データ〜ノート・メール｜\n｜A2｜ロングテール｜";
    const body = repairFullwidthTables(source)!.body;
    const withoutPipes = (text: string) => text.replace(/[|｜￨]/g, "");
    expect(withoutPipes(body)).toBe(withoutPipes(source));
  });

  it("supplies a missing delimiter row once enough rows agree on a column count", () => {
    expect(repairFullwidthTables("｜章｜点｜\n｜A1｜6｜\n｜A2｜7｜\n｜A3｜8｜")?.body).toBe(
      "|章|点|\n|---|---|\n|A1|6|\n|A2|7|\n|A3|8|",
    );
    // Two rows are as easily a coincidence as a table — left as written.
    expect(repairFullwidthTables("｜章｜点｜\n｜A1｜6｜")).toBeNull();
  });

  it("leaves a fullwidth pipe that is cell content of a working table", () => {
    // The only way to put a vertical bar in a cell without splitting it, so the ｜ here
    // is deliberate — rewriting it would break a table that renders fine today.
    expect(repairFullwidthTables("| status | `pending｜failed` |\n|---|---|\n| a | b |")).toBeNull();
  });

  it("leaves prose and fenced code alone", () => {
    expect(repairFullwidthTables("- 集中度：A1 高｜A2 低｜A3 高")).toBeNull();
    expect(repairFullwidthTables("```\n｜章｜点｜\n｜---｜---｜\n｜A1｜6｜\n```")).toBeNull();
    expect(repairFullwidthTables("    ｜章｜点｜\n    ｜---｜---｜\n    ｜A1｜6｜")).toBeNull();
  });

  it("counts every table so a caller can line the indexes up with rendered tables", () => {
    const repair = repairFullwidthTables("| a | b |\n|---|---|\n| 1 | 2 |\n\n｜c｜d｜\n｜---｜---｜\n｜3｜4｜");
    expect(repair).toMatchObject({ repaired: [1], total: 2 });
  });

  it("returns null for a document with no fullwidth pipe at all", () => {
    expect(repairFullwidthTables("| a | b |\n|---|---|\n| 1 | 2 |")).toBeNull();
  });
});

// A link reference definition renders as nothing, and Japanese prose has no ASCII space,
// so `- [保留]: 幕間の再配置／MED語彙拡張。` matched the shape whole and vanished. The
// tokenizer keeps definitions working and only declines the ones no author wrote.
describe("link reference definitions", () => {
  const html = (source: string) => marked.parse(source, { gfm: true }) as string;

  it("renders a label followed by Japanese prose as written", () => {
    expect(html("- [保留]: 幕間の再配置（一律不可・幕間ごと個別）／MED語彙拡張。")).toContain(
      "[保留]: 幕間の再配置（一律不可・幕間ごと個別）／MED語彙拡張。",
    );
    // Not registered either: without this, every later [保留] became a link to the sentence.
    expect(html("- [保留]: 幕間の再配置。\n- あとで[保留]を見る")).not.toContain("<a ");
  });

  it("still consumes a definition with a real destination", () => {
    expect(html("[foo]: https://example.com/x\n\nsee [foo]")).toBe(
      '<p>see <a href="https://example.com/x">foo</a></p>\n',
    );
    expect(html("[d]: /docs/log/68.md\n\nsee [d]")).toContain('href="/docs/log/68.md"');
    // The destination and the title may sit on the following lines — marked's own rule
    // decides where the definition ends, so the title line is consumed with it.
    expect(html('[t]: /a.md\n  "Title"\n\nsee [t]')).toContain('title="Title"');
  });

  it("judges a destination by whether it could be one", () => {
    expect(isLinkDestination("https://ja.wikipedia.org/wiki/日本語")).toBe(true);
    expect(isLinkDestination("/docs/日本語.md")).toBe(true);
    expect(isLinkDestination("./図.drawio")).toBe(true);
    expect(isLinkDestination("#見出し")).toBe(true);
    expect(isLinkDestination("<any thing>")).toBe(true);
    expect(isLinkDestination("mailto:a@example.com")).toBe(true);
    expect(isLinkDestination("docs/log/68-session-changed-files.md")).toBe(true);
    expect(isLinkDestination("中イキ未達を意図化する案（既定設計と逆）。")).toBe(false);
    expect(isLinkDestination("幕間の再配置／MED語彙拡張。")).toBe(false);
  });

  it("leaves a same-shaped line inside a code block untouched", () => {
    const source = "```\n[保留]: 幕間の再配置。\n```";
    expect(html(source)).toContain("[保留]: 幕間の再配置。");
    expect(html(source)).toContain("<code>");
  });
});

// Emphasis around Japanese punctuation. CommonMark reads 「、。… as punctuation, and a
// delimiter with punctuation on one side and a letter on the other flanks neither way —
// so bold written the way Japanese is written came out as literal asterisks. See
// asciiPunctuationRule in markdown.ts.
describe("emphasis next to non-ASCII punctuation", () => {
  const inline = (source: string) => marked.parseInline(source) as string;

  it("bolds a run that starts or ends on Japanese punctuation", () => {
    expect(inline("あ**「強調」**です")).toBe("あ<strong>「強調」</strong>です");
    expect(inline("あ**（強調）**です")).toBe("あ<strong>（強調）</strong>です");
    expect(inline("**強調。**続く")).toBe("<strong>強調。</strong>続く");
    expect(inline("**強調、**続く")).toBe("<strong>強調、</strong>続く");
    expect(inline("あ**【強調】**い")).toBe("あ<strong>【強調】</strong>い");
    expect(inline("あ**…強調…**い")).toBe("あ<strong>…強調…</strong>い");
  });

  it("does the same for italics and strikethrough", () => {
    expect(inline("あ*「強調」*です")).toBe("あ<em>「強調」</em>です");
    expect(inline("あ~~「取り消し」~~です")).toBe("あ<del>「取り消し」</del>です");
    expect(inline("~~取り消し。~~続く")).toBe("<del>取り消し。</del>続く");
  });

  it("leaves `_` alone, so a filename stays a filename", () => {
    expect(inline("Ph0_声の増量設計_改.md")).toBe("Ph0_声の増量設計_改.md");
    expect(inline("あ_強調_です")).toBe("あ_強調_です");
    expect(inline("__太字__")).toBe("<strong>太字</strong>");
  });

  // The whole point of rewriting only the Unicode classes: over ASCII the rules are
  // character-for-character what marked shipped, so nothing an English document does can
  // parse differently than before.
  it("parses ASCII exactly as marked does", () => {
    const cases = [
      "a**b**c",
      "**bold** text",
      "*em*",
      "a * b * c",
      "2 * 3 * 4",
      "snake_case_name",
      "**a_b**",
      "a~~b~~c",
      "**(x)**y",
      "x**(y)**z",
      "**a**b**c**",
      "***both***",
      "a *b* c",
      "5*6*7",
      "* list item",
      "foo**bar**baz",
      "**\"q\"**",
      "a**\"q\"**b",
    ];
    for (const source of cases) expect(inline(source)).toBe(new Marked().parseInline(source) as string);
  });

  it("keeps the emphasis marked already got right", () => {
    expect(inline("これは**強調**です")).toBe("これは<strong>強調</strong>です");
    expect(inline("「**強調**」です")).toBe("「<strong>強調</strong>」です");
    expect(inline("**強調**（注）")).toBe("<strong>強調</strong>（注）");
    expect(inline("あ**強調**！")).toBe("あ<strong>強調</strong>！");
  });

  // Why the relaxed rules are a second attempt and not a replacement. Read with them
  // alone, the closing run here sits between a backtick — ASCII, still punctuation — and a
  // 。 that is no longer punctuation, which makes it an opener and nothing closes. This is
  // not a corner case: 150 spans across this repository's own documents move, and these
  // used to be among them.
  it("keeps bold that begins or ends on a code span", () => {
    expect(inline("**`--read-only`**。")).toBe("<strong><code>--read-only</code></strong>。");
    expect(inline("**既定は `--read-only`**。")).toBe("<strong>既定は <code>--read-only</code></strong>。");
    expect(inline("**`mcp-proxy-for-aws`**（PyPI）")).toBe("<strong><code>mcp-proxy-for-aws</code></strong>（PyPI）");
  });

  it("rewrites the character classes it was built against", () => {
    // A no-op means marked now spells its delimiter rules differently and the fix above
    // is silently doing nothing — the tests before this one would go red with it.
    const rule = /(?:[^\s\p{P}\p{S}]|~)(\*+)(?=(?!~)[\s\p{P}\p{S}]|$)/u;
    expect(asciiPunctuationRule(rule).source).not.toBe(rule.source);
    expect(asciiPunctuationRule(/^nothing to do$/).source).toBe("^nothing to do$");
  });
});
