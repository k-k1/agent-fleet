// 「ここから分岐」（docs/55）の判定 — ミラーのどのブロックに導線を出すか、そこで分岐したら
// 何往復が引き継がれるか。MirrorView から切り出してあるのは、この 2 つが**出しすぎると
// 必ず 400 になり、数え違えると確認ダイアログが嘘をつく**種類のロジックだから。

// 判定に要る形だけを構造的に受ける（MirrorView の Group は非公開で、ここは表示に依存しない）。
export interface BranchableTurn {
  role: string;
  anchorId?: string;
  compact?: boolean;
  pending?: boolean;
  queued?: boolean;
}

// canBranchInSession: このセッションで分岐という操作を出してよいか（ブロック単位の判定は
// canBranchFrom）。**kind ごとに条件が違う**のがここの肝で、opencode/codex は分岐点を渡せる
// 口が runtime API にしかないので managed 必須、claude は managed driver 自体が無く自分の
// 転写を切るので TUI で使える。一律に managed を要求すると claude の導線が永久に出ない。
export function canBranchInSession(
  caps: { forkAt: boolean; forkAtManagedOnly: boolean },
  opts: { managed: boolean; readOnly: boolean },
): boolean {
  if (!caps.forkAt || opts.readOnly) return false;
  return !caps.forkAtManagedOnly || opts.managed;
}

// canBranchFrom: このブロックから分岐できるか。
// - ユーザーの発言であること（エージェントの回答から分岐しても意味が変わる。v1 は打ち直し用）
// - アンカーがあること（無いブロックを指すと会話まるごと分岐に化ける）
// - まだ会話に載っていない echo / キュー済みでないこと（アンカーは landed な行にしか無い）
// - 圧縮サマリでないこと（会話の内容ではなく、その要約という別物）
export function canBranchFrom(t: BranchableTurn): boolean {
  return t.role === "user" && !!t.anchorId && !t.pending && !t.queued && !t.compact;
}

// carriedUserTurns: 分岐先へ引き継がれる「あなたの発言」の数。分岐点そのものは
// 引き継がれない（打ち直せるように手前で切る）ので数に入れない。確認ダイアログの
// 「N 件を引き継ぎます」がこれ。
export function carriedUserTurns(groups: BranchableTurn[], at: BranchableTurn): number {
  const i = groups.indexOf(at);
  if (i < 0) return 0;
  return groups.slice(0, i).filter((g) => g.role === "user" && !g.compact).length;
}
