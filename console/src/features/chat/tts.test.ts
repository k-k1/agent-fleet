import { describe, it, expect } from "vitest";
import {
  plainify,
  plainifyStreaming,
  firstChunkCut,
  parseUserDict,
  applyUserDict,
  mergeDicts,
  splitSentences,
  abbrevCode,
  isBareHash,
  pauseParticles,
  applyBuiltinReadings,
  applyReadings,
  emotionOf,
  pendingSpeech,
  startsBlock,
} from "./ttsText.ts";
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

describe("startsBlock (ブロック頭の判定 = 前拍を置く合図)", () => {
  it("リスト・番号リスト・見出し・引用の頭に一致する", () => {
    expect(startsBlock("- 項目A")).toBe(true);
    expect(startsBlock("  * ネスト項目")).toBe(true);
    expect(startsBlock("1. 手順")).toBe(true);
    expect(startsBlock("## 見出し")).toBe(true);
    expect(startsBlock("> 引用")).toBe(true);
  });

  it("普通の文・ハイフンだけの語・マイナス値には一致しない", () => {
    expect(startsBlock("次の手順です。")).toBe(false);
    expect(startsBlock("-1 が返ります")).toBe(false);
    expect(startsBlock("run-dev.sh を実行")).toBe(false);
  });
});

describe("pendingSpeech (保留中の質問の読み上げ文)", () => {
  it("質問文＋選択肢（説明文優先）を番号つきで読む", () => {
    const t = pendingSpeech([
      {
        question: "どの方式にしますか？",
        options: [
          { label: "案A", description: "設定を専用タブに分離する" },
          { label: "案B" },
        ],
      },
    ]);
    expect(t).toBe("確認です。どの方式にしますか？選択肢は2つ。1、設定を専用タブに分離する。2、案B。");
  });

  it("複数質問は番号を振り、multiSelect は補足する", () => {
    const t = pendingSpeech([
      { question: "対象は？", multiSelect: true, options: [{ label: "全部" }] },
      { question: "いつ？" },
    ]);
    expect(t).toBe("確認です。質問1。対象は？選択肢は1つ。1、全部。複数選択できます。質問2。いつ？");
  });

  it("Markdown 断片は plainify される", () => {
    const t = pendingSpeech([{ question: "`main` にマージしますか？", options: [] }]);
    expect(t).toBe("確認です。main にマージしますか？");
  });
});

describe("emotionOf (文の感情推定)", () => {
  it("エラー・失敗系は angry", () => {
    expect(emotionOf("テストが失敗しました。")).toBe("angry");
    expect(emotionOf("ビルドでエラーが出ています。")).toBe("angry");
    expect(emotionOf("3 tests FAILED")).toBe("angry"); // 英語は小文字化して判定
  });

  it("成功・完了系は happy", () => {
    expect(emotionOf("マージが完了しました。")).toBe("happy");
    expect(emotionOf("テストは全部 green です。")).toBe("happy");
  });

  it("混在は angry 優先、どちらも無ければ null", () => {
    expect(emotionOf("修正は完了しましたがテストが失敗しています。")).toBe("angry");
    expect(emotionOf("次にドキュメントを更新します。")).toBeNull();
  });
});

describe("mergeDicts (ユーザー辞書＋テナント共通辞書の合成)", () => {
  it("同じ表記はユーザー辞書が勝つ（読み飛ばし上書きも含む）", () => {
    const user = parseUserDict("神=かみ\n(社外秘)=");
    const tenant = parseUserDict("神=しん\n(社外秘)=しゃがいひ\nagent-fleet=エージェントフリート");
    const m = mergeDicts(user, tenant);
    expect(applyUserDict("神は(社外秘)のagent-fleet", m)).toBe("かみはのエージェントフリート");
  });

  it("合成後も長い表記から当たる（テナント側の長い表記が部分一致に食われない）", () => {
    const user = parseUserDict("AB=エービー");
    const tenant = parseUserDict("ABC=エービーシー");
    expect(applyUserDict("ABC", mergeDicts(user, tenant))).toBe("エービーシー");
  });

  it("テナント辞書が空ならユーザー辞書そのまま", () => {
    const user = parseUserDict("神=かみ");
    expect(mergeDicts(user, [])).toBe(user);
  });
});

describe("abbrevCode (インラインコードの省略読み)", () => {
  const F = "(なんとか|ふがふが|むにゅむにゅ)";

  it("語が無いハッシュ等は頭 2 文字＋フィラー", () => {
    expect(abbrevCode("e79853e")).toMatch(new RegExp(`^e7 ${F}$`));
    expect(abbrevCode("1c74d26")).toMatch(new RegExp(`^1c ${F}$`));
    expect(abbrevCode("v1.2.30")).toMatch(new RegExp(`^v1 ${F}$`));
  });

  it("フィラーはトークンから決定的に選ぶ（毎回同じ）", () => {
    expect(abbrevCode("e79853e")).toBe(abbrevCode("e79853e"));
  });

  it("camelCase 2 語は頭一語＋フィラー", () => {
    expect(abbrevCode("ttsEnabled")).toMatch(new RegExp(`^tts ${F}$`));
  });

  it("camelCase 3 語以上は頭一語＋フィラー＋末尾一語", () => {
    expect(abbrevCode("ttsAutoReadMirror")).toMatch(new RegExp(`^tts ${F} Mirror$`));
  });

  it("区切り記号の語も同じ扱い（ファイル名・パス）", () => {
    expect(abbrevCode("run-dev.sh")).toMatch(new RegExp(`^run ${F} sh$`));
    expect(abbrevCode("console/src/features/mirror/turnTts.ts")).toMatch(new RegExp(`^console ${F} ts$`));
  });

  it("そのまま読むもの: 短い・純粋な 1 単語・空白入り・日本語入り", () => {
    expect(abbrevCode("main")).toBe("main");
    expect(abbrevCode("EC2")).toBe("EC2");
    expect(abbrevCode("vitest")).toBe("vitest");
    expect(abbrevCode("git push origin main")).toBe("git push origin main");
    expect(abbrevCode("「読み上げ」タブ")).toBe("「読み上げ」タブ");
  });

  it("ユーザー辞書に掛かるトークンは触らない（辞書優先）", () => {
    const d = parseUserDict("e79853e=れいのコミット");
    expect(abbrevCode("e79853e", d)).toBe("e79853e");
  });

  it("plainify に組み込み（code 指定時のみ効く）", () => {
    expect(plainify("コミット `e79853e` を見て", { abbrev: true, dict: [] })).toMatch(
      new RegExp(`^コミット e7 ${F} を見て$`),
    );
    expect(plainify("コミット `e79853e` を見て")).toBe("コミット e79853e を見て");
    expect(plainify("`e79853e` は", { abbrev: false, dict: [] })).toBe("e79853e は");
  });
});

describe("applyBuiltinReadings / applyReadings (組み込みの読み補正)", () => {
  it("空＋カタカナ語・空文字列などは「から」で読む", () => {
    expect(applyBuiltinReadings("空レポを作成")).toBe("からレポを作成");
    expect(applyBuiltinReadings("空リストと空文字列")).toBe("からリストとから文字列");
    expect(applyBuiltinReadings("空行を削除")).toBe("からぎょうを削除");
  });

  it("熟語・ひらがな続きの空は触らない", () => {
    expect(applyBuiltinReadings("空が青い")).toBe("空が青い");
    expect(applyBuiltinReadings("航空券と空港と空白")).toBe("航空券と空港と空白");
  });

  it("ヌメロニム（i18n 等）は元の語の慣用カタカナで読む（大文字小文字不問・単語境界）", () => {
    expect(applyBuiltinReadings("I18n対応とa11yの改善")).toBe("インターナショナリゼーション対応とアクセシビリティの改善");
    expect(applyBuiltinReadings("l10n と e2e テスト")).toBe("ローカリゼーション と エンドツーエンド テスト");
    expect(applyBuiltinReadings("k8sクラスタ")).toBe("クーバネティスクラスタ");
    expect(applyBuiltinReadings("v12n と o11y と p13n")).toBe(
      "バーチャライゼーション と オブザーバビリティ と パーソナライゼーション",
    );
    expect(applyBuiltinReadings("mai18nx")).toBe("mai18nx"); // 語中は触らない
  });

  it("大文字の IT はアイティー（小文字の it = 英語の代名詞は触らない）", () => {
    expect(applyBuiltinReadings("IT業界のIT投資")).toBe("アイティー業界のアイティー投資");
    expect(applyBuiltinReadings("do it now")).toBe("do it now");
    expect(applyBuiltinReadings("GITHUB")).toBe("GITHUB"); // 語中は触らない
  });

  it("ユーザー辞書が先に当たれば組み込みより優先される", () => {
    const dict = parseUserDict("空レポ=そらレポ");
    expect(applyReadings("空レポです", dict, false)).toBe("そらレポです");
  });

  it("applyReadings は 辞書 → 読み補正 → 助詞の小休止 の順で通す", () => {
    expect(applyReadings("空配列を確認", [], true)).toBe("から配列を、確認");
  });
});

describe("pauseParticles (助詞＋漢字の小休止)", () => {
  it("を・は・で・に・と の直後に漢字が続くとき読点を入れる", () => {
    expect(pauseParticles("神は細部に宿る")).toBe("神は、細部に、宿る");
    expect(pauseParticles("設定を保存して再起動")).toBe("設定を、保存して再起動");
    expect(pauseParticles("ミラーで再生とターミナルで停止")).toBe("ミラーで、再生とターミナルで、停止");
  });

  it("ひらがな・カタカナ・句読点が続くときは入れない", () => {
    expect(pauseParticles("これはそのままです")).toBe("これはそのままです");
    expect(pauseParticles("画面にカーソルを、")).toBe("画面にカーソルを、");
    expect(pauseParticles("ペインとタブ")).toBe("ペインとタブ");
  });
});

describe("isBareHash / 裸のハッシュの省略読み", () => {
  const F = "(なんとか|ふがふが|むにゅむにゅ)";

  it("16 進ハッシュにしか見えないトークンだけを真にする", () => {
    expect(isBareHash("f437e17")).toBe(true); // git 短縮ハッシュ
    expect(isBareHash("b2d7fac36b996a9ae6245c188b51c4dbac2c9aef")).toBe(true); // フル SHA
    expect(isBareHash("28733db5-247d-5fd9-a6b9-6d64d685412a")).toBe(true); // UUID
    expect(isBareHash("deadbeef")).toBe(false); // 英字のみ（英単語かもしれない）
    expect(isBareHash("facade")).toBe(false); // 英字のみ＋7 文字未満
    expect(isBareHash("1783780316")).toBe(false); // 数字のみ（トークン数・時刻等）
    expect(isBareHash("F437E17")).toBe(false); // 大文字は対象外（git は小文字）
    expect(isBareHash("abc123")).toBe(false); // 6 文字（短縮ハッシュの下限未満）
  });

  it("plainify: 地の文の裸ハッシュを省略読みにする（ttsAbbrevCode でゲート）", () => {
    const code = { abbrev: true, dict: [] as [string, string][] };
    expect(plainify("前回マージ f437e17 以降の 8 コミット", code)).toMatch(
      new RegExp(`^前回マージ f4 ${F} 以降の 8 コミット$`),
    );
    expect(plainify("前回マージ f437e17 以降の")).toBe("前回マージ f437e17 以降の"); // 省略読み OFF
    expect(plainify("値は 1783780316 です", code)).toBe("値は 1783780316 です"); // 長い数値はそのまま
    expect(plainify("facade を deadbeef に", code)).toBe("facade を deadbeef に"); // 英単語はそのまま
  });

  it("辞書に掛かる表記は触らない（置換は後段の applyUserDict）", () => {
    const dict = parseUserDict("f437e17=まえのマージ");
    expect(plainify("f437e17 を見て", { abbrev: true, dict })).toBe("f437e17 を見て");
  });

  it("範囲表記や句読点に隣接しても各ハッシュを拾う", () => {
    const code = { abbrev: true, dict: [] as [string, string][] };
    expect(plainify("f437e17..0415b6a を比較。", code)).toMatch(new RegExp(`^f4 ${F}..04 ${F} を比較。$`));
  });

  it("長い SHA はインラインコードでも頭 2 文字＋フィラー（語の誤認をしない）", () => {
    expect(abbrevCode("b2d7fac36b996a9ae6245c188b51c4dbac2c9aef")).toMatch(new RegExp(`^b2 ${F}$`));
  });
});

describe("splitSentences (レンダ済みテキストの文分割)", () => {
  it("句点で分割し、句点は前の文に残す", () => {
    expect(splitSentences("これは一文目。二文目です。")).toEqual(["これは一文目。", "二文目です。"]);
  });

  it("末尾の句点なし断片も 1 文として返す", () => {
    expect(splitSentences("読点だけで、終わる断片")).toEqual(["読点だけで、終わる断片"]);
  });

  it("改行・連続空白は 1 つの空白に潰す", () => {
    expect(splitSentences("一行目\n  二行目です。")).toEqual(["一行目 二行目です。"]);
  });

  it("かな/漢字/英数字を含まない断片（記号だけ）は捨てる", () => {
    expect(splitSentences("---")).toEqual([]);
    expect(splitSentences("読む。※")).toEqual(["読む。"]);
  });

  it("空文字列は空配列", () => {
    expect(splitSentences("")).toEqual([]);
  });
});

describe("makeAudioLru (合成キャッシュの LRU)", () => {
  const buf = (duration: number) => ({ duration });

  it("put/get の基本と、無いキーは undefined", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    const a = buf(3);
    c.put("a", a);
    expect(c.get("a")).toBe(a);
    expect(c.get("b")).toBeUndefined();
    expect(c.size()).toBe(1);
  });

  it("合計秒数が上限を超えたら古いものからエビクト", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    c.put("a", buf(4));
    c.put("b", buf(4));
    c.put("c", buf(4)); // 12 > 10 → a を捨てて 8
    expect(c.get("a")).toBeUndefined();
    expect(c.get("b")).toBeDefined();
    expect(c.get("c")).toBeDefined();
  });

  it("get で触ったエントリは最新扱いになり、エビクトを免れる", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    c.put("a", buf(4));
    c.put("b", buf(4));
    c.get("a"); // a を末尾へ
    c.put("c", buf(4)); // 最古は b → b が消える
    expect(c.get("a")).toBeDefined();
    expect(c.get("b")).toBeUndefined();
    expect(c.get("c")).toBeDefined();
  });

  it("単体で上限を超える値と重複キーは入れない", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    c.put("big", buf(11));
    expect(c.get("big")).toBeUndefined();
    const first = buf(2);
    c.put("dup", first);
    c.put("dup", buf(3)); // 二重 put は無視（合計秒数を壊さない）
    expect(c.get("dup")).toBe(first);
    expect(c.size()).toBe(1);
  });

  it("上限の変更に追随する（下げたら put 時に縮小、0 で無効化＋破棄）", () => {
    let max = 10;
    const c = makeAudioLru<{ duration: number }>(() => max);
    c.put("a", buf(4));
    c.put("b", buf(4));
    max = 4; // 設定を下げる → 次の put で合計 4 まで縮む
    c.put("c", buf(4)); // c 自体は入り、a/b が捨てられる
    expect(c.get("a")).toBeUndefined();
    expect(c.get("b")).toBeUndefined();
    expect(c.get("c")).toBeDefined();
    max = 0; // 無効化 → get は必ずミスし保持分も手放す
    expect(c.get("c")).toBeUndefined();
    expect(c.size()).toBe(0);
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
