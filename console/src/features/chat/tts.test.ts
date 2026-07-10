import { describe, it, expect } from "vitest";
import { plainify, plainifyStreaming, firstChunkCut } from "./ttsText.ts";

describe("plainify (読み上げ用プレーン化)", () => {
  it("インラインコード/リンク/URL/強調を落とす", () => {
    expect(plainify("これは `code` です")).toBe("これは code です");
    expect(plainify("詳しくは [ドキュメント](https://x.example) を見て")).toBe("詳しくは ドキュメント を見て");
    // URL 除去で空いた二重スペースは 1 つに圧縮される。
    expect(plainify("生URL https://a.example/b は読まない")).toBe("生URL は読まない");
    expect(plainify("**強調** と *斜体* と ~~取消~~")).toBe("強調 と 斜体 と 取消");
  });

  it("行頭マーカー（見出し/引用/リスト）を除く", () => {
    expect(plainify("## 見出し")).toBe("見出し");
    expect(plainify("- 項目A")).toBe("項目A");
    expect(plainify("> 引用文")).toBe("引用文");
  });

  it("画像を落とす", () => {
    expect(plainify("図 ![alt](https://x/y.png) を参照")).toBe("図 を参照");
  });
});

describe("firstChunkCut (最初の発話の早出し)", () => {
  it("読点が FIRST_MIN 以降にあればそこで切る", () => {
    // "これは長めの前置きで、" → 読点(11 文字目)で切る
    const s = "これは長めの前置きで、続きます";
    expect(firstChunkCut(s)).toBe(s.indexOf("、") + 1);
  });

  it("読点が早すぎる（FIRST_MIN 未満）ときは切らない", () => {
    // "はい、" の読点は 3 文字目 → 短すぎるので早出ししない
    expect(firstChunkCut("はい、まだ短い")).toBe(-1);
  });

  it("区切りが無くても FIRST_MAX まで伸びたら強制的に切る", () => {
    const long = "あ".repeat(40); // 区切り無しの長い連続
    expect(firstChunkCut(long)).toBe(28);
  });

  it("短くて区切りも無ければ切らない（-1）", () => {
    expect(firstChunkCut("みじかい")).toBe(-1);
  });

  it("閉じ括弧類も早出しの区切りになる", () => {
    const s = "設定（詳しくは後述）を開きます";
    expect(firstChunkCut(s)).toBe(s.indexOf("）") + 1);
  });
});

describe("plainifyStreaming (fence またぎ)", () => {
  const mkFence = () => {
    let v = false;
    return { get: () => v, set: (nv: boolean) => (v = nv) };
  };

  it("1 チャンク内で閉じたコードブロックを丸ごと落とす", () => {
    const f = mkFence();
    expect(plainifyStreaming("前\n```\nconst x=1;\n```\n後", f).replace(/\s+/g, "")).toBe("前後");
    expect(f.get()).toBe(false); // 閉じたので状態はリセット
  });

  it("チャンクをまたぐコードブロックを状態引き回しで抑止する", () => {
    const f = mkFence();
    // 1 チャンク目でフェンスを開く（未閉）
    const a = plainifyStreaming("説明。\n```js", f);
    expect(a).toBe("説明。"); // 開きフェンス以降は落ちる
    expect(f.get()).toBe(true); // fence 継続中
    // 2 チャンク目はコード本体 → 全部落ちる
    const b = plainifyStreaming("const y = 2;", f);
    expect(b).toBe("");
    expect(f.get()).toBe(true);
    // 3 チャンク目で閉じる → 閉じた後の散文だけ残る
    const c = plainifyStreaming("```\nおわり", f);
    expect(c.replace(/\s+/g, "")).toBe("おわり");
    expect(f.get()).toBe(false);
  });
});
