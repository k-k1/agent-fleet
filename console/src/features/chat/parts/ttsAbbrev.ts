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
export const BARE_HASH_RE = /\b(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{7,})\b/g;

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
export const PATH_RE = /(?:\.{0,2}\/)?(?:[\w.-]+\/){2,}[\w.-]+/g;

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
