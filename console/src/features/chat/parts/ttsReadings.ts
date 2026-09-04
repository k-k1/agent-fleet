import { applyUserDict } from "./ttsUserDict.ts";

// --- Built-in reading corrections --------------------------------------------------
// Corrects everyday words in a development context that VOICEVOX tends to mispronounce. Applied
// after the user/tenant dictionary, so a user entry for the same spelling wins (it has already
// been substituted and the spelling is gone by the time these run). 空 followed by a katakana
// word is handled by a rule (read as "kara"); compounds where a kanji follows get their own
// entry. Set phrases such as 航空券 / 空港, and 空 followed by hiragana ("the sky is blue"),
// are left alone.
const KARA_KATAKANA = /空(?=[ァ-ヶー])/g;
// 身体 reads naturally as "karada" alone, before a particle or before a native-Japanese word,
// but a Sino-Japanese compound with a kanji following (身体検査, 身体能力, 身体機能) is
// "shintai". Leave compounds as written for OpenJTalk's own dictionary and pin the kun reading
// only when what follows is not a kanji.
const KARADA_BODY = /身体(?![一-鿿㐀-䶿豈-﫿々])/g;
const BUILTIN_READINGS: [string, string][] = [
  ["空文字", "から文字"], // prefix match also catches 空文字列
  ["空配列", "から配列"],
  ["空要素", "から要素"],
  ["空判定", "からはんてい"], // pin 判定 to "hantei" (matching the 判定 entry below) and 空 to "kara" at once
  ["空行", "からぎょう"], // 行 on its own is unstable (kou/gyou), so pin the reading too
  // Compounds where 行 means "line" and the meaning is unambiguous without okurigana. Pinned
  // before fixGyoLine runs.
  ["行目", "ぎょうめ"],
  ["行数", "ぎょうすう"],
  ["行末", "ぎょうまつ"],
  ["行頭", "ぎょうとう"],
  ["行番号", "ぎょうばんごう"],
  ["行全体", "ぎょうぜんたい"],
  // In 判定 the 定 turns into "jou" depending on context (measured: 誤判定 read "gohanjou").
  ["判定", "はんてい"],
  // In 貼り付け the 貼 gets voiced into "baritsuke" (measured). Prefix match also covers
  // 貼り付ける / 貼り付けた.
  ["貼り付け", "はりつけ"],
  // In 型チェック the 型 gets voiced into "gata" (measured). 型 legitimately reads kei/gata in
  // other compounds (模型, 大型), so no blanket substitution: pin only the compounds that are
  // unambiguously "kata" in a development context.
  ["型チェック", "かたチェック"],
  // 言って (te-form of 言う) is misread as "geitte" in short address-like contexts at the end of
  // a sentence or right after a break (measured). It is always "itte" - the word has no other
  // reading - so pinning it is safe.
  ["言って", "いって"],
  // 放って is a homograph: te-form of 放る (hou-ru) = "houtte", the idiom 放っておく/おかれる
  // "leave it be"; and of 放つ (hana-tsu) = "hanatte", releasing light, an arrow, a voice.
  // VOICEVOX tends to pick the latter (measured). 放ってお (放っておく/おいた/おかれる/おいて,
  // i.e. followed by おく) can only be the "leave it be" idiom, so pin just that shape. Bare
  // 放って may still be "hanatte", so it is left alone and the two stay distinguishable.
  ["放ってお", "ほうってお"],
  // あり様 is pinned to "ariyou" (the way something ought to be, as in "~のありよう") (measured).
  ["あり様", "ありよう"],
  // --- Sino-Japanese terms used in development that OpenJTalk tends to read away from the
  //     conventional IT reading. Each has a single reading in an IT context, so pinning is safe
  //     and does not clash with the everyday alternative reading. ---
  ["引数", "ひきすう"], // easily turns into "insuu"
  ["添字", "そえじ"], // easily turns into "tenji"
  ["閾値", "しきいち"], // also read "ikichi", but the IT convention is "shikiichi"
  ["相殺", "そうさい"], // often misread "sousatsu"
  ["脆弱性", "ぜいじゃくせい"], // the 脆 drops out and it turns into "kijaku" and the like
  ["端数", "はすう"], // often misread "tansuu"
  ["冪等", "べきとう"], // 冪 is outside the common-use kanji and sometimes unreadable
];

// GYO_KO_COMPOUNDS - Sino-Japanese words that end in 行 but are not read "gyou" (usually "kou").
// They are excluded from fixGyoLine's default (行 -> ぎょう) and left to OpenJTalk. The user's
// policy is "never kudari, essentially always gyou", so 行 = line/row defaults to gyou while
// these established two-kanji words are protected. When a "kou" compound comes out as
// kudari/gyou, add one word here. 修行 (shugyou) / 奉行 (bugyou) are "gyou" but get misread by
// decomposition (修 -> shuu), so they are excluded the same way to keep their original reading.
const GYO_KO_COMPOUNDS = new Set([
  "実行", "移行", "発行", "進行", "現行", "並行", "平行", "続行", "代行", "施行", "履行",
  "直行", "運行", "先行", "通行", "遂行", "執行", "携行", "同行", "銀行", "旅行", "飛行",
  "航行", "歩行", "走行", "慣行", "強行", "決行", "断行", "紀行", "刊行", "興行", "徐行",
  "急行", "逆行", "蛇行", "夜行", "修行", "苦行", "潜行", "素行", "品行", "暴行", "犯行",
  "非行", "悪行", "善行", "孝行", "横行", "流行", "励行", "尾行", "随行",
]);

// fixGyoLine - pin 行 = line/row to "gyou" by default ("kudari" is never wanted in a development
// or data context). 集計行, 統計行, WT行, 3行, a bare 行, 行を and so on become gyou, while these
// are left alone to avoid misreadings:
//   - followed by a kanji (行動, 行政, 行為, 行程, 行間 - OpenJTalk reads kou/gyou correctly)
//   - followed by okurigana, i.e. hiragana (行く, 行う, 行った; the particles を/が/は/も/に/へ/
//     で/と/の count as "line" and do become gyou)
//   - followed by katakana or a long vowel mark, or preceding-char + 行 is in GYO_KO_COMPOUNDS
// The single preceding character is preserved (集計行 -> 集計ぎょう; OpenJTalk reads the leading
// kanji). No lookbehind, for Safari below 16.4.
const GYO_LINE = /(.?)行(?=[をがはもにへでとの]|[^ぁ-んァ-ヶ一-鿿々ー]|$)/g;

function fixGyoLine(t: string): string {
  return t.replace(GYO_LINE, (m, prev: string) => (GYO_KO_COMPOUNDS.has(prev + "行") ? m : prev + "ぎょう"));
}

// GO_PREFIX - the prefix 誤 is the on reading "go" (誤表示, 誤判定, 誤検知, 誤動作, 誤操作,
// 誤入力). OpenJTalk falls back to the kun reading "ayama" depending on context (measured:
// 誤表示 read "ayamahyouji"). A 誤 + kanji compound is always "go", so pin it. Verbs and nouns
// with okurigana (誤る, 誤り, 誤って) are followed by hiragana and so fall outside the pattern,
// keeping "ayama". Applied before BUILTIN_READINGS (判定 -> はんてい etc.) so that the compound
// substitution downstream still fires: 誤判定 -> ご判定 -> ごはんてい.
const GO_PREFIX = /誤(?=[一-鿿々])/g;

// KANAME - pin 要 to "kaname" only where が/は/も + 要 ends the sentence as a standalone noun
// (the crux, the vital point); measured on "ここが要". 要 splits three ways: "you" in compounds
// (必要, 要素, 重要), "iru" as the verb 要る (要らない etc.), and "kaname". To avoid dragging in
// compounds or the conjugations of 要る (要ら/要り/要る/要れ/要ろ), the match is narrowed to
// where no kanji and no inflection containing る follows - i.e. before punctuation, end of
// sentence, or です/だ. が要注意 / が要素 / が要る are excluded and keep you/iru.
const KANAME = /([がはも])要(?=[。、！？」』）\s]|です|だ|$)/g;

// KONO_YOU_NA - この/その/あの/どの + 様な・様に is a fixed demonstrative expression that is
// always "you" (そのような, このように). 様 splits between "sama" (the honorific in お客様,
// 皆様) and "you" (様子, そのよう) and gets read wrongly for the same reason as above (measured:
// その様な read "sonosamana"). Restricting the match to a demonstrative + 様 followed by な/に
// is safe: compounds such as 様子 and 様々 are followed by 子/々 and stay out of scope.
const KONO_YOU_NA = /(この|その|あの|どの)様(?=な|に)/g;

// Numeronyms (i18n = internationalization and friends). The digit is a count of elided letters,
// not a number, so expand to the conventional katakana of the original word rather than letting
// it be read as a number. Word-bounded and case-insensitive, so I18n / A11Y are caught too. The
// list is the IT entries of the Japanese Wikipedia "numeronym" article plus what comes up in
// development (o11y, e2e). enkana (English -> katakana, on the CP) runs after this, but these
// are already expanded here so nothing is converted twice.
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

// All-caps acronyms whose reading is stolen by an identically spelled English word (IT loses to
// the CMU dictionary's it). Matched case-sensitively at a word boundary - lowercase "it" is the
// English pronoun and must be left alone. It is handled here because enkana's dictionary
// lowercases its keys and therefore cannot tell the two apart.
const UPPER_ACRONYMS: [RegExp, string][] = [[/\bIT\b/g, "アイティー"]];

// The product name (this repository itself). It is written as "Agent-Fleet", "Agent fleet" or
// "AgentFleet"; handed to enkana (on the CP) the hyphen survives and breaks the utterance, and
// "agent" alone is misread as "eijanto" via CMUdict (measured). Pin it to katakana before
// enkana so every spelling variant is absorbed here.
const PRODUCT_NAMES: [RegExp, string][] = [[/agent[\s-]?fleet/gi, "エージェントフリート"]];

// Fixed tokens that span a period do not survive enkana (which works on alphanumeric tokens on
// the CP side): it splits them at ".". Pin them to katakana here - "*.d" directories such as
// init.d, and config file names such as resolv.conf. The \b word boundaries keep other words
// such as cron.daily from matching.
const DOTTED_TERMS: [RegExp, string][] = [
  [/\binit\.d\b/gi, "イニットドットディー"],
  [/\bcron\.d\b/gi, "クロンドットディー"],
  [/\brc\.d\b/gi, "アールシードットディー"],
  [/\bconf\.d\b/gi, "コンフドットディー"],
  [/\bsudoers\.d\b/gi, "スードゥアーズドットディー"],
  [/\bresolv\.conf\b/gi, "リゾルブドットコンフ"],
];

// --- Tightening the pause at a bare slash separator (outside code) ----------------
// Bare (unbackticked) slash separators such as "origin/main", "on/off", "read/write" make
// VOICEVOX (OpenJTalk) treat "/" as a symbol and insert a pause, which sounds far too long in
// running prose (measured). Substitute a middle dot, the natural separator for a list or an
// either/or and effectively silent. Three or more segments (two or more "/") are out of scope
// because plainify's PATH_RE/abbrevPath folds them into "head, filler, tail" first; whatever did
// not go through that because ttsAbbrevCode is off is caught here. Dates, fractions and division
// (2024/01/02, 1/2) are excluded only when every side is digits, and left as written. The
// character class is restricted to alphanumerics, katakana and the long vowel mark: including
// hiragana or kanji lets surrounding prose bleed into the token ("確率は1/2です") and the
// digits-only test stops working.
const SLASH_CHAIN = /[A-Za-z0-9ァ-ヶー]+(?:\/[A-Za-z0-9ァ-ヶー]+)+/g;

function shortenSlashPause(t: string): string {
  return t.replace(SLASH_CHAIN, (m) => {
    const segs = m.split("/");
    if (segs.every((s) => /^\d+$/.test(s))) return m; // leave dates, fractions and division alone
    return segs.join("・");
  });
}

// --- Japanese readings for dates and times -----------------------------------
// Handing symbol-separated numbers straight to the speech engine makes it read the hyphen,
// slash and colon as symbols. Expand only dates and times in a real range into year/month/day
// and hour/minute/second. M/D is ambiguous with a fraction or division, so a date wins except
// where the context is unmistakably arithmetic.
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

// --- Reading the wave dash (U+301C / U+FF5E) two different ways ---------------------
// A range such as "3〜5倍速" should be read "3 kara 5 baisoku", whereas a run of them ("詳細は
// ほにゃらら〜〜〜") stands for elision or hesitation, so the two must be told apart. The wide
// form (～ U+FF5E) and the JIS form (〜 U+301C) both arrive depending on the OS/IME and are
// treated identically.
//  1. Collapse runs (two or more) into "honyarara" first. Order matters: run before the range
//     rule, or each one turns into "kara" separately.
//  2. Convert a remaining single wave dash to "kara" only when a character other than
//     whitespace, punctuation or end of text follows - i.e. it sits between two values as a
//     range separator. A drawn-out casual ending ("そうだね〜", "〜。": end of text,
//     punctuation or whitespace after it) is out of scope and left to VOICEVOX's default.
const TILDE = "〜～";
const TILDE_RUN = new RegExp(`[${TILDE}]{2,}`, "g");
const TILDE_RANGE = new RegExp(`([^\\s${TILDE}])[${TILDE}](?=[^\\s${TILDE}。、！？!?，,])`, "g");

function applyTildeReadings(text: string): string {
  return text.replace(TILDE_RUN, "ほにゃらら").replace(TILDE_RANGE, "$1から");
}

// OpenJTalk skips the section sign § (and a run §§) or reads it as a symbol, so replace it with
// "section". A following number is left as a number: §3 -> section 3.
const SECTION_SIGN = /§+/g;

export function applyBuiltinReadings(text: string): string {
  let t = applyDateTimeReadings(text);
  t = t.replace(SECTION_SIGN, "セクション");
  t = shortenSlashPause(t);
  t = tameMidToPause(t); // mid-text dramatic pauses (-- , ...); line-leading ones are already handled by plainify
  t = t.replace(KARA_KATAKANA, "から");
  t = applyTildeReadings(t);
  for (const [re, to] of NUMERONYMS) t = t.replace(re, to);
  for (const [re, to] of PRODUCT_NAMES) t = t.replace(re, to);
  for (const [re, to] of DOTTED_TERMS) t = t.replace(re, to);
  for (const [re, to] of UPPER_ACRONYMS) t = t.replace(re, to);
  t = t.replace(GO_PREFIX, "ご"); // prefix 誤 = go (誤表示, 誤判定); must precede the 判定 substitution
  t = t.replace(KANAME, "$1かなめ"); // が/は/も + 要 before end of sentence, punctuation or です/だ
  t = t.replace(KONO_YOU_NA, "$1よう"); // この/その/あの/どの + 様な・様に = you
  t = t.replace(KARADA_BODY, "からだ"); // pin only the kun reading; Sino-Japanese compounds are protected
  t = applyUserDict(t, BUILTIN_READINGS); // compounds such as 行目 / 判定, pinned before fixGyoLine
  t = fixGyoLine(t); // remaining 行 = line/row defaults to gyou
  return t;
}

// applyReadings is the whole "shape the reading" step run just before speaking: user/tenant
// dictionary (which wins) -> built-in reading corrections -> particle micro-pauses. All three
// paths in tts.ts (streaming, narration, interleaved announcements) go through it. enkana runs
// after this, on the CP side.
export function applyReadings(text: string, dict: [string, string][], particlePause: boolean): string {
  let t = text;
  if (dict.length) t = applyUserDict(t, dict);
  t = applyBuiltinReadings(t);
  if (particlePause) t = pauseParticles(t);
  return t.trim();
}

// --- Micro-pause after a particle -------------------------------------------------
// When を/は/で/に/と is followed by a kanji, insert a comma so the synthesis gets a breath-sized
// gap (神は細部に宿る -> 神は、細部に、宿る). Mid-sentence this is inside a single synthesis, so
// the playback schedule's gaps (SENT_BEAT and friends) cannot produce it; it has to be done in
// the text, as VOICEVOX's comma pause (shorter than a full stop). Nothing is inserted when
// hiragana follows (とき, など, のような): a kanji is far more likely to be a word boundary, so
// this placement misfires rarely. Applied just before synthesis (after the user dictionary) and
// never affects what is displayed.
const PARTICLE_PAUSE = /([をはでにと])(?=[一-鿿㐀-䶿々])/g;

export function pauseParticles(text: string): string {
  return text.replace(PARTICLE_PAUSE, "$1、");
}

// --- Detecting a block head (for the pre-beat before a list, heading or quote) -----
// Does this text start at the head of a new block - a list item, heading or quote? Reading drops
// the marker itself (plainify), so a beat is placed before it instead, making the structural
// break audible. Used by tts.ts (streaming) and readerText.ts (narration).
const BLOCK_HEAD = /^\s*([-*+・•]\s|\d+[.)．]\s|#{1,6}\s|>\s)/;

export function startsBlock(s: string): boolean {
  return BLOCK_HEAD.test(s);
}

// --- Dramatic pause (hesitation, a beat held before speaking) ----------------------
// A line starting with a run of dashes or of ellipsis characters ("――また、行く。",
// "……一日中、って。") is, in prose and dialogue, the cue to hold a beat before speaking
// (measured). The marker itself is not read (plainify strips it); instead a pre-beat longer than
// an ordinary block head reproduces the pause (TAME_BEAT; tts.ts / readerText.ts hold the value).
// Only full-width dashes count (— em, – en, ― horizontal bar): a plain "-" would collide with
// the bullet marker (BLOCK_HEAD). Ellipses count both full-width (… U+2026) and a run of three
// or more ASCII periods. Whitespace or end of line after it (a drawn-out ending) is out of
// scope, so only a marker followed by text counts as the "hold, then speak" usage. The trailing
// lookahead demands a character that is neither a marker character nor whitespace: a bare \S
// would match "―― " on the second dash and misdetect a line consisting only of the marker.
const TAME_LEAD = /^\s*(?:[—–―]+|\.{3,}|…+)(?=[^\s—–―.…])/;

export function startsTame(s: string): boolean {
  return TAME_LEAD.test(s);
}

// Mid-text dramatic pause: a run of dashes or ellipses away from the start of a line
// ("そして――彼は言った。") sits inside a single synthesis rather than before one, so the
// playback schedule's pre-beat (TAME_BEAT) cannot produce it - the same constraint as the
// particle micro-pause, see pauseParticles. Substitute a comma (VOICEVOX's pause, shorter than
// for a full stop) to make the held beat in the text. Line-leading dashes have already been
// removed with their marker by plainify (expressed via TAME_LEAD and the TAME_BEAT pre-beat), so
// only non-leading occurrences reach here. Ellipses count both full-width (… U+2026) and a run
// of three or more ASCII periods; nothing is added when a comma, full stop or end of text
// follows, which would double the pause.
const TAME_MID = /[—–―]+|(?:\.{3,}|…+)/g;
const TAME_FOLLOWED_BY_PAUSE = /^[、。．！？!?」』）】\s]|^$/;

function tameMidToPause(t: string): string {
  return t.replace(TAME_MID, (m, offset: number, s: string) => {
    const rest = s.slice(offset + m.length);
    return TAME_FOLLOWED_BY_PAUSE.test(rest) ? "" : "、";
  });
}
