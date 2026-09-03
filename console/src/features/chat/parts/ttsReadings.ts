import { applyUserDict } from "./ttsUserDict.ts";

// --- 組み込みの読み補正 ------------------------------------------------------------
// VOICEVOX が読み間違えやすい開発文脈の一般語を補正する（例: 空レポ→そられぽ）。
// ユーザー/テナント辞書の**後**に適用するので、同じ表記をユーザーが定義すればそちらが勝つ
// （先に置換されて表記が消えるため）。「空＋カタカナ語」（空レポ・空リスト等）は規則で
// 「から」に、漢字が続く複合語は個別エントリで持つ。航空券・空港などの熟語や
// 「空が青い」（ひらがな続き）は触らない。
const KARA_KATAKANA = /空(?=[ァ-ヶー])/g;
// 「身体」は単独・助詞続き・和語続きでは「からだ」が自然だが、漢字が続く
// 音読み複合語（身体検査・身体能力・身体機能等）は「しんたい」。複合語は原文を残して
// OpenJTalk の辞書に任せ、後続が漢字でない場合だけ訓読みへ固定する。
const KARADA_BODY = /身体(?![一-鿿㐀-䶿豈-﫿々])/g;
const BUILTIN_READINGS: [string, string][] = [
  ["空文字", "から文字"], // 空文字列も前方一致で拾う
  ["空配列", "から配列"],
  ["空要素", "から要素"],
  ["空判定", "からはんてい"], // 判定=はんてい に固定（下の 判定 と揃える。空→から も同時に効かせる）
  ["空行", "からぎょう"], // 行 単体は読みが揺れる（こう/ぎょう）ので読みまで固定
  // 「行」= line の複合語（送りがな無しで意味が確定するもの）。fixGyoLine より先に固定する。
  ["行目", "ぎょうめ"],
  ["行数", "ぎょうすう"],
  ["行末", "ぎょうまつ"],
  ["行頭", "ぎょうとう"],
  ["行番号", "ぎょうばんごう"],
  ["行全体", "ぎょうぜんたい"],
  // 判定 は文脈により 定 が「じょう」に化ける（誤判定→ごはんじょう の実機報告）。読みを固定。
  ["判定", "はんてい"],
  // 貼り付け は 貼 が濁って「ばりつけ」に化ける（実機報告）。前方一致で 貼り付ける/貼り付けた も。
  ["貼り付け", "はりつけ"],
  // 型チェック は 型 が濁って「がたチェック」に化ける（実機報告）。型 は熟語で けい/がた にも
  // なる（模型・大型…）ため一律変換は避け、開発文脈で「かた」に確定する複合語だけ個別に固定する。
  ["型チェック", "かたチェック"],
  // 言って（言う のて形）が文末・区切り直後の短い呼びかけ文脈で「げいって」に誤読される実機
  // 報告。言って は常に いって（他の読みを持つ語ではない）なので固定して安全。
  ["言って", "いって"],
  // 放って は 2 通りの動詞のて形で読みが割れる同形異音語: 放る(ほうる)＝ほうって（「放って
  // おく/おかれる」＝放置する慣用句）と、放つ(はなつ)＝はなって（光・矢・声など「放つ＝
  // 解き放つ」の意）。VOICEVOX は後者（はなって）に倒れがち（実機報告）。「放ってお」
  // （放っておく/おいた/おかれる/おいて 等、おく が続く形）は「放置する」の意味以外あり得ない
  // 慣用句なので、この形だけ安全に「ほうってお」へ固定する。単独の「放って」（光を放って 等）
  // は はなって の可能性が残るため触らない＝区別できる。
  ["放ってお", "ほうってお"],
  // あり様 は ありよう（本来あるべき姿・状態。「〜のありよう」）に固定する（実機報告）。
  ["あり様", "ありよう"],
  // --- 開発現場で使う漢語のうち、OpenJTalk が慣用（IT 分野）の読みから外しやすいもの。
  //     いずれも IT 文脈では読みが一意なので固定して安全（一般語の別読みと衝突しない）。 ---
  ["引数", "ひきすう"], // いんすう に化けやすい
  ["添字", "そえじ"], // てんじ に化けやすい
  ["閾値", "しきいち"], // いきち とも読むが IT 分野の慣用は しきいち
  ["相殺", "そうさい"], // そうさつ と誤読されやすい
  ["脆弱性", "ぜいじゃくせい"], // 脆 が落ちて きじゃく 等に化けやすい
  ["端数", "はすう"], // たんすう と誤読されやすい
  ["冪等", "べきとう"], // 冪 は常用外で読めないことがある
];

// GYO_KO_COMPOUNDS — 末尾が「行」でも「ぎょう」と読まない漢語（こう/他）。fixGyoLine の
// 既定（行→ぎょう）から除外して OpenJTalk の読みに委ねる。ユーザー方針「くだりは不要・基本
// ぜんぶ ぎょう」を受け、行=line/row は既定 ぎょう にしつつ、これら定着した二字漢語だけ守る。
// **こう熟語が くだり/ぎょう に化けたらここに 1 語足す。** 修行(しゅぎょう)/奉行(ぶぎょう) は
// 「ぎょう」だが 修→しゅう 等に分解誤読するため同様に除外（元の読みを保つ）。
const GYO_KO_COMPOUNDS = new Set([
  "実行", "移行", "発行", "進行", "現行", "並行", "平行", "続行", "代行", "施行", "履行",
  "直行", "運行", "先行", "通行", "遂行", "執行", "携行", "同行", "銀行", "旅行", "飛行",
  "航行", "歩行", "走行", "慣行", "強行", "決行", "断行", "紀行", "刊行", "興行", "徐行",
  "急行", "逆行", "蛇行", "夜行", "修行", "苦行", "潜行", "素行", "品行", "暴行", "犯行",
  "非行", "悪行", "善行", "孝行", "横行", "流行", "励行", "尾行", "随行",
]);

// fixGyoLine — 「行」= line/row を既定で「ぎょう」に固定する（くだり は開発/データ文脈で不要）。
// 集計行・統計行・WT行・3行・裸の行・行を などを ぎょう にする一方、次は誤読を避けて温存する:
//   - 直後が漢字（行動・行政・行為・行程・行間…OpenJTalk が こう/ぎょう を正しく読む）
//   - 直後が送りがな＝ひらがな（行く・行う・行った…助詞 を/が/は/も/に/へ/で/と/の は line 扱いで ぎょう化）
//   - 直後がカタカナ/長音、または前＋行 が GYO_KO_COMPOUNDS（実行・銀行…）
// 前 1 文字はそのまま温存する（"集計行"→"集計ぎょう"＝しゅうけいぎょう。前の漢字は OpenJTalk 読み）。
// lookbehind 不使用（Safari 16.4 未満対策）。
const GYO_LINE = /(.?)行(?=[をがはもにへでとの]|[^ぁ-んァ-ヶ一-鿿々ー]|$)/g;

function fixGyoLine(t: string): string {
  return t.replace(GYO_LINE, (m, prev: string) => (GYO_KO_COMPOUNDS.has(prev + "行") ? m : prev + "ぎょう"));
}

// GO_PREFIX — 接頭辞「誤」= 音読み「ご」（誤表示・誤判定・誤検知・誤動作・誤操作・誤入力…）。
// OpenJTalk は文脈で訓読み「あやま」に化ける（「誤表示」→「あやまひょうじ」の実機報告）。「誤＋
// 漢字」の複合語は必ず音読み ご なので固定する。送りがなを伴う動詞・名詞（誤る・誤り・誤って）は
// 直後がひらがななので対象外＝「あやま」のまま残す。BUILTIN_READINGS（判定→はんてい 等）より
// 先に適用し、後段の複合語変換に載せる（誤判定 → ご判定 → ごはんてい）。
const GO_PREFIX = /誤(?=[一-鿿々])/g;

// KANAME — 「が/は/も＋要」が独立した名詞（かなめ＝要点・急所）として文を終える形だけを
// 「かなめ」に固定する（「ここが要」の実機報告）。「要」は よう（必要・要素・重要…複合語）・
// いる（要る＝必要とする、要らない等）・かなめ の 3 通りで読みが割れるため、複合語や動詞
// 「要る」の活用（要ら/要り/要る/要れ/要ろ…）を巻き込まないよう、直後が漢字・「る」を含む
// 活用語尾に続かない場合（句読点・文末・「です/だ」の直前）だけに絞る。「が要注意/が要素/
// が要る」はこの条件で除外され、よう/いる のまま残る。
const KANAME = /([がはも])要(?=[。、！？」』）\s]|です|だ|$)/g;

// KONO_YOU_NA — 「この/その/あの/どの＋様な・様に」は「よう」で確定する定型の指示表現
// （そのような・このように 等）。様 は さま（お客様・皆様等の敬称）/よう（様子・そのよう等）/
// かなめ とは別語だが同じ問題で読みが割れる（実機報告: その様な→そのさまな）。直後が
// な/に に確定する指示語＋様の形だけに絞れば安全（様子・様々 等の複合語は直後が子/々で
// 対象外のまま残る）。
const KONO_YOU_NA = /(この|その|あの|どの)様(?=な|に)/g;

// ヌメロニム（i18n = internationalization 等の中抜き略語）。数字は「省略した文字数」で
// 数ではないので、数字読みさせず元の語の慣用カタカナに展開する。単語境界つき・大文字
// 小文字は不問（I18n / A11Y 等も拾う）。一覧は ja.wikipedia の「ヌメロニム」の IT 系＋
// 開発現場の頻出（o11y / e2e）。enkana（CP 側・英語→カタカナ）はこの後段だが、ここで
// 展開済みなので二重変換にはならない。読みは各語のいちばん流通しているカタカナ形。
const NUMERONYMS: [RegExp, string][] = [
  [/\bi18n\b/gi, "インターナショナリゼーション"],
  [/\bl10n\b/gi, "ローカリゼーション"],
  [/\bg11n\b/gi, "グローバリゼーション"],
  [/\bm17n\b/gi, "マルチリンガライゼーション"],
  [/\ba11y\b/gi, "アクセシビリティ"],
  [/\bo11y\b/gi, "オブザーバビリティ"],
  [/\bi14y\b/gi, "インターオペラビリティ"],
  [/\bc14n\b/gi, "カノニカライゼーション"],
  [/\bn11n\b/gi, "ノーマライゼーション"],
  [/\bd11n\b/gi, "ドキュメンテーション"],
  [/\bp13n\b/gi, "パーソナライゼーション"],
  [/\bv12n\b/gi, "バーチャライゼーション"],
  [/\btr8n\b/gi, "トランスレーション"],
  [/\be2e\b/gi, "エンドツーエンド"],
  [/\bk8s\b/gi, "クーバネティス"],
];

// 大文字だけの略語で、同綴りの英単語に読みを食われるもの（IT は CMU 辞書の it=イット が
// 勝ってしまう）。**大文字小文字を区別して**単語境界で当てる — 小文字の it は英語の代名詞
// なので触らない（enkana の辞書はキーを小文字化するため大小を区別できず、ここで扱う）。
const UPPER_ACRONYMS: [RegExp, string][] = [[/\bIT\b/g, "アイティー"]];

// プロダクト名（このリポジトリ自身）。"Agent-Fleet"/"Agent fleet"/"AgentFleet" のように
// ハイフン・空白・詰めて書くの表記ゆれがあり、enkana（CP 側）に渡すとハイフンがそのまま
// 残って発話が途切れる／"agent" 単体が CMUdict で "エイジャント" に誤読される（実機
// フィードバック）。enkana より前でカタカナへ確定させて表記ゆれごと吸収する。
const PRODUCT_NAMES: [RegExp, string][] = [[/agent[\s-]?fleet/gi, "エージェントフリート"]];

// ピリオドを跨ぐ定型トークンは enkana（CP・英数字トークン単位）では 1 語にまとまらず
// "." で分断されるので、ここでカタカナに確定させる。init.d 等の "*.d" ディレクトリや
// resolv.conf のような設定ファイル名。単語境界 \b 付きなので cron.daily 等の別語は誤爆しない。
const DOTTED_TERMS: [RegExp, string][] = [
  [/\binit\.d\b/gi, "イニットドットディー"],
  [/\bcron\.d\b/gi, "クロンドットディー"],
  [/\brc\.d\b/gi, "アールシードットディー"],
  [/\bconf\.d\b/gi, "コンフドットディー"],
  [/\bsudoers\.d\b/gi, "スードゥアーズドットディー"],
  [/\bresolv\.conf\b/gi, "リゾルブドットコンフ"],
];

// --- 裸のスラッシュ区切り（コード外）の間つめ ------------------------------------
// "origin/main"・"on/off"・"read/write" のような裸（バッククォート無し）のスラッシュ区切りは
// VOICEVOX(OpenJTalk) が "/" を記号扱いしてポーズを挟むため、地の文で読むと間が長く感じる
// （実機報告）。列挙・二択の慣用表現として自然な中黒（・、ほぼ無音の区切り）に差し替える。
// 3 セグメント以上（2 個以上の "/"）は plainify の PATH_RE/abbrevPath が先に「頭、フィラー、
// 末尾」へ畳むので対象外（ttsAbbrevCode が OFF でそこを通らなかった残りもここで拾う）。日付・
// 分数・除算（2024/01/02, 1/2 等）は両辺が数字のみのときだけ除外し、そのまま残す。文字クラスは
// 英数字・カタカナ・長音符だけに絞る（ひらがな/漢字を含めると「確率は1/2です」のように地の文
// が数字トークンへ食い込み、数字のみ判定が effectively 効かなくなるため）。
const SLASH_CHAIN = /[A-Za-z0-9ァ-ヶー]+(?:\/[A-Za-z0-9ァ-ヶー]+)+/g;

function shortenSlashPause(t: string): string {
  return t.replace(SLASH_CHAIN, (m) => {
    const segs = m.split("/");
    if (segs.every((s) => /^\d+$/.test(s))) return m; // 日付・分数・除算は触らない
    return segs.join("・");
  });
}

// --- 日付・時刻の日本語読み -------------------------------------------------
// 記号区切りの数値をそのまま音声エンジンへ渡すと、ハイフン・スラッシュ・
// コロンを記号として読む。実在する範囲の日付/時刻だけ「年月日・時分秒」へ展開する。
// M/D は分数・除算と曖昧なため日付を優先しつつ、明確な数式文脈だけ保護する。
const FULL_DATE = /(^|[^0-9A-Za-z])(\d{4})([-/])(\d{1,2})\3(\d{1,2})(?![0-9A-Za-z])/g;
const SHORT_DATE = /(^|[^0-9A-Za-z/])(\d{1,2})\/(\d{1,2})(?![0-9A-Za-z/])/g;
const CLOCK_TIME = /(^|[^\d])(\d{1,2}):(\d{2})(?::(\d{2}))?(?!\d)/g;
const FRACTION_BEFORE = /(?:確率|割合|比率|分数|計算|除算|商)(?:は|が|を|の)?\s*$/;
const FRACTION_AFTER = /^\s*(?:倍|を\s*(?:計算|求め|割る)|[=+×÷*\-])/;

function leapYear(year: number): boolean {
  return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
}

function validDate(year: number | null, month: number, day: number): boolean {
  if ((year != null && year < 1) || month < 1 || month > 12 || day < 1) return false;
  const feb = year == null || leapYear(year) ? 29 : 28;
  const days = [31, feb, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return day <= days[month - 1];
}

function applyDateTimeReadings(text: string): string {
  let t = text.replace(FULL_DATE, (whole, lead: string, ys: string, _sep: string, ms: string, ds: string) => {
    const year = Number(ys);
    const month = Number(ms);
    const day = Number(ds);
    return validDate(year, month, day) ? `${lead}${year}年${month}月${day}日` : whole;
  });
  t = t.replace(SHORT_DATE, (whole, lead: string, ms: string, ds: string, offset: number, source: string) => {
    const month = Number(ms);
    const day = Number(ds);
    if (!validDate(null, month, day)) return whole;
    const numberAt = offset + lead.length;
    const before = source.slice(Math.max(0, numberAt - 12), numberAt);
    const after = source.slice(offset + whole.length, offset + whole.length + 12);
    if (FRACTION_BEFORE.test(before) || FRACTION_AFTER.test(after)) return whole;
    return `${lead}${month}月${day}日`;
  });
  return t.replace(CLOCK_TIME, (whole, lead: string, hs: string, ms: string, ss?: string) => {
    const hour = Number(hs);
    const minute = Number(ms);
    const second = ss == null ? null : Number(ss);
    if (hour > 23 || minute > 59 || (second != null && second > 59)) return whole;
    return `${lead}${hour}時${minute}分${second == null ? "" : `${second}秒`}`;
  });
}

// --- 波ダッシュ（〜/～）の読み分け -------------------------------------------------
// 「3〜5倍速」のような範囲指定は「3から5倍速」と読ませたい一方、「〜〜〜」のように連続
// させる使い方は文章の省略・言いよどみ（「詳細はほにゃらら〜〜〜」等）を表すので、両者を
// 区別する。ワイド版（～ U+FF5E）と JIS 版（〜 U+301C）は入力元（OS/IME）でどちらでも
// 来うるので同一視する。
//  1. まず連続（2 個以上）を先に「ほにゃらら」へ潰す（範囲ルールより先に処理しないと
//     1 個ずつ「から」に化けてしまうため順序が重要）。
//  2. 残った単独の波ダッシュは、**直後に空白・句読点・文末以外の文字が続く**（＝範囲の
//     区切りとして 2 つの値の間に挟まれている）場合だけ「から」に変換する。「そうだね〜」
//     「〜。」のような語尾の伸ばし・カジュアルな用法（直後が文末/句読点/空白）は対象外＝
//     そのまま残す（VOICEVOX の既定の挙動に委ねる）。
const TILDE = "〜～";
const TILDE_RUN = new RegExp(`[${TILDE}]{2,}`, "g");
const TILDE_RANGE = new RegExp(`([^\\s${TILDE}])[${TILDE}](?=[^\\s${TILDE}。、！？!?，,])`, "g");

function applyTildeReadings(text: string): string {
  return text.replace(TILDE_RUN, "ほにゃらら").replace(TILDE_RANGE, "$1から");
}

// セクション記号 §（と連続 §§）は OpenJTalk が読み飛ばす/記号読みするので「セクション」に。
// 「§3」→「セクション3」のように直後の番号はそのまま数として読ませる。
const SECTION_SIGN = /§+/g;

export function applyBuiltinReadings(text: string): string {
  let t = applyDateTimeReadings(text);
  t = t.replace(SECTION_SIGN, "セクション");
  t = shortenSlashPause(t);
  t = tameMidToPause(t); // 文中の溜め（――・……。行頭は plainify が既に処理済み）
  t = t.replace(KARA_KATAKANA, "から");
  t = applyTildeReadings(t);
  for (const [re, to] of NUMERONYMS) t = t.replace(re, to);
  for (const [re, to] of PRODUCT_NAMES) t = t.replace(re, to);
  for (const [re, to] of DOTTED_TERMS) t = t.replace(re, to);
  for (const [re, to] of UPPER_ACRONYMS) t = t.replace(re, to);
  t = t.replace(GO_PREFIX, "ご"); // 接頭辞「誤」= ご（誤表示/誤判定… 判定変換より前に）
  t = t.replace(KANAME, "$1かなめ"); // が/は/も＋要（文末・句読点・です/だ 直前）= かなめ
  t = t.replace(KONO_YOU_NA, "$1よう"); // この/その/あの/どの＋様な・様に = よう
  t = t.replace(KARADA_BODY, "からだ"); // 訓読みの身体だけ固定し、漢語複合語は保護
  t = applyUserDict(t, BUILTIN_READINGS); // 行目/判定 等の複合語（fixGyoLine より先に固定）
  t = fixGyoLine(t); // 残りの「行」= line/row を既定 ぎょう に
  return t;
}

// applyReadings は読み上げ直前の「読みの整形」ひとまとめ: ユーザー/テナント辞書（優先）→
// 組み込みの読み補正 → 助詞の小休止。tts.ts の 3 経路（ストリーミング/朗読/告知の差し挟み）
// が共通で通る（enkana は CP 側でこの後段）。
export function applyReadings(text: string, dict: [string, string][], particlePause: boolean): string {
  let t = text;
  if (dict.length) t = applyUserDict(t, dict);
  t = applyBuiltinReadings(t);
  if (particlePause) t = pauseParticles(t);
  return t.trim();
}

// --- 助詞のあとの小休止 -----------------------------------------------------------
// 「を・は・で・に・と」の直後に漢字が続くとき、読点を挿入して合成に「息継ぎ」相当の
// 小さな間を作る（例: 神は細部に宿る → 神は、細部に、宿る）。文中は 1 回の合成の内側
// なので再生スケジュールの間（SENT_BEAT 等）では作れず、テキスト側で VOICEVOX の
// 読点ポーズ（句点より短い）に変換する。ひらがなが続く場合（とき・など・のような）は
// 挿入しない — 漢字の頭は語の切れ目である可能性が高く、誤挿入が少ない。
// 合成直前（ユーザー辞書の後）に適用し、表示には影響しない。
const PARTICLE_PAUSE = /([をはでにと])(?=[一-鿿㐀-䶿々])/g;

export function pauseParticles(text: string): string {
  return text.replace(PARTICLE_PAUSE, "$1、");
}

// --- ブロック頭の判定（リスト・見出し・引用の前拍用） -----------------------------
// リスト項目・見出し・引用など「新しいブロックの頭」で始まるテキストか。読み上げでは
// マーカー記号自体は落とす（plainify）ため、代わりに直前へ一拍（前拍）を置いて構造の
// 切れ目を耳で分かるようにする。tts.ts（ストリーミング）と readerText.ts（朗読）が使う。
const BLOCK_HEAD = /^\s*([-*+・•]\s|\d+[.)．]\s|#{1,6}\s|>\s)/;

export function startsBlock(s: string): boolean {
  return BLOCK_HEAD.test(s);
}

// --- 溜め（言いよどみ・間の演出） -------------------------------------------------
// 「――また、行く。」「……一日中、って。」のように行頭がダッシュ連続（――/——/―― 等）や
// 三点リーダ連続（……/... 等）で始まる表現は、小説・台詞回しで「一拍おいてから話す」溜めの
// 合図（実機報告）。マーカー自体は読まず（下の plainify のストリップ）、代わりに通常の
// ブロック頭より長い前拍（TAME_BEAT。tts.ts/readerText.ts が値を持つ）を置いて溜めを再現する。
// ダッシュは全角系（— em/– en/― 横棒）のみ対象 — 通常のハイフン "-" は箇条書きマーカー
// （BLOCK_HEAD）と衝突するため含めない。三点リーダは全角（… U+2026）・半角連続（... 3 個以上）
// どちらも対象。直後が空白/行末なら（語尾の伸ばし等）対象外＝文字が続く場合だけ「溜めてから
// 話す」用法とみなす。末尾の lookahead は「マーカー文字でも空白でもない文字」を要求する —
// 素の \S だと "―― "（2 個目のダッシュ自身が非空白）にマッチしてしまい、マーカーだけの行を
// 誤検知する。
const TAME_LEAD = /^\s*(?:[—–―]+|\.{3,}|…+)(?=[^\s—–―.…])/;

export function startsTame(s: string): boolean {
  return TAME_LEAD.test(s);
}

// 文中の溜め（――・……）: 行頭以外に出るダッシュ連続・三点リーダ連続（例:「……一日中、って。」
// 「そして――彼は言った。」）は行頭のような「合成の前」ではなく 1 回の合成の内側なので、
// 前拍（TAME_BEAT）を挟む再生スケジュールの間では作れない（助詞の小休止と同じ制約。
// pauseParticles 参照）。読点（VOICEVOX の句点より短いポーズ）へ差し替えて、テキスト側で
// 「一拍おいて話す」間を作る。行頭のダッシュは plainify が既にマーカーごと消している
// （TAME_LEAD・TAME_BEAT の前拍で表現）ので、ここに残るのは行頭以外の出現だけになる。
// 三点リーダは全角（… U+2026）・半角連続（... 3 個以上）どちらも対象、直後に読点/句点/
// 文末が続く場合は二重の間になるので追加しない。
const TAME_MID = /[—–―]+|(?:\.{3,}|…+)/g;
const TAME_FOLLOWED_BY_PAUSE = /^[、。．！？!?」』）】\s]|^$/;

function tameMidToPause(t: string): string {
  return t.replace(TAME_MID, (m, offset: number, s: string) => {
    const rest = s.slice(offset + m.length);
    return TAME_FOLLOWED_BY_PAUSE.test(rest) ? "" : "、";
  });
}
