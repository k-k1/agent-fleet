// features/chat/ttsText — 読み上げ用テキスト整形・分割（純ロジック、依存なし）。
// Markdown 記法・コードブロック・リンク・URL を落として TTS に渡せる素のテキストにする。
// tts.ts から使う。ブラウザ API に触れないので node の vitest で直接テストできる。

// --- ユーザー読み仮名辞書（表記→読みのリテラル置換） ----------------------------
// 設定 ttsUserDict（1 行 "表記=読み"）を、読み上げ用に整形したテキストへ素朴な文字列置換で
// 適用する。英語/日本語/記号どれでも効き、enkana(英語→カタカナ)の前段で当たるので、
// enkana の ON/OFF に依らずユーザー指定の読みが優先される。VOICEVOX のユーザー辞書と同じ発想。

// parseUserDict は設定文字列を [表記, 読み] の配列に。空行と # 始まりはコメント。区切りは
// 半角/全角の = 。表記が空の行は捨てる。長い表記を先に当てられるよう表記長の降順で返す。
export function parseUserDict(raw: string): [string, string][] {
  if (!raw) return [];
  const pairs: [string, string][] = [];
  for (const line of raw.split(/\r?\n/)) {
    const t = line.trim();
    if (!t || t.startsWith("#")) continue;
    const eq = t.search(/[=＝]/);
    if (eq <= 0) continue; // 区切り無し、または表記が空
    const from = t.slice(0, eq).trim();
    const to = t.slice(eq + 1).trim();
    if (!from) continue; // 読みは空でも可（その語を読み飛ばす用途）
    pairs.push([from, to]);
  }
  pairs.sort((a, b) => b[0].length - a[0].length); // 長い表記から適用（部分一致の取りこぼし防止）
  return pairs;
}

// applyUserDict は辞書をテキストへリテラル置換で適用する（全出現・長い表記優先）。
// split/join なので正規表現エスケープ不要。dict は parseUserDict の出力を想定。
export function applyUserDict(text: string, dict: [string, string][]): string {
  let out = text;
  for (const [from, to] of dict) {
    if (from) out = out.split(from).join(to);
  }
  return out;
}

// mergeDicts はユーザー辞書とテナント共通辞書を合成する。同じ表記はユーザー側が勝つ
// （上書き。読みを空にして「読み飛ばす」上書きも効く）。返りは表記長の降順に並べ直し、
// applyUserDict / abbrevCode の「長い表記から当てる」前提を保つ。
export function mergeDicts(user: [string, string][], tenant: [string, string][]): [string, string][] {
  if (!tenant.length) return user;
  const seen = new Set(user.map(([from]) => from));
  const out = [...user, ...tenant.filter(([from]) => !seen.has(from))];
  out.sort((a, b) => b[0].length - a[0].length);
  return out;
}

// --- 開始レイテンシ短縮（最初の 1 文だけ早出し） --------------------------------
// 長い第 1 文が句点で終わるまで待つと発話開始が遅い。**最初の発話に限り**句点を待たず、
// 読点などの軽い区切り（十分な長さがあれば）か、区切りが来なくても一定長で切り出して
// 鳴らし始める。2 文目以降は tts.ts が従来どおり句点粒度で切る（過度な細切れを避ける）。
const FIRST_MIN = 10; // 早出しの最小長（これ未満では切らない＝出だしが細切れにならない）
const FIRST_MAX = 28; // 区切りが来なくてもこの長さで最初だけ強制的に切る
const EARLY_BREAK = /[、，,；;）」』】]/; // 早出しの軽い区切り（読点・閉じ括弧類）

// firstChunkCut は「最初の発話」を早出しするための切り出し位置（1-origin の終端 index）を返す。
// 切らない場合は -1。読点等が FIRST_MIN 以降にあればそこで、無ければ FIRST_MAX で強制的に切る。
export function firstChunkCut(buf: string): number {
  const m = buf.match(EARLY_BREAK);
  if (m && m.index! + 1 >= FIRST_MIN) return m.index! + 1;
  if (buf.length >= FIRST_MAX) return FIRST_MAX;
  return -1;
}

// --- インラインコードの省略読み ---------------------------------------------------
// `e79853e` のようなコード片は素直に読んでも意味が無いので、頭だけ読んで残りをフィラー語
// に置き換える。フィラーはトークン内容から決定的に選ぶ（同じトークンは常に同じ語尾 →
// 合成キャッシュが効き、聞き直しでも安定。トークンごとには変わるのでランダム感は残る）。
export const CODE_FILLERS = [
  "なんとか",
  "ふがふが",
  "むにゅむにゅ",
  "ごにょごにょ",
  "なんちゃら",
  "ほにゃらら",
  "もにょもにょ",
  "うにゃうにゃ",
  "なんとかかんとか",
  "もごもご",
];

// plainify / abbrevCode に渡す省略読みの文脈。dict はユーザー読み仮名辞書（辞書に掛かる
// トークンは省略しない＝辞書優先。実際の置換は後段の applyUserDict が行う）。
export interface CodeReadOpts {
  abbrev: boolean;
  dict: [string, string][];
}

const JA_CHAR = /[ぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/;

// codeWords は camelCase 境界と区切り記号からアルファベットの語（2 文字以上）を抜き出す。
// 語が取れないトークン（ハッシュ・バージョン番号等）は「読める語が無い」扱いになる。
function codeWords(token: string): string[] {
  return (
    token
      .replace(/([a-z0-9])([A-Z])/g, "$1 $2") // fooBar → foo Bar
      .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2") // TTSEnabled → TTS Enabled
      .match(/[A-Za-z]{2,}/g) ?? []
  );
}

// --- 裸のハッシュの判定 -----------------------------------------------------------
// バッククォートで括られていない生ハッシュ（f437e17 等）も省略読みの対象にする。地の文に
// 混ざるので、対象は「16 進ハッシュにしか見えない」トークンに厳しく絞る:
//  - 小文字 16 進のみ・7 文字以上（git 短縮ハッシュの下限）
//  - 数字と英字の両方を含む（英字のみ → facade / deadbeef 等の英単語を守る。数字のみ →
//    トークン数・タイムスタンプ等の長い数値はそのまま読む）
//  - UUID（8-4-4-4-12）は構造だけで判定できるので数字英字の混在は求めない
// 適用は plainify の地の文ステップ（コード片と同じ ttsAbbrevCode 設定でゲート）。読み方は
// abbrevCode のハッシュ枝（頭 2 文字＋フィラー・辞書優先）に乗る。
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

export function isBareHash(t: string): boolean {
  if (UUID_RE.test(t)) return true;
  return /^[0-9a-f]{7,}$/.test(t) && /\d/.test(t) && /[a-f]/.test(t);
}

// 地の文からハッシュ候補を拾う正規表現（UUID を先に、次に 16 進列。最終判定は isBareHash。
// 末尾の \b で「16 進の後に英字が続く語」= deadbeef12ghost のような識別子の頭は拾わない）。
const BARE_HASH_RE = /\b(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{7,})\b/g;

// abbrevCode はインラインコード 1 個の読み上げ形を決める。
//  - 短い（<6 文字）・空白を含むコマンド/フレーズ・日本語入り・ユーザー辞書に掛かるもの → そのまま
//  - ハッシュ・UUID（isBareHash）→ 頭 2 文字＋フィラー
//  - 純粋な 1 単語（vitest 等）→ そのまま
//  - 2 語（ttsEnabled 等）→ 頭一語＋フィラー
//  - 3 語以上（ttsAutoReadMirror・パス等）→ 頭一語＋フィラー＋末尾一語
//  - 語が無い（バージョン番号等）→ 頭 2 文字＋フィラー
export function abbrevCode(token: string, dict: [string, string][] = []): string {
  const t = token.trim();
  if (t.length < 6) return token;
  if (/\s/.test(t)) return token;
  if (JA_CHAR.test(t)) return token;
  if (dict.some(([from]) => from && t.includes(from))) return token; // 辞書優先
  let sum = 0;
  for (const c of t) sum += c.codePointAt(0)!;
  const filler = CODE_FILLERS[sum % CODE_FILLERS.length];
  // ハッシュは語の切り出しに掛けない（長い SHA は偶然の英字連続を「語」と誤認するため）。
  if (isBareHash(t)) return `${t.slice(0, 2)} ${filler}`;
  const words = codeWords(t);
  if (words.length === 1 && words[0] === t) return token; // 純粋な 1 単語
  if (words.length >= 3) return `${words[0]} ${filler} ${words[words.length - 1]}`;
  if (words.length === 2) return `${words[0]} ${filler}`;
  return `${t.slice(0, 2)} ${filler}`;
}

// --- パスの省略読み ---------------------------------------------------------------
// 絶対パス(/a/b/c)・相対パス(./a/b)・多段パス(a/b/c.ext) を素直に読むと全ディレクトリを逐一
// 読み上げて冗長きわまる。頭のセグメント＋「面倒で途中を省く感じ」のフィラー＋末尾（ファイル名）
// に畳む（例: console/src/features/chat/tts.ts → 「console、なんとかかんとか、tts.ts」）。読点で
// 挟むと自然に間が空き、長い階層を端折った雰囲気になる。日付(2024/01/02)や数値列は畳まない。
// インラインコードの abbrevCode と同じ ttsAbbrevCode でゲート（plainify に code?.abbrev で渡る）。
// セグメントは 3 個以上のときだけ畳む（2 個以下はそのまま読んでも短い）。
const PATH_RE = /(?:\.{0,2}\/)?(?:[\w.-]+\/){2,}[\w.-]+/g;

// 「間の階層を面倒で省いた」感を出すフィラー。パスから決定的に選ぶ（同じパスは常に同じ）。
export const PATH_FILLERS = ["なんとかかんとか", "ずらずらっと", "うんぬんかんぬん", "ごにょごにょ"];

export function abbrevPath(path: string): string {
  const segs = path.split("/").filter((s) => s && s !== "." && s !== ".."); // 空・./・../ は落とす
  if (segs.length <= 2) return path; // 短いパスはそのまま
  if (segs.every((s) => /^\d+$/.test(s))) return path; // 日付・数値列（2024/01/02 等）は畳まない
  let sum = 0;
  for (const c of path) sum += c.codePointAt(0)!;
  const filler = PATH_FILLERS[sum % PATH_FILLERS.length];
  return `${segs[0]}、${filler}、${segs[segs.length - 1]}`;
}

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
// "." で分断されるので、ここでカタカナに確定させる。init.d（SysV 初期化スクリプト置き場）等。
const DOTTED_TERMS: [RegExp, string][] = [[/\binit\.d\b/gi, "イニットディー"]];

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

// --- 保留中の質問（AskUserQuestion）の読み上げ文 ---------------------------------
// ミラーが確認待ちになったとき、質問文と選択肢を音声用の 1 本のテキストに組む。選択肢は
// 画面の表示ラベル（短縮されがち）ではなく **説明文（ツールチップの中身）を優先**して読む。
// question/option は Markdown 断片のことがあるので plainify を通す。
export interface SpokenQuestion {
  question?: string;
  multiSelect?: boolean;
  options?: { label: string; description?: string }[];
}

export function pendingSpeech(qs: SpokenQuestion[]): string {
  const parts: string[] = ["確認です。"];
  const period = (t: string) => (/[。．！？!?]$/.test(t) ? t : t + "。");
  qs.forEach((q, qi) => {
    if (qs.length > 1) parts.push(`質問${qi + 1}。`);
    const question = q.question ? plainify(q.question).trim() : "";
    if (question) parts.push(period(question));
    const opts = q.options || [];
    if (opts.length) {
      parts.push(`選択肢は${opts.length}つ。`);
      opts.forEach((o, oi) => {
        const alt = plainify(o.description || o.label).trim();
        if (alt) parts.push(`${oi + 1}、${period(alt)}`);
      });
    }
    if (q.multiSelect) parts.push("複数選択できます。");
  });
  return parts.join("");
}

// --- 文の感情推定（感情スタイル読み分け用） --------------------------------------
// 文にエラー・失敗系の語があれば "angry"（ツンツン系スタイル）、成功・完了系なら
// "happy"（あまあま系）、どちらも無ければ null（ノーマル）。読み上げ済みテキスト
// （プレーン化後の 1 文）に対するキーワード判定で、angry を優先する（失敗の報告に
// 成功語が混ざることはあっても逆は稀なため）。
const ANGRY_WORDS = ["エラー", "失敗", "例外", "できませんでした", "落ちました", "error", "fail"];
const HAPPY_WORDS = ["成功", "完了", "できました", "通りました", "問題ありません", "green", "passed", "✅", "🎉"];

export function emotionOf(text: string): "happy" | "angry" | null {
  const t = text.toLowerCase();
  for (const w of ANGRY_WORDS) if (t.includes(w)) return "angry";
  for (const w of HAPPY_WORDS) if (t.includes(w)) return "happy";
  return null;
}

// --- レンダ済みテキストの文分割（ミラーのカラオケ朗読用） ------------------------
// textContent 由来のテキスト（Markdown 記法は既に落ちている）を文単位に割る。句点は前の
// 文に含める。改行・連続空白は 1 つの空白に潰し、かな/漢字/英数字を 1 つも含まない断片
// （罫線・記号だけ等）は捨てる。
const SENT_END = "。．！？!?";
const SPEAKABLE = /[0-9A-Za-zぁ-んァ-ヶーｦ-ﾟ一-鿿㐀-䶿豈-﫿々]/;

// --- 長文の合成用分割 --------------------------------------------------------------
// 句点で切った「1 文」が長すぎるとき、合成用にさらに弱い区切り（読点・中黒・スラッシュ・
// ダッシュ・閉じ括弧など）で割る。合成 1 回が長いと CPU エンジンの合成時間がそのまま
// 無音の待ちになる（先読みの息切れ）ため、開始レイテンシとパイプラインの持続性を優先する。
// 区切りは前の片に含める。max までに区切りが無ければ長さで強制分割。呼び手（tts.ts の
// submit / turnTts / ReaderView）は途中の片の間を詰めて連続再生し、ハイライトは元の文の
// 単位のまま扱う。
const SENT_SPLIT_BREAK = "、，・；：／—–」』）】";
const SPLIT_HEAD_MIN = 8; // 先頭 8 文字未満では切らない（細切れ防止）

export function splitLongSentence(s: string, max = 60): string[] {
  const out: string[] = [];
  let rest = s;
  while (rest.length > max) {
    let cut = -1;
    for (let i = max; i >= SPLIT_HEAD_MIN; i--) {
      if (SENT_SPLIT_BREAK.includes(rest[i])) {
        cut = i + 1;
        break;
      }
    }
    if (cut < 0) cut = max; // 区切りが無い → 長さで強制分割
    out.push(rest.slice(0, cut));
    rest = rest.slice(cut);
  }
  if (rest) out.push(rest);
  return out;
}

export function splitSentences(text: string): string[] {
  const out: string[] = [];
  let buf = "";
  const flush = () => {
    const t = buf.replace(/\s+/g, " ").trim();
    buf = "";
    if (t && SPEAKABLE.test(t)) out.push(t);
  };
  for (const c of text) {
    buf += c;
    if (SENT_END.includes(c)) flush();
  }
  flush();
  return out;
}

// plainifyStreaming — 1 文分のテキストを読み上げ用にプレーン化。```fence``` は
// またぎ状態（inFence）を引き回して内側を丸ごと落とす。
export function plainifyStreaming(
  s: string,
  fence: { get: () => boolean; set: (v: boolean) => void },
  code?: CodeReadOpts,
): string {
  const out: string[] = [];
  let rest = s;
  while (rest.length) {
    const i = rest.indexOf("```");
    if (i < 0) {
      out.push(fence.get() ? "" : rest);
      rest = "";
      break;
    }
    if (!fence.get()) out.push(rest.slice(0, i));
    fence.set(!fence.get());
    rest = rest.slice(i + 3);
  }
  return plainify(out.join(""), code);
}

// plainify — Markdown 記法・リンク・URL・記号を落として読み上げ用テキストにする。
// fence の除去は plainifyStreaming が済ませている前提。code を渡すとインラインコードを
// 省略読み（abbrevCode）にする（未指定は従来どおり中身をそのまま）。
export function plainify(s: string, code?: CodeReadOpts): string {
  return (
    s
      // インラインコード `x` → x（省略読み有効時は abbrevCode で頭＋フィラーに）
      .replace(/`([^`]*)`/g, (_, p: string) => (code?.abbrev ? abbrevCode(p, code.dict) : p))
      // 画像 ![alt](url) → 落とす
      .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
      // リンク [text](url) → text
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      // 裸の URL は読まない
      .replace(/https?:\/\/\S+/g, "")
      // 裸のパス（バッククォート無しの /a/b/c.ts, ./src/foo 等）は頭＋末尾に畳む
      .replace(PATH_RE, (m) => (code?.abbrev ? abbrevPath(m) : m))
      // 裸のハッシュ（バッククォート無しの f437e17 等）もコード片と同じ省略読みに
      .replace(BARE_HASH_RE, (m) => (code?.abbrev && isBareHash(m) ? abbrevCode(m, code.dict) : m))
      // 行頭の見出し/引用/リストマーカー
      .replace(/^\s{0,3}(#{1,6}\s+|>\s+|[-*+]\s+|\d+\.\s+)/gm, "")
      // 行頭の溜め（――・……等）はマーカーなので読まない（間は preGaps 側の TAME_BEAT で表現）
      .replace(/^\s*(?:[—–―]+|\.{3,}|…+)(?=[^\s—–―.…])/gm, "")
      // 強調・打ち消し
      .replace(/(\*\*|__|~~|\*|_)(.*?)\1/g, "$2")
      // 水平線
      .replace(/^\s*([-*_]\s*){3,}$/gm, "")
      // 余分な空白の圧縮
      .replace(/[ \t]+/g, " ")
      .trim()
  );
}
