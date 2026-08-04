// ミラーのターンに出す時刻の規則。ここを分けているのは、フッターの日時が「回答が
// 着地した時刻」ではなく「ターンが始まった時刻」になっていた不具合を、描画を起こさず
// 押さえられるようにするため。
//
// 転写の1行と1ターンの関係はエージェントで違う:
//   - claude / codex … 1ターン = 複数行（thinking・ツール呼び出し毎・最終テキスト）。
//     各行が自分の ts を持つので、畳んだ最後の行の ts がターンの終わり。
//   - opencode / copilot … 1ターン = 1行（span）。その ts は開始でしかないので、
//     Agent 側が endTs（opencode の time.completed / copilot の turn_end）を添える。
//   - cursor / kiro / agy … assistant 側に時刻の素材が無い（無表示のまま）。
// どちらの形でも「行の終わり = endTs があればそれ、無ければ ts」に畳める。

export interface TurnTimeLike {
  ts?: string;
  endTs?: string;
}

// endOf は転写1行の終了時刻。span を1行で表すエージェントは endTs を持ち、多行に
// 分かれるエージェントは持たない（その行自身の ts が終わり）。
export function endOf(t: TurnTimeLike): string {
  return t.endTs || t.ts || "";
}

// carryEnd は行をブロックへ畳む時に終了時刻を前へ進める。ブロックの ts（＝先頭行）は
// 触らない — 引き継ぎカードの時系列挿入（chronoInsertIndex）が開始側を見るため。
export function carryEnd(block: TurnTimeLike, row: TurnTimeLike): void {
  const end = endOf(row);
  if (end) block.endTs = end;
}

// footTime はブロックのフッターに出す時刻。終わりが分かるならそれ、分からなければ
// 開始にフォールバックする（進行中の opencode ターンなど）。
export function footTime(block: TurnTimeLike): string {
  return block.endTs || block.ts || "";
}
