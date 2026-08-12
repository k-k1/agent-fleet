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
  it("内部的な再生置換は明示停止ではなく replaced を渡す", () => {
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
  it("非表示またはフォーカス外を背景として扱う", () => {
    expect(ttsIsBackground(true, true)).toBe(true);
    expect(ttsIsBackground(false, false)).toBe(true);
    expect(ttsIsBackground(false, true)).toBe(false);
  });

  it("設定ONかつ背景時だけ音量を下げる", () => {
    expect(ttsMasterGain("quiet", true)).toBe(HIDDEN_TTS_GAIN);
    expect(ttsMasterGain("quiet", false)).toBe(1);
    expect(ttsMasterGain("normal", true)).toBe(1);
    expect(ttsMasterGain("mute", true)).toBe(0);
  });

  it("quiet の音量はスライダー値（ttsBackgroundVolume）で調整でき、mute/normal には効かない", () => {
    expect(ttsMasterGain("quiet", true, 0.6)).toBe(0.6);
    expect(ttsMasterGain("quiet", true, 0)).toBe(0); // 実質ミュート
    expect(ttsMasterGain("quiet", true, 5)).toBe(1); // 0..1 にクランプ
    expect(ttsMasterGain("mute", true, 0.6)).toBe(0); // mute は常に無音
    expect(ttsMasterGain("normal", true, 0.6)).toBe(1); // normal は常に通常
    expect(ttsMasterGain("quiet", false, 0.6)).toBe(1); // 非背景は常に通常
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

  it("左右端を最大パン、中央列を中央へ配置する", () => {
    expect(ttsPanePan(true, layout, "left-top")).toBe(-MAX_PANE_PAN);
    expect(ttsPanePan(true, layout, "left-bottom")).toBe(-MAX_PANE_PAN);
    expect(ttsPanePan(true, layout, "center")).toBeCloseTo(0);
    expect(ttsPanePan(true, layout, "right")).toBe(MAX_PANE_PAN);
  });

  it("設定OFF・ペイン外・単一列は中央で再生する", () => {
    expect(ttsPanePan(false, layout, "left-top")).toBe(0);
    expect(ttsPanePan(true, layout, "missing")).toBe(0);
    expect(
      ttsPanePan(true, { ...layout, cols: [layout.cols[0]], colRatios: [1] }, "left-top"),
    ).toBe(0);
  });
});

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

  it("行頭の溜め（――等・……等）はマーカーとして読み上げから除く", () => {
    expect(plainify("————また、イく。")).toBe("また、イく。"); // 実機報告
    expect(plainify("——イって、る。")).toBe("イって、る。");
    expect(plainify("……一日中、って。")).toBe("一日中、って。"); // 実機報告（三点リーダ）
    expect(plainify("...本当に？")).toBe("本当に？"); // 半角三点リーダ連続
    expect(plainify("普通の文の中の—区切り")).toBe("普通の文の中の—区切り"); // 行頭以外は触らない
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

describe("startsTame (溜めの判定 = 長めの前拍を置く合図)", () => {
  it("行頭のダッシュ連続（――/——/―― 等）に一致する", () => {
    expect(startsTame("――また、行く。")).toBe(true);
    expect(startsTame("————また、イく。")).toBe(true); // em dash 連続
    expect(startsTame("——イって、る。")).toBe(true);
    expect(startsTame("―行く。")).toBe(true); // 1 個でも対象
  });

  it("行頭の三点リーダ連続（……/... 等）にも一致する", () => {
    expect(startsTame("……一日中、って。")).toBe(true); // 実機報告
    expect(startsTame("...本当に？")).toBe(true); // 半角三点リーダ連続
    expect(startsTame("…")).toBe(false); // 直後が無い（行末）
  });

  it("語尾の伸ばし（直後が空白・行末）やハイフンだけの語には一致しない", () => {
    expect(startsTame("普通の文です。")).toBe(false);
    expect(startsTame("-1 が返ります")).toBe(false); // 半角ハイフンは対象外（BLOCK_HEAD と衝突回避）
    expect(startsTame("―― ")).toBe(false); // 直後が空白/行末
    expect(startsTame("")).toBe(false);
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
  const F = `(${CODE_FILLERS.join("|")})`;

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

describe("abbrevPath (裸のパスの省略読み)", () => {
  const F = `(${PATH_FILLERS.join("|")})`;

  it("3 セグメント以上のパスは頭、フィラー、末尾（ファイル名）に読点で畳む", () => {
    expect(abbrevPath("console/src/features/chat/tts.ts")).toMatch(new RegExp(`^console、${F}、tts.ts$`));
    expect(abbrevPath("/home/dev/repos/agent-fleet/tts.go")).toMatch(new RegExp(`^home、${F}、tts.go$`));
    expect(abbrevPath("./src/features/store.ts")).toMatch(new RegExp(`^src、${F}、store.ts$`));
    expect(abbrevPath("../a/b/c")).toMatch(new RegExp(`^a、${F}、c$`));
  });

  it("2 セグメント以下・数値列（日付）は畳まない", () => {
    expect(abbrevPath("src/main.ts")).toBe("src/main.ts");
    expect(abbrevPath("2024/01/02")).toBe("2024/01/02");
  });

  it("フィラーはパスから決定的に選ぶ（毎回同じ）", () => {
    expect(abbrevPath("a/b/c/d")).toBe(abbrevPath("a/b/c/d"));
  });

  it("plainify に組み込み（abbrev 有効時のみ・裸パス）", () => {
    expect(plainify("編集は /home/dev/repos/agent-fleet/tts.go です", { abbrev: true, dict: [] })).toMatch(
      new RegExp(`^編集は home、${F}、tts.go です$`),
    );
    // 2 セグメント（TCP/IP 等）・日付は畳まれない
    expect(plainify("2024/01/02 の変更", { abbrev: true, dict: [] })).toBe("2024/01/02 の変更");
    // abbrev 無効時はそのまま
    expect(plainify("a/b/c/d.ts を見て", { abbrev: false, dict: [] })).toBe("a/b/c/d.ts を見て");
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

  it("セクション記号 § は「セクション」に読み、番号はそのまま残す", () => {
    expect(applyBuiltinReadings("§3 を参照")).toBe("セクション3 を参照");
    expect(applyBuiltinReadings("§§ に記載")).toBe("セクション に記載");
  });

  it("ピリオドを跨ぐ定型トークン（init.d / cron.d / resolv.conf 等）はカタカナに確定する", () => {
    expect(applyBuiltinReadings("init.d に配置")).toBe("イニットドットディー に配置");
    expect(applyBuiltinReadings("cron.d と rc.d と conf.d")).toBe(
      "クロンドットディー と アールシードットディー と コンフドットディー",
    );
    expect(applyBuiltinReadings("resolv.conf を編集")).toBe("リゾルブドットコンフ を編集");
    expect(applyBuiltinReadings("sudoers.d に置く")).toBe("スードゥアーズドットディー に置く");
    // 単語境界つきなので cron.daily の一部（cron.d）は誤って置換しない
    expect(applyBuiltinReadings("cron.daily")).toBe("cron.daily");
  });

  it("開発現場の漢語は IT の慣用読みに固定する", () => {
    expect(applyBuiltinReadings("引数と添字")).toBe("ひきすうとそえじ");
    expect(applyBuiltinReadings("閾値を超えたら相殺")).toBe("しきいちを超えたらそうさい");
    expect(applyBuiltinReadings("脆弱性と端数と冪等")).toBe("ぜいじゃくせいとはすうとべきとう");
  });

  it("裸のスラッシュ区切り（コード外）は中黒に変えて間を詰める", () => {
    expect(applyBuiltinReadings("origin/main のブランチ")).toBe("origin・main のブランチ");
    expect(applyBuiltinReadings("on/off と read/write")).toBe("on・off と read・write");
    expect(applyBuiltinReadings("a/b/c の階層")).toBe("a・b・c の階層"); // チェーンも一括
  });

  it("年付き日付は変換し、明確な分数文脈は保護する", () => {
    expect(applyBuiltinReadings("2024/01/02 の変更")).toBe("2024年1月2日 の変更");
    expect(applyBuiltinReadings("確率は1/2です")).toBe("確率は1/2です");
  });

  it("日付を年月日の日本語読みへ展開する", () => {
    expect(applyBuiltinReadings("2024-09-20 と2024/09/20、7/2")).toBe(
      "2024年9月20日 と2024年9月20日、7月2日",
    );
    expect(applyBuiltinReadings("2024-02-29")).toBe("2024年2月29日");
  });

  it("存在しない日付と明確な分数・計算文脈は変換しない", () => {
    expect(applyBuiltinReadings("2023-02-29、2023/02/29、2024/13/01、4/31")).toBe(
      "2023-02-29、2023/02/29、2024/13/01、4/31",
    );
    expect(applyBuiltinReadings("確率は1/2で、7/2倍、9/3を計算")).toBe("確率は1/2で、7/2倍、9/3を計算");
  });

  it("時刻を時分秒の日本語読みへ展開する", () => {
    expect(applyBuiltinReadings("12:34:56 と12:34、00:05")).toBe("12時34分56秒 と12時34分、0時5分");
    expect(applyBuiltinReadings("24:00、12:60、12:34:60")).toBe("24:00、12:60、12:34:60");
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

  it("プロダクト名 agent-fleet は表記ゆれ込みでエージェントフリートに確定", () => {
    expect(applyBuiltinReadings("Agent-Fleetの機能")).toBe("エージェントフリートの機能");
    expect(applyBuiltinReadings("Agent fleetはCP-native")).toBe("エージェントフリートはCP-native");
    expect(applyBuiltinReadings("AgentFleetを使う")).toBe("エージェントフリートを使う");
  });

  it("範囲の波ダッシュ（単独）は「から」と読む", () => {
    expect(applyBuiltinReadings("通常の3〜5倍速です")).toBe("通常の3から5倍速です");
    expect(applyBuiltinReadings("月〜金曜日")).toBe("月から金曜日");
    expect(applyBuiltinReadings("1～3個")).toBe("1から3個"); // 全角チルダ（U+FF5E）も同一視
    expect(applyBuiltinReadings("3〜5〜7")).toBe("3から5から7"); // 連鎖レンジ
  });

  it("波ダッシュの連続（省略・言いよどみ用法）は「ほにゃらら」と読む", () => {
    expect(applyBuiltinReadings("詳細は～～～省略します")).toBe("詳細はほにゃらら省略します");
    expect(applyBuiltinReadings("あとは〜〜適当に")).toBe("あとはほにゃらら適当に"); // 2個でも対象
    expect(applyBuiltinReadings("混在～〜～も1個として畳む")).toBe("混在ほにゃららも1個として畳む");
  });

  it("語尾の伸ばし（文末・句読点・空白の前）の単独波ダッシュは触らない", () => {
    expect(applyBuiltinReadings("そうだね〜")).toBe("そうだね〜");
    expect(applyBuiltinReadings("そうだね〜。")).toBe("そうだね〜。");
    expect(applyBuiltinReadings("うんうん〜 まあいいか")).toBe("うんうん〜 まあいいか");
  });

  it("「行」= line/row は既定で「ぎょう」（くだり不要）。行サフィックス・数字＋行・行＋助詞を拾う", () => {
    expect(applyBuiltinReadings("3行目のバグ")).toBe("3ぎょうめのバグ");
    expect(applyBuiltinReadings("42行を削除")).toBe("42ぎょうを削除");
    expect(applyBuiltinReadings("行数と行末と行頭")).toBe("ぎょうすうとぎょうまつとぎょうとう");
    expect(applyBuiltinReadings("行全体を選択")).toBe("ぎょうぜんたいを選択");
    expect(applyBuiltinReadings("１０行目")).toBe("１０ぎょうめ"); // 全角数字
    expect(applyBuiltinReadings("集計行と統計行")).toBe("集計ぎょうと統計ぎょう"); // 漢字＋行サフィックス
    expect(applyBuiltinReadings("WT 行を確認")).toBe("WT ぎょうを確認"); // 実機報告の "WT 行"
    expect(applyBuiltinReadings("この行が長い")).toBe("このぎょうが長い"); // 行＋助詞
    expect(applyBuiltinReadings("先頭行と最終行")).toBe("先頭ぎょうと最終ぎょう");
  });

  it("「行」でも こう熟語・行動・行く/行う は壊さない（既定 ぎょう の除外）", () => {
    // 直後が漢字（行動・行政）＝ OpenJTalk に委ねる
    expect(applyBuiltinReadings("行動と行政")).toBe("行動と行政");
    // 送りがな（行く・行う）
    expect(applyBuiltinReadings("現場に行く、処理を行う")).toBe("現場に行く、処理を行う");
    // こう熟語のブロックリスト（実行・銀行・移行・並行…）
    expect(applyBuiltinReadings("銀行で実行し移行を並行する")).toBe("銀行で実行し移行を並行する");
  });

  it("「判定」は はんてい に固定（誤判定→ごはんてい 相当）", () => {
    expect(applyBuiltinReadings("誤判定を修正")).toBe("ごはんていを修正"); // 誤→ご も同時に効く
    expect(applyBuiltinReadings("空判定と判定結果")).toBe("からはんていとはんてい結果");
  });

  it("接頭辞「誤」＋漢字は「ご」（誤表示→ごひょうじ）。送りがな 誤る/誤り は あやま のまま", () => {
    expect(applyBuiltinReadings("「5時間制限」と誤表示")).toBe("「5時間制限」とご表示"); // 実機報告
    expect(applyBuiltinReadings("誤検知と誤動作と誤操作")).toBe("ご検知とご動作とご操作");
    // 送りがな（訓読み あやま）は触らない
    expect(applyBuiltinReadings("設定を誤ると誤りが出る")).toBe("設定を誤ると誤りが出る");
  });

  it("濁り誤読の固定: 貼り付け→はりつけ / 型チェック→かたチェック", () => {
    expect(applyBuiltinReadings("画像貼り付け")).toBe("画像はりつけ"); // 実機報告
    expect(applyBuiltinReadings("ここに貼り付けると貼り付けた")).toBe("ここにはりつけるとはりつけた"); // 前方一致
    expect(applyBuiltinReadings("TypeScript型チェック")).toBe("TypeScriptかたチェック"); // 実機報告（英字は CP 側 enkana）
  });

  it("言って→いって（文末誤読対策）/ 単独の身体→からだ", () => {
    expect(applyBuiltinReadings("何する？　言って。")).toBe("何する？　いって。"); // 実機報告
    expect(applyBuiltinReadings("身体を動かす")).toBe("からだを動かす"); // 実機報告
    expect(applyBuiltinReadings("身体が資本で、身体そのものを鍛える")).toBe(
      "からだが資本で、からだそのものを鍛える",
    );
  });

  it("身体＋漢字の音読み複合語は「からだ」に壊さない", () => {
    expect(applyBuiltinReadings("身体検査と身体能力と身体機能")).toBe(
      "身体検査と身体能力と身体機能",
    );
  });

  it("放ってお（放置の慣用句）→ほうってお。単独の放っては触らない（放つ＝はなつ と区別）", () => {
    expect(applyBuiltinReadings("放っておく")).toBe("ほうっておく"); // 実機報告
    expect(applyBuiltinReadings("放っておかれる子供")).toBe("ほうっておかれる子供");
    expect(applyBuiltinReadings("放っておいた")).toBe("ほうっておいた");
    // 放つ（解き放つ）の可能性が残る単独形は触らない＝はなって のまま委ねる
    expect(applyBuiltinReadings("光を放って輝いた")).toBe("光を放って輝いた");
    expect(applyBuiltinReadings("矢を放って命中した")).toBe("矢を放って命中した");
  });

  it("が/は/も＋要（文末・句読点・です/だ）→かなめ。複合語・要る は触らない", () => {
    expect(applyBuiltinReadings("ここが要")).toBe("ここがかなめ"); // 実機報告
    expect(applyBuiltinReadings("ここが要です")).toBe("ここがかなめです");
    expect(applyBuiltinReadings("そこが要だ。")).toBe("そこがかなめだ。");
    expect(applyBuiltinReadings("そこは要、次も要")).toBe("そこはかなめ、次もかなめ");
    // 複合語（よう）は直後が漢字なので対象外
    expect(applyBuiltinReadings("そこが要注意")).toBe("そこが要注意");
    expect(applyBuiltinReadings("それが要素です")).toBe("それが要素です");
    // 要る（いる＝必要）は活用語尾が続くので対象外
    expect(applyBuiltinReadings("許可が要る")).toBe("許可が要る");
    expect(applyBuiltinReadings("それは要らない")).toBe("それは要らない");
  });

  it("この/その/あの/どの＋様な・様に→よう。あり様→ありよう", () => {
    expect(applyBuiltinReadings("その様な問題")).toBe("そのような問題"); // 実機報告
    expect(applyBuiltinReadings("この様に考える")).toBe("このように考える");
    expect(applyBuiltinReadings("あの様なミス")).toBe("あのようなミス");
    expect(applyBuiltinReadings("どの様に進める？")).toBe("どのように進める？");
    expect(applyBuiltinReadings("あり様を見直す")).toBe("ありようを見直す"); // 実機報告
    // 複合語（様子・様々）は直後が な/に ではないので対象外
    expect(applyBuiltinReadings("その様子と様々な意見")).toBe("その様子と様々な意見");
  });

  it("文中の溜め（――・……）は読点に変えて間を作る（行頭は startsTame/plainify が別処理）", () => {
    expect(applyBuiltinReadings("……一日中、って。")).toBe("、一日中、って。"); // 実機報告
    expect(applyBuiltinReadings("そして――彼は言った。")).toBe("そして、彼は言った。");
    expect(applyBuiltinReadings("分かった…でも心配だ")).toBe("分かった、でも心配だ");
    expect(applyBuiltinReadings("待って...本当に？")).toBe("待って、本当に？"); // 半角三点リーダ連続も対象
  });

  it("溜めマークの直後が句読点・文末ならさらに読点を重ねない", () => {
    expect(applyBuiltinReadings("え……。")).toBe("え。");
    expect(applyBuiltinReadings("そう……")).toBe("そう");
    expect(applyBuiltinReadings("待って――」と叫んだ")).toBe("待って」と叫んだ");
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
  const F = `(${CODE_FILLERS.join("|")})`;

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

describe("splitLongSentence (長文の合成用分割)", () => {
  it("短い文はそのまま", () => {
    expect(splitLongSentence("短い文です。")).toEqual(["短い文です。"]);
  });

  it("長い文は弱い区切り（読点・中黒・閉じ括弧等）で割り、結合すると元に戻る", () => {
    const s =
      "キャリア系は au（エーユー）・ymobile（ワイモバイル）を追加し、NTT や KDDI は自動で読めるので対象外としつつ、SOFTBANK と RAKUTEN は綴りママになるため辞書で手当てしました。";
    const pieces = splitLongSentence(s, 40);
    expect(pieces.length).toBeGreaterThan(1);
    expect(pieces.join("")).toBe(s);
    for (const p of pieces) expect(p.length).toBeLessThanOrEqual(41); // 区切りを含めて max+1 まで
  });

  it("区切りが無い長文は長さで強制分割する", () => {
    const s = "あ".repeat(100);
    const pieces = splitLongSentence(s, 40);
    expect(pieces).toEqual(["あ".repeat(40), "あ".repeat(40), "あ".repeat(20)]);
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
