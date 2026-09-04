// ファイルビュアーの表示位置を「ペイン × ファイル × 面」ごとに覚え、戻ってきた
// ときに同じ位置へ返す。
//
// なぜ要るか: タブ表示は 1 セルに選ばれた 1 枚しか描かない（PaneHost の
// selectedView）。別のタブへ移ると FileView ごと unmount され、戻ってくれば
// api/fs/file から取り直して組み直す＝先頭からになる。読んでいた位置は React の
// 中には残らないので、コンポーネントの外に置くしかない。編集⇄表示の切り替えも
// 同じで、あちらは `hidden`（display:none）で面の箱そのものが消えるため、戻すと
// ブラウザが scrollTop を 0 に落とす。
//
// px（scrollTop）をそのまま覚える。ミラー（features/mirror/scrollMark.ts）が
// ターンをアンカーにするのは、戻ってきたときに載っている転写が別物（tail の
// 取り直し）だからで、ファイルは同じ中身が同じ順で戻ってくるので px で足りる。
// ただし**高さが決まるのは遅れる**（Markdown プレビューは innerHTML が passive
// effect、PDF はページ寸法を取ってから）ので、1 回書き戻すだけでは済まない ——
// 復元の粘りは parts/useScrollMemory.ts が持つ。
//
// ここはストアだけ。React も DOM も触らないので node プロジェクトで回せる。

/** 覚えておく面の数。1 ペイン 1 ファイルにつき数個（コード/プレビュー/…）なので、
 *  これで数十ファイルぶん。溢れたら古い順に捨てる（Map は挿入順）。 */
const MAX_ENTRIES = 200;

/** キー → 最後に見ていた scrollTop。タブが生きている間だけの記憶で、リロードで
 *  消える＝次回は先頭から（echoStore / scrollMark と同じ、モジュールスコープ）。 */
const positions = new Map<string, number>();

/** 面ごとのキー。ペインが違えば別の記憶（同じファイルを 2 枚開いて別々の場所を
 *  読める）、ファイルが違えば別の記憶。surface は「コード / プレビュー / PDF …」。 */
export function scrollMemoryKey(paneId: string | undefined, filePath: string): string | null {
  if (!filePath) return null;
  // 区切りはパスに絶対に現れない NUL（生で書くと「バイナリ」扱いになって
  // grep から消えるので、必ずエスケープで書く: src/test/noRawControlChars.test.ts）。
  return `${paneId || "-"}\u0000${filePath}`;
}

export function saveScrollPos(key: string, top: number): void {
  if (!key) return;
  positions.delete(key); // 入れ直して「最近使った」順に並べ替える
  positions.set(key, top);
  if (positions.size > MAX_ENTRIES) {
    const oldest = positions.keys().next();
    if (!oldest.done) positions.delete(oldest.value);
  }
}

export function loadScrollPos(key: string): number | null {
  if (!key) return null;
  const top = positions.get(key);
  return top === undefined ? null : top;
}

/** テスト用。 */
export function clearScrollPos(): void {
  positions.clear();
}
