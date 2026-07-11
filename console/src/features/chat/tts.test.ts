import { describe, it, expect } from "vitest";
import { plainify, plainifyStreaming, firstChunkCut, parseUserDict, applyUserDict } from "./ttsText.ts";
import { makeAudioLru } from "./ttsCache.ts";

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

describe("parseUserDict / applyUserDict (ユーザー読み仮名辞書)", () => {
  it("表記=読み をパースする（全角＝・空行・コメントを許容）", () => {
    const raw = "GPT-4=ジーピーティーフォー\n\n# コメント\n神＝かみ\n  k-k1 = ケーケーワン ";
    expect(parseUserDict(raw)).toEqual([
      // 長い表記から（長さ降順）
      ["GPT-4", "ジーピーティーフォー"],
      ["k-k1", "ケーケーワン"],
      ["神", "かみ"],
    ]);
  });

  it("区切りが無い/表記が空の行は捨てる。読みは空でも可", () => {
    expect(parseUserDict("区切りなし\n=読みだけ\nfoo=")).toEqual([["foo", ""]]);
  });

  it("空文字列は空配列", () => {
    expect(parseUserDict("")).toEqual([]);
  });

  it("リテラル置換で全出現を置き換える", () => {
    const d = parseUserDict("GPT-4=ジーピーティーフォー");
    expect(applyUserDict("GPT-4 と GPT-4 は同じ", d)).toBe("ジーピーティーフォー と ジーピーティーフォー は同じ");
  });

  it("長い表記を先に当てる（部分一致に食われない）", () => {
    const d = parseUserDict("AB=エービー\nABC=エービーシー");
    expect(applyUserDict("ABC", d)).toBe("エービーシー");
  });

  it("読みが空なら読み飛ばす（除去）", () => {
    const d = parseUserDict("(社外秘)=");
    expect(applyUserDict("これは(社外秘)です", d)).toBe("これはです");
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

describe("makeAudioLru (合成キャッシュの LRU)", () => {
  const buf = (duration: number) => ({ duration });

  it("put/get の基本と、無いキーは undefined", () => {
    const c = makeAudioLru<{ duration: number }>(10);
    const a = buf(3);
    c.put("a", a);
    expect(c.get("a")).toBe(a);
    expect(c.get("b")).toBeUndefined();
    expect(c.size()).toBe(1);
  });

  it("合計秒数が上限を超えたら古いものからエビクト", () => {
    const c = makeAudioLru<{ duration: number }>(10);
    c.put("a", buf(4));
    c.put("b", buf(4));
    c.put("c", buf(4)); // 12 > 10 → a を捨てて 8
    expect(c.get("a")).toBeUndefined();
    expect(c.get("b")).toBeDefined();
    expect(c.get("c")).toBeDefined();
  });

  it("get で触ったエントリは最新扱いになり、エビクトを免れる", () => {
    const c = makeAudioLru<{ duration: number }>(10);
    c.put("a", buf(4));
    c.put("b", buf(4));
    c.get("a"); // a を末尾へ
    c.put("c", buf(4)); // 最古は b → b が消える
    expect(c.get("a")).toBeDefined();
    expect(c.get("b")).toBeUndefined();
    expect(c.get("c")).toBeDefined();
  });

  it("単体で上限を超える値と重複キーは入れない", () => {
    const c = makeAudioLru<{ duration: number }>(10);
    c.put("big", buf(11));
    expect(c.get("big")).toBeUndefined();
    const first = buf(2);
    c.put("dup", first);
    c.put("dup", buf(3)); // 二重 put は無視（合計秒数を壊さない）
    expect(c.get("dup")).toBe(first);
    expect(c.size()).toBe(1);
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
