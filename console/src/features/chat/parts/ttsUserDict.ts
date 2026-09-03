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
