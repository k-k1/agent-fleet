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
export const CODE_FILLERS = ["なんとか", "ふがふが", "むにゅむにゅ"];

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

// --- 組み込みの読み補正 ------------------------------------------------------------
// VOICEVOX が読み間違えやすい開発文脈の一般語を補正する（例: 空レポ→そられぽ）。
// ユーザー/テナント辞書の**後**に適用するので、同じ表記をユーザーが定義すればそちらが勝つ
// （先に置換されて表記が消えるため）。「空＋カタカナ語」（空レポ・空リスト等）は規則で
// 「から」に、漢字が続く複合語は個別エントリで持つ。航空券・空港などの熟語や
// 「空が青い」（ひらがな続き）は触らない。
const KARA_KATAKANA = /空(?=[ァ-ヶー])/g;
const BUILTIN_READINGS: [string, string][] = [
  ["空文字", "から文字"], // 空文字列も前方一致で拾う
  ["空配列", "から配列"],
  ["空要素", "から要素"],
  ["空判定", "から判定"],
  ["空行", "からぎょう"], // 行 単体は読みが揺れる（こう/ぎょう）ので読みまで固定
];

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

export function applyBuiltinReadings(text: string): string {
  let t = text.replace(KARA_KATAKANA, "から");
  for (const [re, to] of NUMERONYMS) t = t.replace(re, to);
  for (const [re, to] of UPPER_ACRONYMS) t = t.replace(re, to);
  return applyUserDict(t, BUILTIN_READINGS);
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
      // 裸のハッシュ（バッククォート無しの f437e17 等）もコード片と同じ省略読みに
      .replace(BARE_HASH_RE, (m) => (code?.abbrev && isBareHash(m) ? abbrevCode(m, code.dict) : m))
      // 行頭の見出し/引用/リストマーカー
      .replace(/^\s{0,3}(#{1,6}\s+|>\s+|[-*+]\s+|\d+\.\s+)/gm, "")
      // 強調・打ち消し
      .replace(/(\*\*|__|~~|\*|_)(.*?)\1/g, "$2")
      // 水平線
      .replace(/^\s*([-*_]\s*){3,}$/gm, "")
      // 余分な空白の圧縮
      .replace(/[ \t]+/g, " ")
      .trim()
  );
}
