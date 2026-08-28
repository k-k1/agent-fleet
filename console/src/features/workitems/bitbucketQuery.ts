// Bitbucket の保存クエリを「意図 × 対象」から組み立てる（docs/80 §80.22）。
//
// ★ ここだけ「クエリ欄がそのままフィルタ UI」（§80.16-1）から外れる。理由は Bitbucket の
// クエリだけが **af の発明を含む**からである:
//   ① 先頭の `<workspace>[/<repo>]` は Bitbucket の文法ではなく、横断検索が無い API に
//      「どこを見るか」を渡すために af が決めた約束（§80.19.1）。
//   ② レビュー待ちの式は `reviewers.uuid="{b8ceb65c-…}"` を要求する。`@me` の展開を足しては
//      あるが、**その形を知らないと `@me` に行き着けない**。
// GitHub の検索構文と JQL は本物の方言（利用者が普段書いている物）なので素通しのまま。
//
// 実際に既定値の `workspace/repo reviewers.uuid="@me"` がそのまま保存され、
// 「bitbucket has no workspace/repo visible to this connection」で返ってきた。★ 置き換えるべき
// 語を既定値に置けばエラーが自分でそれを言う、という設計は**実機で成立しなかった**。

export type BbIntent = "reviewing" | "repo_open" | "authored";

/** 出せるのはこの 3 つ。Bitbucket の API がこれ以上を持っていない（§80.19.1）。 */
export const BB_INTENTS: BbIntent[] = ["reviewing", "repo_open", "authored"];

/** その意図がリポジトリまで要るか。authored だけワークスペース単位で引ける
 *  （`/2.0/workspaces/{ws}/pullrequests/{user}` が「その人が作った PR」を返す唯一の経路）。 */
export function bbNeedsRepo(intent: BbIntent): boolean {
  return intent !== "authored";
}

export function bbWorkspaceOf(fullName: string): string {
  return (fullName.split("/")[0] || "").trim();
}

/** `workspace/repo` の羅列から、重複無しのワークスペース一覧。 */
export function bbWorkspaces(fullNames: string[]): string[] {
  const seen = new Set<string>();
  for (const fn of fullNames) {
    const w = bbWorkspaceOf(fn);
    if (w) seen.add(w);
  }
  return [...seen].sort();
}

/** 意図＋対象 → 保存されるクエリ。組み立てられないときは "" を返す（＝保存させない）。 */
export function bbQuery(intent: BbIntent, target: string): string {
  const t = target.trim();
  if (!t) return "";
  if (intent === "authored") return bbWorkspaceOf(t);
  // リポジトリが要る意図にワークスペースだけを渡させない（Bitbucket が答えられない）。
  if (!t.includes("/")) return "";
  return intent === "reviewing" ? `${t} reviewers.uuid="@me"` : t;
}

/** `/connections/git/bitbucket.org/repos` の応答から full_name だけを取り出す境界。
 *
 * ★ 形が違う・`error` が入っている・null が来た、を全部「候補なし」に潰す。呼び手は
 * 候補が無ければ手書きに落ちるので、ここで例外を投げる利得が無い（§80.17.5 の教訓 ——
 * 生成側だけ直しても古い相手からは古い形が来る）。 */
export function bbRepoNames(d: unknown): string[] {
  const list = (d as { repos?: unknown } | null)?.repos;
  if (!Array.isArray(list)) return [];
  const out: string[] = [];
  for (const r of list) {
    const fn = (r as { full_name?: unknown } | null)?.full_name;
    if (typeof fn === "string" && fn.includes("/")) out.push(fn.trim());
  }
  return out.sort();
}
