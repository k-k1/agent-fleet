// セッション表示名の規則。正は Agent の cleanTitle（workspace/agent/session.go の
// sessionTitleMaxRunes）で、ここはその写し。
//
// なぜ 1 か所に集めるのか: 上限が層ごとに違うと「保存も編集もできたのに、起動の瞬間だけ
// 落ちる」になる。引き継ぎ提案（docs/77）は 512 バイトまで保存でき、カードにも起動
// ダイアログにも出て編集もできたのに、POST /sessions だけが 80 文字で弾き、利用者には
// 「worktree 起動に失敗: title is too long」としか見えなかった（実障害）。
export const SESSION_TITLE_MAX = 80;

/** 制御文字を落とし、80 文字（コードポイント）に詰める。
 *
 *  入力欄の `maxLength` は**打鍵にしか効かない** — 引き継ぎ提案や作業項目から
 *  `value` に流し込まれた長い文字列は素通りするので、seed を受け取る側でこれを通す。
 *  Go 側は rune 数で数えるので、UTF-16 の length ではなく Array.from（コードポイント）
 *  で数える。 */
export function clampSessionTitle(s: string): string {
  // eslint-disable-next-line no-control-regex -- Agent 側 cleanTitle と同じ範囲を落とす
  const flat = s.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
  const cps = Array.from(flat);
  return cps.length > SESSION_TITLE_MAX ? cps.slice(0, SESSION_TITLE_MAX).join("").trimEnd() : flat;
}
