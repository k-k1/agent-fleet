// 返信サジェスト（クイック返信）— コンポーサー直上に出す短文候補の学習・ランキング。
//
// Layer A: ユーザーが過去に送った短文の頻度/最近度を学習し、上位を候補に出す
//          （ok / 進めて / commit のような常用返信が自然に並ぶ）。
// Layer B-1: 直近のエージェント回答の内容で並びを押し上げる（トークン0のヒューリスティック）。
//
// 保存は settings.quickReplies（ssmHostUsage と同型 = サーバミラーで複数デバイス同期）。
// キーは正規化 + 小文字化（"OK"/"ok" を同一視）、表示テキストは最後に送った綴りを保持。
// ユーザーが消した候補は settings.quickRepliesHidden（キーの配列）に積んで恒久的に隠す
// （シードも隠せる）。同じ文をもう一度自分で送ったら、その意思表示として隠しを解除する。

export type QuickReplyUse = { text: string; count: number; at: number };
export type QuickReplyMap = Record<string, QuickReplyUse>;

// 候補とする短文の上限長（これを超えるものは「クイック返信」ではなく質問文/プロンプトとみなす）。
// 学習時（isQuickReplyCandidate）と表示時（rankQuickReplies）の両方に効かせ、閾値を下げたとき
// 過去に学習済みの長いエントリも遡って隠れるようにする。チップが1行を占有しない長さに合わせて 20。
const MAX_LEN = 20;
// 保存する最大エントリ数。超えたら最弱（count→at 昇順）から間引く。
const MAX_ENTRIES = 60;

// 空白畳み・トリム。表示・突合の両方に使う。
function normalize(text: string): string {
  return text.trim().replace(/\s+/g, " ");
}
function keyOf(text: string): string {
  return normalize(text).toLowerCase();
}

// この送信テキストを学習対象にするか。1行・短い・添付なし・スラッシュ/パス類でないこと。
export function isQuickReplyCandidate(text: string, hasAttachments: boolean): boolean {
  if (hasAttachments) return false;
  const t = text.trim();
  if (!t) return false;
  if (t.length > MAX_LEN) return false;
  if (/[\r\n]/.test(t)) return false; // 複数行は返信テンプレではない
  if (t.startsWith("/")) return false; // スラッシュコマンド / 絶対パス
  return true;
}

// 送信テキストを頻度マップへ記録し、新しいマップを返す（純関数）。上限超過分は間引く。
export function recordQuickReply(map: QuickReplyMap, text: string, now: number): QuickReplyMap {
  const norm = normalize(text);
  const k = keyOf(norm);
  const prev = map[k];
  const next: QuickReplyMap = {
    ...map,
    [k]: { text: norm, count: (prev?.count ?? 0) + 1, at: now },
  };
  const keys = Object.keys(next);
  if (keys.length > MAX_ENTRIES) {
    // 最弱（使用回数→最近度）から落とす。今書いたキーは新しいので残る。
    keys
      .sort((a, b) => next[a].count - next[b].count || next[a].at - next[b].at)
      .slice(0, keys.length - MAX_ENTRIES)
      .forEach((dead) => delete next[dead]);
  }
  return next;
}

// 学習エントリを1件消す（純関数）。表示から消すだけでは次の送信で復活するので、呼び出し側は
// hideQuickReply と対で使う（シードは学習マップに無いので、隠しリスト側だけが効く）。
export function forgetQuickReply(map: QuickReplyMap, text: string): QuickReplyMap {
  const k = keyOf(text);
  if (!(k in map)) return map;
  const next = { ...map };
  delete next[k];
  return next;
}

// 隠しリストにキーを積む。上限は学習エントリと同じ（際限なく増やさない・古いものから落とす）。
export function hideQuickReply(hidden: string[], text: string): string[] {
  const k = keyOf(text);
  if (!k || hidden.includes(k)) return hidden;
  const next = [...hidden, k];
  return next.length > MAX_ENTRIES ? next.slice(next.length - MAX_ENTRIES) : next;
}

// 隠しを解除する。自分でその文を送り直したときに呼ぶ（＝もう一度使う意思表示）。
// 変化が無ければ同じ配列参照を返すので、呼び出し側は差分だけ保存できる。
export function unhideQuickReply(hidden: string[], text: string): string[] {
  const k = keyOf(text);
  if (!hidden.includes(k)) return hidden;
  return hidden.filter((h) => h !== k);
}

// i18n-exempt-start: 以下はサジェストの seed 語と突合用の辞書データ（翻訳対象の UI 文言ではなく、
// locale キーで ja/en を出し分ける“中身”。fontStack の生値や VOICEVOX 名と同じ扱い）。
// 初期シード（学習が空でも ok/進めて/commit が並ぶよう種まき）。count 0 なので実利用が即上回る。
const SEEDS: Record<string, string[]> = {
  ja: ["OK", "進めて", "続けて", "commit して", "やめて"],
  en: ["OK", "Go ahead", "Continue", "Commit it", "Stop"],
};

// 肯定/否定の短答セット（末尾が「？」の回答直後に押し上げる対象）。小文字で突合。
const AFFIRM = new Set(["ok", "はい", "yes", "y", "進めて", "続けて", "go ahead", "continue", "sure"]);
const NEGATE = new Set(["no", "いいえ", "n", "やめて", "待って", "stop", "cancel", "キャンセル"]);

// 直近回答（lastReply）から加点する（B-1）。lastReply は小文字化済みを渡す想定はせず内部で処理。
//
// ★加点は「合算」ではなく「最大値」を採る。合算にすると、たまたま複数のキーワードを含む欲張った
// 一文（例「OK,順に進めよう。都度コミットしてね」= コミット + 進め）が +180 を得て、単語ひとつの
// 素直な候補（「コミット」+100 /「進めて」+80）を構造的に永久に上回り、どの文脈でも先頭に貼り付く。
// 文脈適合は「どれか1つ当たったか」で十分で、当たった数は関連度ではない。
function contextBoost(entryText: string, lastReply: string): number {
  if (!lastReply) return 0;
  const lr = lastReply.toLowerCase();
  const et = entryText.toLowerCase();
  let boost = 0;
  // 質問（末尾「?」/「？」）→ 肯定・否定の短答を押し上げる。
  if (/[?？]\s*$/.test(lastReply)) {
    if (AFFIRM.has(et) || NEGATE.has(et)) boost = Math.max(boost, 120);
  }
  // 「commit / コミット」の話題 → commit 系を押し上げる。
  if ((lr.includes("commit") || lr.includes("コミット")) && (et.includes("commit") || et.includes("コミット")))
    boost = Math.max(boost, 100);
  // 「続ける/進める/proceed/continue」の話題 → 続行系を押し上げる。
  if (/続け|進め|proceed|continue/.test(lr) && /続け|進め|proceed|continue|ok/.test(et)) boost = Math.max(boost, 80);
  return boost;
}
// i18n-exempt-end

export type RankArgs = {
  draft: string; // 現在のコンポーサー入力（前方一致フィルタに使う）
  lastReply: string; // 直近エージェント回答の最終テキスト（B-1）
  locale: string; // "ja" | "en"（シード言語の選択）
  hidden?: string[]; // ユーザーが×で消したキー（settings.quickRepliesHidden）
  limit?: number; // 返す候補数（既定 6）
};

// 候補を算出して並べて返す（表示テキストの配列）。
export function rankQuickReplies(map: QuickReplyMap, args: RankArgs): string[] {
  const { draft, lastReply, locale, hidden, limit = 6 } = args;
  const seeds = SEEDS[locale] ?? SEEDS.ja;
  const hide = new Set(hidden ?? []);
  // 学習エントリ + 未学習シードを統合（キー重複はシードを捨てる）。閾値を下げたとき過去に
  // 学習済みの長いエントリを遡って隠すため、ここでも MAX_LEN 超は取り込まない。消された
  // キーはシード側でも復活させない（隠しは学習の有無に関わらず効く）。
  const byKey = new Map<string, { text: string; count: number; at: number }>();
  for (const e of Object.values(map)) {
    if (normalize(e.text).length > MAX_LEN) continue;
    if (hide.has(keyOf(e.text))) continue;
    byKey.set(keyOf(e.text), { ...e });
  }
  for (const s of seeds) {
    const k = keyOf(s);
    if (hide.has(k)) continue;
    if (!byKey.has(k)) byKey.set(k, { text: normalize(s), count: 0, at: 0 });
  }

  const draftNorm = normalize(draft).toLowerCase();
  const scored = [...byKey.values()]
    // draft 入力中は前方一致で絞り、draft そのものと一致する候補は除く（無意味なので）。
    .filter((e) => {
      const et = e.text.toLowerCase();
      if (draftNorm && !et.startsWith(draftNorm)) return false;
      if (draftNorm && et === draftNorm) return false;
      return true;
    })
    .map((e) => ({ text: e.text, score: e.count + contextBoost(e.text, lastReply), at: e.at }))
    // スコア降順 → 同点は最近度 → なお同点なら短い方を先に（チップは短いほど押しやすく、
    // 並びが Object のキー順という説明できない順序に落ちるのも防ぐ）。
    .sort((a, b) => b.score - a.score || b.at - a.at || a.text.length - b.text.length);

  return scored.slice(0, limit).map((e) => e.text);
}
