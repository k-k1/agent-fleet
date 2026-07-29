// スキルピッカーの純ロジック（docs/50 / ADR0034）。DOM もストアも触らない —
// MirrorView がトリガ判定・絞り込み・差し込みをここへ委譲する。
// トリガ文字は kind 依存（claude/opencode/cursor は "/"、codex は "$" メンション —
// registry の skillTrigger）。起動は「入力の先頭」でだけ成立するとみなし、トリガも
// 先頭の 1 トークン内にキャレットがある間だけ生きる（空白を打って引数に入ったら閉じる）。

import type { SessionSkill } from "../../core/api/client.ts";

export interface SlashToken {
  token: string; // トリガ文字を除いた入力中の断片（"" = トリガ直後）
  start: number; // 常に 0（先頭トリガ）— 差し込み置換の左端
  end: number; // 置換の右端（最初の空白 or 文末）
  bare?: boolean; // トリガ文字なしの先頭トークン（ボタン起点の絞り込み。pickerTokenAt 参照）
}

// 全角エイリアス: 日本語 IME では「/」「$」が全角（／・＄）で入るため、トリガとして
// 等価に受ける。確定時は invoke（半角の正しい起動形）で丸ごと置換されるので、全角の
// まま送信される事故も起きない。
const TRIGGER_ALIASES: Record<string, string[]> = { "/": ["/", "／"], $: ["$", "＄"] };

// triggerHead: text の先頭がトリガ（または全角エイリアス）なら、その一致文字列を返す。
function triggerHead(text: string, trigger: string): string | null {
  if (!trigger) return null;
  for (const a of TRIGGER_ALIASES[trigger] ?? [trigger]) {
    if (text.startsWith(a)) return a;
  }
  return null;
}

// hasTriggerHead: MirrorView の「draft と token のずれ」ガードが使う公開判定。
export function hasTriggerHead(text: string, trigger: string): boolean {
  return triggerHead(text, trigger) !== null;
}

// slashTokenAt: draft とキャレット位置から「補完対象のトークン」を返す。
// 対象外（先頭がトリガでない・キャレットがトークン外）は null。
export function slashTokenAt(text: string, caret: number, trigger = "/"): SlashToken | null {
  const head = triggerHead(text, trigger);
  if (!head) return null;
  const ws = text.search(/[\s]/); // 最初の空白（改行含む）でトークン終了
  const end = ws < 0 ? text.length : ws;
  if (caret < head.length || caret > end) return null;
  return { token: text.slice(head.length, end), start: 0, end };
}

// pickerTokenAt: ピッカーが実際に絞り込みへ使うトークン。allowBare（＝「/」ボタン起点で
// 開いている間）は、トリガ文字が無くても先頭 1 トークンをクエリとして受ける — ボタンで
// 開いてからそのままタイプして絞り込めるように。確定時は applySkillToDraft が同じ規則で
// そのトークンごと invoke に置換するので、クエリが引数として残る事故は起きない。
// 先頭の 2 語目以降にキャレットがある（＝引数を書いている）ときは null＝全件のまま。
export function pickerTokenAt(text: string, caret: number, trigger = "/", allowBare = false): SlashToken | null {
  const tok = slashTokenAt(text, caret, trigger);
  if (tok || !allowBare) return tok;
  if (triggerHead(text, trigger)) return null; // トリガ付きの判断は slashTokenAt に従う
  const ws = text.search(/[\s]/);
  const end = ws < 0 ? text.length : ws;
  if (caret > end) return null;
  return { token: text.slice(0, end), start: 0, end, bare: true };
}

// filterSkills: 前方一致 > 名前部分一致 > 説明部分一致の順で並べる。大文字小文字は
// 無視。空クエリは全件（API の並び＝name 昇順のまま）。
export function filterSkills(skills: SessionSkill[], query: string): SessionSkill[] {
  const q = query.trim().toLowerCase();
  if (!q) return skills;
  const rank = (s: SessionSkill): number => {
    const nm = s.name.toLowerCase();
    if (nm.startsWith(q)) return 0;
    if (nm.includes(q)) return 1;
    if ((s.description || "").toLowerCase().includes(q)) return 2;
    return -1;
  };
  return skills
    .map((s) => ({ s, r: rank(s) }))
    .filter((x) => x.r >= 0)
    .sort((a, b) => a.r - b.r)
    .map((x) => x.s);
}

// originKind: foreign スキルの出所規約 dir → 表示上の kind。".agents" はエージェント
// 横断の共有規約でどの kind にも属さない → null（中立の「共有」バッジになる）。
export function originKind(origin: string | undefined): "claude" | "codex" | null {
  if (origin === ".claude") return "claude";
  if (origin === ".codex") return "codex";
  return null;
}

// applySkillToDraft: 選択したスキルの起動文字列（invoke — 末尾空白込み。"/name " や
// "$name "）を draft へ差し込み、新しい draft とキャレット位置を返す。起動は先頭で
// だけ意味を持つので、常に「invoke ＋既存の本文（引数として残す）」に組み立てる。
// 入力中のトークンは置換して消える（allowBare のときはトリガ無しの先頭トークンも
// 「絞り込みに使った文字」なので同じく置換される — pickerTokenAt と対で読むこと）。
export function applySkillToDraft(
  draft: string,
  caret: number,
  invoke: string,
  trigger = "/",
  allowBare = false,
): { next: string; caret: number } {
  const tok = pickerTokenAt(draft, caret, trigger, allowBare);
  // トークンが生きていればその右側（既に書いた引数）を、そうでなければ（ボタン
  // 起点）トリガで始まらない下書き全体を引数位置へ残す。
  const tail = tok
    ? draft.slice(tok.end).trimStart()
    : hasTriggerHead(draft, trigger)
      ? ""
      : draft.trimStart();
  return { next: invoke + tail, caret: invoke.length };
}
