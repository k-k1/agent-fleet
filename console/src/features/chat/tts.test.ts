import { describe, it, expect } from "vitest";
import {
  plainify,
  plainifyStreaming,
  firstChunkCut,
  parseUserDict,
  applyUserDict,
  mergeDicts,
  splitSentences,
  splitLongSentence,
  abbrevCode,
  abbrevPath,
  CODE_FILLERS,
  PATH_FILLERS,
  isBareHash,
  pauseParticles,
  applyBuiltinReadings,
  applyReadings,
  emotionOf,
  pendingSpeech,
  startsBlock,
  startsTame,
} from "./ttsText.ts";
import { makeAudioLru } from "./ttsCache.ts";
import {
  HIDDEN_TTS_GAIN,
  MAX_PANE_PAN,
  stopTtsForReplacement,
  ttsIsBackground,
  ttsMasterGain,
  ttsPanePan,
  type TtsController,
} from "./ttsControl.ts";
import type { Layout, Pane } from "../../layout/types.ts";

describe("TTS controller replacement", () => {
  it("an internal playback swap passes replaced, not an explicit stop", () => {
    const reasons: Array<string | undefined> = [];
    const controller: TtsController = {
      push() {},
      flush() {},
      stop: (reason) => reasons.push(reason),
    };

    stopTtsForReplacement(controller);
    stopTtsForReplacement(null);

    expect(reasons).toEqual(["replaced"]);
  });
});

describe("TTS master gain", () => {
  it("treats hidden or unfocused as background", () => {
    expect(ttsIsBackground(true, true)).toBe(true);
    expect(ttsIsBackground(false, false)).toBe(true);
    expect(ttsIsBackground(false, true)).toBe(false);
  });

  it("lowers the volume only when the setting is on and in background", () => {
    expect(ttsMasterGain("quiet", true)).toBe(HIDDEN_TTS_GAIN);
    expect(ttsMasterGain("quiet", false)).toBe(1);
    expect(ttsMasterGain("normal", true)).toBe(1);
    expect(ttsMasterGain("mute", true)).toBe(0);
  });

  it("quiet volume follows the ttsBackgroundVolume slider and does not affect mute or normal", () => {
    expect(ttsMasterGain("quiet", true, 0.6)).toBe(0.6);
    expect(ttsMasterGain("quiet", true, 0)).toBe(0); // effectively muted
    expect(ttsMasterGain("quiet", true, 5)).toBe(1); // clamped to 0..1
    expect(ttsMasterGain("mute", true, 0.6)).toBe(0); // mute is always silent
    expect(ttsMasterGain("normal", true, 0.6)).toBe(1); // normal is always full volume
    expect(ttsMasterGain("quiet", false, 0.6)).toBe(1); // not in background: always full volume
  });
});

describe("TTS pane stereo pan", () => {
  const pane = (id: string): Pane => ({
    id,
    session: null,
    content: { kind: "terminal", chat: false },
    wrap: null,
  });
  const layout: Layout = {
    version: 3,
    mode: "split",
    cols: [
      { id: "left-col", rowRatio: 0.5, cells: [pane("left-top"), pane("left-bottom")].map((v) => ({ id: "g-" + v.id, selectedViewId: v.id, views: [v] })) },
      { id: "center-col", rowRatio: 0.5, cells: [pane("center")].map((v) => ({ id: "g-" + v.id, selectedViewId: v.id, views: [v] })) },
      { id: "right-col", rowRatio: 0.5, cells: [pane("right")].map((v) => ({ id: "g-" + v.id, selectedViewId: v.id, views: [v] })) },
    ],
    colRatios: [0.25, 0.5, 0.25],
    activeCellId: "g-center",
  };

  it("pans the outer columns to the extremes and places the middle one at centre", () => {
    expect(ttsPanePan(true, layout, "left-top")).toBe(-MAX_PANE_PAN);
    expect(ttsPanePan(true, layout, "left-bottom")).toBe(-MAX_PANE_PAN);
    expect(ttsPanePan(true, layout, "center")).toBeCloseTo(0);
    expect(ttsPanePan(true, layout, "right")).toBe(MAX_PANE_PAN);
  });

  it("plays centred when the setting is off, the pane is unknown, or there is one column", () => {
    expect(ttsPanePan(false, layout, "left-top")).toBe(0);
    expect(ttsPanePan(true, layout, "missing")).toBe(0);
    expect(
      ttsPanePan(true, { ...layout, cols: [layout.cols[0]], colRatios: [1] }, "left-top"),
    ).toBe(0);
  });
});

describe("plainify (flattening text for reading)", () => {
  it("drops inline code, links, URLs and emphasis", () => {
    expect(plainify("これは `code` です")).toBe("これは code です");
    expect(plainify("詳しくは [ドキュメント](https://x.example) を見て")).toBe("詳しくは ドキュメント を見て");
    // the double space left where the URL was collapses to one
    expect(plainify("生URL https://a.example/b は読まない")).toBe("生URL は読まない");
    expect(plainify("**強調** と *斜体* と ~~取消~~")).toBe("強調 と 斜体 と 取消");
  });

  it("removes line-start markers (heading, quote, list)", () => {
    expect(plainify("## 見出し")).toBe("見出し");
    expect(plainify("- 項目A")).toBe("項目A");
    expect(plainify("> 引用文")).toBe("引用文");
  });

  it("a held beat at line start is a marker and is dropped from the reading", () => {
    expect(plainify("————また、イく。")).toBe("また、イく。"); // reported from a real device
    expect(plainify("——イって、る。")).toBe("イって、る。");
    expect(plainify("……一日中、って。")).toBe("一日中、って。"); // reported from a real device (ellipsis)
    expect(plainify("...本当に？")).toBe("本当に？"); // a run of half-width ellipsis dots
    expect(plainify("普通の文の中の—区切り")).toBe("普通の文の中の—区切り"); // not touched anywhere but at line start
  });

  it("drops images", () => {
    expect(plainify("図 ![alt](https://x/y.png) を参照")).toBe("図 を参照");
  });
});

describe("parseUserDict / applyUserDict (the user pronunciation dictionary)", () => {
  it("parses spelling=reading, allowing a full-width equals sign, blank lines and comments", () => {
    const raw = "GPT-4=ジーピーティーフォー\n\n# コメント\n神＝かみ\n  k-k1 = ケーケーワン ";
    expect(parseUserDict(raw)).toEqual([
      // longest spelling first
      ["GPT-4", "ジーピーティーフォー"],
      ["k-k1", "ケーケーワン"],
      ["神", "かみ"],
    ]);
  });

  it("drops a line with no separator or an empty spelling; an empty reading is allowed", () => {
    expect(parseUserDict("区切りなし\n=読みだけ\nfoo=")).toEqual([["foo", ""]]);
  });

  it("an empty string gives an empty array", () => {
    expect(parseUserDict("")).toEqual([]);
  });

  it("replaces every occurrence, literally", () => {
    const d = parseUserDict("GPT-4=ジーピーティーフォー");
    expect(applyUserDict("GPT-4 と GPT-4 は同じ", d)).toBe("ジーピーティーフォー と ジーピーティーフォー は同じ");
  });

  it("applies longer spellings first, so a partial match cannot swallow them", () => {
    const d = parseUserDict("AB=エービー\nABC=エービーシー");
    expect(applyUserDict("ABC", d)).toBe("エービーシー");
  });

  it("an empty reading skips the word, removing it", () => {
    const d = parseUserDict("(社外秘)=");
    expect(applyUserDict("これは(社外秘)です", d)).toBe("これはです");
  });
});

describe("firstChunkCut (getting the first utterance out early)", () => {
  it("cuts at a comma once it is at or past FIRST_MIN", () => {
    // "これは長めの前置きで、" -> cut at the comma (11th character)
    const s = "これは長めの前置きで、続きます";
    expect(firstChunkCut(s)).toBe(s.indexOf("、") + 1);
  });

  it("does not cut at a comma that comes before FIRST_MIN", () => {
    // the comma in "はい、" is the 3rd character, too early to cut
    expect(firstChunkCut("はい、まだ短い")).toBe(-1);
  });

  it("forces a cut at FIRST_MAX even with no break", () => {
    const long = "あ".repeat(40); // a long run with no break
    expect(firstChunkCut(long)).toBe(28);
  });

  it("does not cut when the text is short and has no break (-1)", () => {
    expect(firstChunkCut("みじかい")).toBe(-1);
  });

  it("a closing bracket also counts as an early-cut break", () => {
    const s = "設定（詳しくは後述）を開きます";
    expect(firstChunkCut(s)).toBe(s.indexOf("）") + 1);
  });
});

describe("startsBlock (detecting a block head = the cue for a leading beat)", () => {
  it("matches the head of a list, numbered list, heading or quote", () => {
    expect(startsBlock("- 項目A")).toBe(true);
    expect(startsBlock("  * ネスト項目")).toBe(true);
    expect(startsBlock("1. 手順")).toBe(true);
    expect(startsBlock("## 見出し")).toBe(true);
    expect(startsBlock("> 引用")).toBe(true);
  });

  it("does not match an ordinary sentence, a bare hyphen word, or a negative number", () => {
    expect(startsBlock("次の手順です。")).toBe(false);
    expect(startsBlock("-1 が返ります")).toBe(false);
    expect(startsBlock("run-dev.sh を実行")).toBe(false);
  });
});

describe("startsTame (detecting a held beat = the cue for a longer leading beat)", () => {
  it("matches a run of dashes at line start", () => {
    expect(startsTame("――また、行く。")).toBe(true);
    expect(startsTame("————また、イく。")).toBe(true); // a run of em dashes
    expect(startsTame("——イって、る。")).toBe(true);
    expect(startsTame("―行く。")).toBe(true); // one is enough to match
  });

  it("also matches a run of ellipses at line start", () => {
    expect(startsTame("……一日中、って。")).toBe(true); // reported from a real device
    expect(startsTame("...本当に？")).toBe(true); // a run of half-width ellipsis dots
    expect(startsTame("…")).toBe(false); // nothing follows (end of line)
  });

  it("does not match a trailing lengthener (followed by a space or end of line) or a bare hyphen word", () => {
    expect(startsTame("普通の文です。")).toBe(false);
    expect(startsTame("-1 が返ります")).toBe(false); // a half-width hyphen is out of scope (it would clash with BLOCK_HEAD)
    expect(startsTame("―― ")).toBe(false); // followed by a space or the end of the line
    expect(startsTame("")).toBe(false);
  });
});

describe("pendingSpeech (spoken text for a pending question)", () => {
  it("reads the question plus numbered options, preferring the description", () => {
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

  it("numbers multiple questions and notes multiSelect", () => {
    const t = pendingSpeech([
      { question: "対象は？", multiSelect: true, options: [{ label: "全部" }] },
      { question: "いつ？" },
    ]);
    expect(t).toBe("確認です。質問1。対象は？選択肢は1つ。1、全部。複数選択できます。質問2。いつ？");
  });

  it("a Markdown fragment goes through plainify", () => {
    const t = pendingSpeech([{ question: "`main` にマージしますか？", options: [] }]);
    expect(t).toBe("確認です。main にマージしますか？");
  });
});

describe("emotionOf (guessing a sentence's emotion)", () => {
  it("error and failure wording gives angry", () => {
    expect(emotionOf("テストが失敗しました。")).toBe("angry");
    expect(emotionOf("ビルドでエラーが出ています。")).toBe("angry");
    expect(emotionOf("3 tests FAILED")).toBe("angry"); // English is lower-cased before matching
  });

  it("success and completion wording gives happy", () => {
    expect(emotionOf("マージが完了しました。")).toBe("happy");
    expect(emotionOf("テストは全部 green です。")).toBe("happy");
  });

  it("a mix prefers angry, and neither gives null", () => {
    expect(emotionOf("修正は完了しましたがテストが失敗しています。")).toBe("angry");
    expect(emotionOf("次にドキュメントを更新します。")).toBeNull();
  });
});

describe("mergeDicts (user dictionary merged with the tenant-wide one)", () => {
  it("on the same spelling the user dictionary wins, including an override that skips the word", () => {
    const user = parseUserDict("神=かみ\n(社外秘)=");
    const tenant = parseUserDict("神=しん\n(社外秘)=しゃがいひ\nagent-fleet=エージェントフリート");
    const m = mergeDicts(user, tenant);
    expect(applyUserDict("神は(社外秘)のagent-fleet", m)).toBe("かみはのエージェントフリート");
  });

  it("after merging, longer spellings still match first, so a long tenant entry is not swallowed by a partial match", () => {
    const user = parseUserDict("AB=エービー");
    const tenant = parseUserDict("ABC=エービーシー");
    expect(applyUserDict("ABC", mergeDicts(user, tenant))).toBe("エービーシー");
  });

  it("an empty tenant dictionary leaves the user dictionary as-is", () => {
    const user = parseUserDict("神=かみ");
    expect(mergeDicts(user, [])).toBe(user);
  });
});

describe("abbrevCode (abbreviated reading of inline code)", () => {
  const F = `(${CODE_FILLERS.join("|")})`;

  it("a hash with no words reads as its first 2 characters plus a filler", () => {
    expect(abbrevCode("e79853e")).toMatch(new RegExp(`^e7 ${F}$`));
    expect(abbrevCode("1c74d26")).toMatch(new RegExp(`^1c ${F}$`));
    expect(abbrevCode("v1.2.30")).toMatch(new RegExp(`^v1 ${F}$`));
  });

  it("the filler is chosen deterministically from the token, the same every time", () => {
    expect(abbrevCode("e79853e")).toBe(abbrevCode("e79853e"));
  });

  it("two camelCase words read as the first word plus a filler", () => {
    expect(abbrevCode("ttsEnabled")).toMatch(new RegExp(`^tts ${F}$`));
  });

  it("three or more camelCase words read as first word, filler, last word", () => {
    expect(abbrevCode("ttsAutoReadMirror")).toMatch(new RegExp(`^tts ${F} Mirror$`));
  });

  it("words split by punctuation are handled the same way (file names, paths)", () => {
    expect(abbrevCode("run-dev.sh")).toMatch(new RegExp(`^run ${F} sh$`));
    expect(abbrevCode("console/src/features/mirror/turnTts.ts")).toMatch(new RegExp(`^console ${F} ts$`));
  });

  it("read verbatim: short tokens, a plain single word, anything with whitespace, anything with Japanese", () => {
    expect(abbrevCode("main")).toBe("main");
    expect(abbrevCode("EC2")).toBe("EC2");
    expect(abbrevCode("vitest")).toBe("vitest");
    expect(abbrevCode("git push origin main")).toBe("git push origin main");
    expect(abbrevCode("「読み上げ」タブ")).toBe("「読み上げ」タブ");
  });

  it("a token the user dictionary covers is left alone; the dictionary wins", () => {
    const d = parseUserDict("e79853e=れいのコミット");
    expect(abbrevCode("e79853e", d)).toBe("e79853e");
  });

  it("wired into plainify, and only active when code is given", () => {
    expect(plainify("コミット `e79853e` を見て", { abbrev: true, dict: [] })).toMatch(
      new RegExp(`^コミット e7 ${F} を見て$`),
    );
    expect(plainify("コミット `e79853e` を見て")).toBe("コミット e79853e を見て");
    expect(plainify("`e79853e` は", { abbrev: false, dict: [] })).toBe("e79853e は");
  });
});

describe("abbrevPath (abbreviated reading of a bare path)", () => {
  const F = `(${PATH_FILLERS.join("|")})`;

  it("a path of three or more segments folds to head, filler and file name, separated by commas", () => {
    expect(abbrevPath("console/src/features/chat/tts.ts")).toMatch(new RegExp(`^console、${F}、tts.ts$`));
    expect(abbrevPath("/home/dev/repos/agent-fleet/tts.go")).toMatch(new RegExp(`^home、${F}、tts.go$`));
    expect(abbrevPath("./src/features/store.ts")).toMatch(new RegExp(`^src、${F}、store.ts$`));
    expect(abbrevPath("../a/b/c")).toMatch(new RegExp(`^a、${F}、c$`));
  });

  it("two segments or fewer, and all-numeric runs such as dates, are not folded", () => {
    expect(abbrevPath("src/main.ts")).toBe("src/main.ts");
    expect(abbrevPath("2024/01/02")).toBe("2024/01/02");
  });

  it("the filler is chosen deterministically from the path, the same every time", () => {
    expect(abbrevPath("a/b/c/d")).toBe(abbrevPath("a/b/c/d"));
  });

  it("wired into plainify for bare paths, only when abbreviation is on", () => {
    expect(plainify("編集は /home/dev/repos/agent-fleet/tts.go です", { abbrev: true, dict: [] })).toMatch(
      new RegExp(`^編集は home、${F}、tts.go です$`),
    );
    // two segments (TCP/IP) and dates are not folded
    expect(plainify("2024/01/02 の変更", { abbrev: true, dict: [] })).toBe("2024/01/02 の変更");
    // left as-is when abbreviation is off
    expect(plainify("a/b/c/d.ts を見て", { abbrev: false, dict: [] })).toBe("a/b/c/d.ts を見て");
  });
});

describe("applyBuiltinReadings / applyReadings (the built-in reading corrections)", () => {
  it("空 before a katakana word, and 空文字列, are read から", () => {
    expect(applyBuiltinReadings("空レポを作成")).toBe("からレポを作成");
    expect(applyBuiltinReadings("空リストと空文字列")).toBe("からリストとから文字列");
    expect(applyBuiltinReadings("空行を削除")).toBe("からぎょうを削除");
  });

  it("空 inside a compound or before hiragana is left alone", () => {
    expect(applyBuiltinReadings("空が青い")).toBe("空が青い");
    expect(applyBuiltinReadings("航空券と空港と空白")).toBe("航空券と空港と空白");
  });

  it("the section sign § reads セクション and the number is kept as it is", () => {
    expect(applyBuiltinReadings("§3 を参照")).toBe("セクション3 を参照");
    expect(applyBuiltinReadings("§§ に記載")).toBe("セクション に記載");
  });

  it("fixed tokens spanning a period (init.d, cron.d, resolv.conf) are pinned to katakana", () => {
    expect(applyBuiltinReadings("init.d に配置")).toBe("イニットドットディー に配置");
    expect(applyBuiltinReadings("cron.d と rc.d と conf.d")).toBe(
      "クロンドットディー と アールシードットディー と コンフドットディー",
    );
    expect(applyBuiltinReadings("resolv.conf を編集")).toBe("リゾルブドットコンフ を編集");
    expect(applyBuiltinReadings("sudoers.d に置く")).toBe("スードゥアーズドットディー に置く");
    // word boundaries keep cron.d inside cron.daily from being replaced
    expect(applyBuiltinReadings("cron.daily")).toBe("cron.daily");
  });

  it("everyday development kanji terms are pinned to their usual IT reading", () => {
    expect(applyBuiltinReadings("引数と添字")).toBe("ひきすうとそえじ");
    expect(applyBuiltinReadings("閾値を超えたら相殺")).toBe("しきいちを超えたらそうさい");
    expect(applyBuiltinReadings("脆弱性と端数と冪等")).toBe("ぜいじゃくせいとはすうとべきとう");
  });

  it("a bare slash outside code becomes a middle dot, tightening the pause", () => {
    expect(applyBuiltinReadings("origin/main のブランチ")).toBe("origin・main のブランチ");
    expect(applyBuiltinReadings("on/off と read/write")).toBe("on・off と read・write");
    expect(applyBuiltinReadings("a/b/c の階層")).toBe("a・b・c の階層"); // a whole chain is converted at once
  });

  it("converts a date carrying a year and protects an unambiguous fraction context", () => {
    expect(applyBuiltinReadings("2024/01/02 の変更")).toBe("2024年1月2日 の変更");
    expect(applyBuiltinReadings("確率は1/2です")).toBe("確率は1/2です");
  });

  it("expands a date into its Japanese year/month/day reading", () => {
    expect(applyBuiltinReadings("2024-09-20 と2024/09/20、7/2")).toBe(
      "2024年9月20日 と2024年9月20日、7月2日",
    );
    expect(applyBuiltinReadings("2024-02-29")).toBe("2024年2月29日");
  });

  it("does not convert an impossible date, or an unambiguous fraction or arithmetic context", () => {
    expect(applyBuiltinReadings("2023-02-29、2023/02/29、2024/13/01、4/31")).toBe(
      "2023-02-29、2023/02/29、2024/13/01、4/31",
    );
    expect(applyBuiltinReadings("確率は1/2で、7/2倍、9/3を計算")).toBe("確率は1/2で、7/2倍、9/3を計算");
  });

  it("expands a time into its Japanese hour/minute/second reading", () => {
    expect(applyBuiltinReadings("12:34:56 と12:34、00:05")).toBe("12時34分56秒 と12時34分、0時5分");
    expect(applyBuiltinReadings("24:00、12:60、12:34:60")).toBe("24:00、12:60、12:34:60");
  });

  it("a numeronym such as i18n reads as the usual katakana of the original word, case-insensitively and on word boundaries", () => {
    expect(applyBuiltinReadings("I18n対応とa11yの改善")).toBe("インターナショナリゼーション対応とアクセシビリティの改善");
    expect(applyBuiltinReadings("l10n と e2e テスト")).toBe("ローカリゼーション と エンドツーエンド テスト");
    expect(applyBuiltinReadings("k8sクラスタ")).toBe("クーバネティスクラスタ");
    expect(applyBuiltinReadings("v12n と o11y と p13n")).toBe(
      "バーチャライゼーション と オブザーバビリティ と パーソナライゼーション",
    );
    expect(applyBuiltinReadings("mai18nx")).toBe("mai18nx"); // not touched mid-word
  });

  it("upper-case IT reads アイティー; lower-case it, the English pronoun, is left alone", () => {
    expect(applyBuiltinReadings("IT業界のIT投資")).toBe("アイティー業界のアイティー投資");
    expect(applyBuiltinReadings("do it now")).toBe("do it now");
    expect(applyBuiltinReadings("GITHUB")).toBe("GITHUB"); // not touched mid-word
  });

  it("the product name agent-fleet, in all its spellings, is pinned to エージェントフリート", () => {
    expect(applyBuiltinReadings("Agent-Fleetの機能")).toBe("エージェントフリートの機能");
    expect(applyBuiltinReadings("Agent fleetはCP-native")).toBe("エージェントフリートはCP-native");
    expect(applyBuiltinReadings("AgentFleetを使う")).toBe("エージェントフリートを使う");
  });

  it("a single wave dash marking a range reads から", () => {
    expect(applyBuiltinReadings("通常の3〜5倍速です")).toBe("通常の3から5倍速です");
    expect(applyBuiltinReadings("月〜金曜日")).toBe("月から金曜日");
    expect(applyBuiltinReadings("1～3個")).toBe("1から3個"); // the full-width tilde (U+FF5E) counts as the same mark
    expect(applyBuiltinReadings("3〜5〜7")).toBe("3から5から7"); // chained ranges
  });

  it("a run of wave dashes, the elision or hesitation use, reads ほにゃらら", () => {
    expect(applyBuiltinReadings("詳細は～～～省略します")).toBe("詳細はほにゃらら省略します");
    expect(applyBuiltinReadings("あとは〜〜適当に")).toBe("あとはほにゃらら適当に"); // two is enough to match
    expect(applyBuiltinReadings("混在～〜～も1個として畳む")).toBe("混在ほにゃららも1個として畳む");
  });

  it("a lone wave dash lengthening a word ending, before end of sentence, punctuation or a space, is left alone", () => {
    expect(applyBuiltinReadings("そうだね〜")).toBe("そうだね〜");
    expect(applyBuiltinReadings("そうだね〜。")).toBe("そうだね〜。");
    expect(applyBuiltinReadings("うんうん〜 まあいいか")).toBe("うんうん〜 まあいいか");
  });

  it("行 as line/row defaults to ぎょう, never くだり, and picks up the 行 suffix, digits + 行, and 行 + a particle", () => {
    expect(applyBuiltinReadings("3行目のバグ")).toBe("3ぎょうめのバグ");
    expect(applyBuiltinReadings("42行を削除")).toBe("42ぎょうを削除");
    expect(applyBuiltinReadings("行数と行末と行頭")).toBe("ぎょうすうとぎょうまつとぎょうとう");
    expect(applyBuiltinReadings("行全体を選択")).toBe("ぎょうぜんたいを選択");
    expect(applyBuiltinReadings("１０行目")).toBe("１０ぎょうめ"); // full-width digits
    expect(applyBuiltinReadings("集計行と統計行")).toBe("集計ぎょうと統計ぎょう"); // kanji + the 行 suffix
    expect(applyBuiltinReadings("WT 行を確認")).toBe("WT ぎょうを確認"); // "WT 行", reported from a real device
    expect(applyBuiltinReadings("この行が長い")).toBe("このぎょうが長い"); // 行 + a particle
    expect(applyBuiltinReadings("先頭行と最終行")).toBe("先頭ぎょうと最終ぎょう");
  });

  it("the ぎょう default spares こう compounds, 行動, and 行く / 行う", () => {
    // a kanji directly after (行動, 行政) is left to OpenJTalk
    expect(applyBuiltinReadings("行動と行政")).toBe("行動と行政");
    // okurigana forms (行く, 行う)
    expect(applyBuiltinReadings("現場に行く、処理を行う")).toBe("現場に行く、処理を行う");
    // blocklist of こう compounds (実行, 銀行, 移行, 並行)
    expect(applyBuiltinReadings("銀行で実行し移行を並行する")).toBe("銀行で実行し移行を並行する");
  });

  it("判定 is pinned to はんてい, so 誤判定 does not become ごはんてい", () => {
    expect(applyBuiltinReadings("誤判定を修正")).toBe("ごはんていを修正"); // the 誤 -> ご rule fires at the same time
    expect(applyBuiltinReadings("空判定と判定結果")).toBe("からはんていとはんてい結果");
  });

  it("the prefix 誤 before a kanji reads ご (誤表示 -> ごひょうじ); with okurigana, 誤る / 誤り stay あやま", () => {
    expect(applyBuiltinReadings("「5時間制限」と誤表示")).toBe("「5時間制限」とご表示"); // reported from a real device
    expect(applyBuiltinReadings("誤検知と誤動作と誤操作")).toBe("ご検知とご動作とご操作");
    // okurigana forms (the kun reading あやま) are left alone
    expect(applyBuiltinReadings("設定を誤ると誤りが出る")).toBe("設定を誤ると誤りが出る");
  });

  it("fixes mis-voiced readings: 貼り付け -> はりつけ, 型チェック -> かたチェック", () => {
    expect(applyBuiltinReadings("画像貼り付け")).toBe("画像はりつけ"); // reported from a real device
    expect(applyBuiltinReadings("ここに貼り付けると貼り付けた")).toBe("ここにはりつけるとはりつけた"); // prefix match
    expect(applyBuiltinReadings("TypeScript型チェック")).toBe("TypeScriptかたチェック"); // reported from a real device (the Latin part is handled by enkana on the CP side)
  });

  it("言って -> いって, guarding the sentence-final misreading, and a standalone 身体 -> からだ", () => {
    expect(applyBuiltinReadings("何する？　言って。")).toBe("何する？　いって。"); // reported from a real device
    expect(applyBuiltinReadings("身体を動かす")).toBe("からだを動かす"); // reported from a real device
    expect(applyBuiltinReadings("身体が資本で、身体そのものを鍛える")).toBe(
      "からだが資本で、からだそのものを鍛える",
    );
  });

  it("an on-reading compound of 身体 plus a kanji is not broken into からだ", () => {
    expect(applyBuiltinReadings("身体検査と身体能力と身体機能")).toBe(
      "身体検査と身体能力と身体機能",
    );
  });

  it("放ってお, the idiom for leaving something be, reads ほうってお; a standalone 放って is left alone, to keep 放つ = はなつ apart", () => {
    expect(applyBuiltinReadings("放っておく")).toBe("ほうっておく"); // reported from a real device
    expect(applyBuiltinReadings("放っておかれる子供")).toBe("ほうっておかれる子供");
    expect(applyBuiltinReadings("放っておいた")).toBe("ほうっておいた");
    // the standalone form could still be 放つ (解き放つ), so it stays はなって
    expect(applyBuiltinReadings("光を放って輝いた")).toBe("光を放って輝いた");
    expect(applyBuiltinReadings("矢を放って命中した")).toBe("矢を放って命中した");
  });

  it("要 after が/は/も and before end of sentence, punctuation or です/だ reads かなめ; compounds and 要る are left alone", () => {
    expect(applyBuiltinReadings("ここが要")).toBe("ここがかなめ"); // reported from a real device
    expect(applyBuiltinReadings("ここが要です")).toBe("ここがかなめです");
    expect(applyBuiltinReadings("そこが要だ。")).toBe("そこがかなめだ。");
    expect(applyBuiltinReadings("そこは要、次も要")).toBe("そこはかなめ、次もかなめ");
    // a compound (よう) has a kanji right after, so it is out of scope
    expect(applyBuiltinReadings("そこが要注意")).toBe("そこが要注意");
    expect(applyBuiltinReadings("それが要素です")).toBe("それが要素です");
    // 要る (いる = to need) is followed by an inflection, so it is out of scope
    expect(applyBuiltinReadings("許可が要る")).toBe("許可が要る");
    expect(applyBuiltinReadings("それは要らない")).toBe("それは要らない");
  });

  it("様な / 様に after この/その/あの/どの read よう, and あり様 reads ありよう", () => {
    expect(applyBuiltinReadings("その様な問題")).toBe("そのような問題"); // reported from a real device
    expect(applyBuiltinReadings("この様に考える")).toBe("このように考える");
    expect(applyBuiltinReadings("あの様なミス")).toBe("あのようなミス");
    expect(applyBuiltinReadings("どの様に進める？")).toBe("どのように進める？");
    expect(applyBuiltinReadings("あり様を見直す")).toBe("ありようを見直す"); // reported from a real device
    // compounds (様子, 様々) are not followed by な/に, so they are out of scope
    expect(applyBuiltinReadings("その様子と様々な意見")).toBe("その様子と様々な意見");
  });

  it("a held beat inside a sentence becomes a comma to make a pause; at line start startsTame and plainify handle it", () => {
    expect(applyBuiltinReadings("……一日中、って。")).toBe("、一日中、って。"); // reported from a real device
    expect(applyBuiltinReadings("そして――彼は言った。")).toBe("そして、彼は言った。");
    expect(applyBuiltinReadings("分かった…でも心配だ")).toBe("分かった、でも心配だ");
    expect(applyBuiltinReadings("待って...本当に？")).toBe("待って、本当に？"); // a run of half-width ellipsis dots matches too
  });

  it("no extra comma when the held-beat mark is already followed by punctuation or the end of the sentence", () => {
    expect(applyBuiltinReadings("え……。")).toBe("え。");
    expect(applyBuiltinReadings("そう……")).toBe("そう");
    expect(applyBuiltinReadings("待って――」と叫んだ")).toBe("待って」と叫んだ");
  });

  it("a user dictionary match takes precedence over the built-in corrections", () => {
    const dict = parseUserDict("空レポ=そらレポ");
    expect(applyReadings("空レポです", dict, false)).toBe("そらレポです");
  });

  it("applyReadings runs dictionary, then reading corrections, then particle micro-pauses", () => {
    expect(applyReadings("空配列を確認", [], true)).toBe("から配列を、確認");
  });
});

describe("pauseParticles (micro-pause on a particle followed by kanji)", () => {
  it("inserts a comma when を/は/で/に/と is followed directly by a kanji", () => {
    expect(pauseParticles("神は細部に宿る")).toBe("神は、細部に、宿る");
    expect(pauseParticles("設定を保存して再起動")).toBe("設定を、保存して再起動");
    expect(pauseParticles("ミラーで再生とターミナルで停止")).toBe("ミラーで、再生とターミナルで、停止");
  });

  it("inserts nothing when hiragana, katakana or punctuation follows", () => {
    expect(pauseParticles("これはそのままです")).toBe("これはそのままです");
    expect(pauseParticles("画面にカーソルを、")).toBe("画面にカーソルを、");
    expect(pauseParticles("ペインとタブ")).toBe("ペインとタブ");
  });
});

describe("isBareHash / abbreviated reading of a bare hash", () => {
  const F = `(${CODE_FILLERS.join("|")})`;

  it("only a token that can be nothing but a hex hash is true", () => {
    expect(isBareHash("f437e17")).toBe(true); // git short hash
    expect(isBareHash("b2d7fac36b996a9ae6245c188b51c4dbac2c9aef")).toBe(true); // full SHA
    expect(isBareHash("28733db5-247d-5fd9-a6b9-6d64d685412a")).toBe(true); // UUID
    expect(isBareHash("deadbeef")).toBe(false); // letters only (it could be an English word)
    expect(isBareHash("facade")).toBe(false); // letters only, and under 7 characters
    expect(isBareHash("1783780316")).toBe(false); // digits only (a token count, a time and so on)
    expect(isBareHash("F437E17")).toBe(false); // upper case is out of scope (git uses lower case)
    expect(isBareHash("abc123")).toBe(false); // 6 characters, below the short-hash minimum
  });

  it("plainify abbreviates a bare hash in prose, gated on ttsAbbrevCode", () => {
    const code = { abbrev: true, dict: [] as [string, string][] };
    expect(plainify("前回マージ f437e17 以降の 8 コミット", code)).toMatch(
      new RegExp(`^前回マージ f4 ${F} 以降の 8 コミット$`),
    );
    expect(plainify("前回マージ f437e17 以降の")).toBe("前回マージ f437e17 以降の"); // abbreviated reading off
    expect(plainify("値は 1783780316 です", code)).toBe("値は 1783780316 です"); // a long number is left as-is
    expect(plainify("facade を deadbeef に", code)).toBe("facade を deadbeef に"); // an English word is left as-is
  });

  it("a spelling the dictionary covers is left alone; applyUserDict substitutes later", () => {
    const dict = parseUserDict("f437e17=まえのマージ");
    expect(plainify("f437e17 を見て", { abbrev: true, dict })).toBe("f437e17 を見て");
  });

  it("picks up each hash even next to a range mark or punctuation", () => {
    const code = { abbrev: true, dict: [] as [string, string][] };
    expect(plainify("f437e17..0415b6a を比較。", code)).toMatch(new RegExp(`^f4 ${F}..04 ${F} を比較。$`));
  });

  it("a long SHA in inline code still reads as its first 2 characters plus a filler, never mistaken for words", () => {
    expect(abbrevCode("b2d7fac36b996a9ae6245c188b51c4dbac2c9aef")).toMatch(new RegExp(`^b2 ${F}$`));
  });
});

describe("splitLongSentence (splitting a long sentence for synthesis)", () => {
  it("a short sentence is left as-is", () => {
    expect(splitLongSentence("短い文です。")).toEqual(["短い文です。"]);
  });

  it("a long sentence splits at weak breaks (comma, middle dot, closing bracket) and rejoins to the original", () => {
    const s =
      "キャリア系は au（エーユー）・ymobile（ワイモバイル）を追加し、NTT や KDDI は自動で読めるので対象外としつつ、SOFTBANK と RAKUTEN は綴りママになるため辞書で手当てしました。";
    const pieces = splitLongSentence(s, 40);
    expect(pieces.length).toBeGreaterThan(1);
    expect(pieces.join("")).toBe(s);
    for (const p of pieces) expect(p.length).toBeLessThanOrEqual(41); // up to max+1, counting the break character
  });

  it("a long sentence with no break is split by length", () => {
    const s = "あ".repeat(100);
    const pieces = splitLongSentence(s, 40);
    expect(pieces).toEqual(["あ".repeat(40), "あ".repeat(40), "あ".repeat(20)]);
  });
});

describe("splitSentences (sentence splitting for rendered text)", () => {
  it("splits at a full stop and keeps the full stop on the preceding sentence", () => {
    expect(splitSentences("これは一文目。二文目です。")).toEqual(["これは一文目。", "二文目です。"]);
  });

  it("a trailing fragment with no full stop is still returned as a sentence", () => {
    expect(splitSentences("読点だけで、終わる断片")).toEqual(["読点だけで、終わる断片"]);
  });

  it("newlines and runs of whitespace collapse to a single space", () => {
    expect(splitSentences("一行目\n  二行目です。")).toEqual(["一行目 二行目です。"]);
  });

  it("a fragment with no kana, kanji or alphanumeric (symbols only) is dropped", () => {
    expect(splitSentences("---")).toEqual([]);
    expect(splitSentences("読む。※")).toEqual(["読む。"]);
  });

  it("an empty string gives an empty array", () => {
    expect(splitSentences("")).toEqual([]);
  });
});

describe("makeAudioLru (the synthesis cache LRU)", () => {
  const buf = (duration: number) => ({ duration });

  it("basic put/get, and a missing key gives undefined", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    const a = buf(3);
    c.put("a", a);
    expect(c.get("a")).toBe(a);
    expect(c.get("b")).toBeUndefined();
    expect(c.size()).toBe(1);
  });

  it("evicts oldest-first once the total seconds exceed the cap", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    c.put("a", buf(4));
    c.put("b", buf(4));
    c.put("c", buf(4)); // 12 > 10, so a is evicted and 8 is left
    expect(c.get("a")).toBeUndefined();
    expect(c.get("b")).toBeDefined();
    expect(c.get("c")).toBeDefined();
  });

  it("an entry touched by get counts as most recent and escapes eviction", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    c.put("a", buf(4));
    c.put("b", buf(4));
    c.get("a"); // moves a to the most-recent end
    c.put("c", buf(4)); // b is now the oldest, so b is the one dropped
    expect(c.get("a")).toBeDefined();
    expect(c.get("b")).toBeUndefined();
    expect(c.get("c")).toBeDefined();
  });

  it("a value larger than the cap on its own, and a duplicate key, are not stored", () => {
    const c = makeAudioLru<{ duration: number }>(() => 10);
    c.put("big", buf(11));
    expect(c.get("big")).toBeUndefined();
    const first = buf(2);
    c.put("dup", first);
    c.put("dup", buf(3)); // a duplicate put is ignored (the total seconds stay correct)
    expect(c.get("dup")).toBe(first);
    expect(c.size()).toBe(1);
  });

  it("follows a change of cap: lowering it shrinks on the next put, and 0 disables and discards", () => {
    let max = 10;
    const c = makeAudioLru<{ duration: number }>(() => max);
    c.put("a", buf(4));
    c.put("b", buf(4));
    max = 4; // lower the cap: the next put shrinks the total to 4
    c.put("c", buf(4)); // c itself is stored and a/b are evicted
    expect(c.get("a")).toBeUndefined();
    expect(c.get("b")).toBeUndefined();
    expect(c.get("c")).toBeDefined();
    max = 0; // disabled: get always misses and what was held is released
    expect(c.get("c")).toBeUndefined();
    expect(c.size()).toBe(0);
  });
});

describe("plainifyStreaming (fences spanning chunks)", () => {
  const mkFence = () => {
    let v = false;
    return { get: () => v, set: (nv: boolean) => (v = nv) };
  };

  it("drops a code block that opens and closes within one chunk", () => {
    const f = mkFence();
    expect(plainifyStreaming("前\n```\nconst x=1;\n```\n後", f).replace(/\s+/g, "")).toBe("前後");
    expect(f.get()).toBe(false); // closed, so the state is reset
  });

  it("suppresses a code block spanning chunks by carrying the state", () => {
    const f = mkFence();
    // chunk 1 opens the fence and leaves it open
    const a = plainifyStreaming("説明。\n```js", f);
    expect(a).toBe("説明。"); // everything from the opening fence on is dropped
    expect(f.get()).toBe(true); // the fence is still open
    // chunk 2 is the code body -> dropped entirely
    const b = plainifyStreaming("const y = 2;", f);
    expect(b).toBe("");
    expect(f.get()).toBe(true);
    // chunk 3 closes it -> only the prose after the close survives
    const c = plainifyStreaming("```\nおわり", f);
    expect(c.replace(/\s+/g, "")).toBe("おわり");
    expect(f.get()).toBe(false);
  });
});
