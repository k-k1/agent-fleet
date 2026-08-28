// ソースに**生の制御文字**を置いてはいけない、という静的検査。
//
// 合成キーの区切りに NUL を使う書き方（`repo + "\u0000" + rel`）自体は正しいが、エスケープでは
// なく生の 0x00 バイトがそのままファイルに入っていることがあった（5 ファイル 6 か所）。JS の
// 文法としては通り、実行結果も同じなので誰も気づかない。困るのは読む側の道具で:
//   - git / ripgrep / grep はその瞬間そのファイルを**バイナリ扱い**にし、検索から丸ごと落ちる
//     （`rg foo` が「無い」と答える。実際に調査中これで 1 ファイル見失った）
//   - diff / レビュー画面でも中身が表示されなくなる
// つまり「あるのに grep に出ないコード」が生まれる。エスケープで書けば全部避けられる。
//
// タブ・改行・復帰は普通の空白なので対象外。
import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const SRC = path.resolve(__dirname, "..");

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) sourceFiles(p, out);
    else if (/\.(ts|tsx|css)$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("生の制御文字", () => {
  it("ソースには入れない（エスケープで書く）", () => {
    const bad: string[] = [];
    for (const file of sourceFiles(SRC)) {
      const text = readFileSync(file, "utf8");
      text.split("\n").forEach((line, i) => {
        // eslint-disable-next-line no-control-regex -- これを見つけるのが目的
        const m = line.match(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/);
        if (m) {
          const code = "\\u" + m[0].charCodeAt(0).toString(16).padStart(4, "0");
          bad.push(`${path.relative(SRC, file)}:${i + 1} に生の ${code}（"${code}" と書く）`);
        }
      });
    }
    expect(bad).toEqual([]);
  });
});
