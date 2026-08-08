# 0039. 会話の分岐点は kind 固有の不透明アンカーで指し、claude だけ jsonl 手術を許す

- 状態: 採用・P1〜P2 実装済み（契約＋opencode＋codex＋Console 導線。claude は後続）
- 関連: [55-fork-at-message.md](../55-fork-at-message.md) /
  [history/fork-from-chat.md](../history/fork-from-chat.md)（本 ADR が差し替える旧判断） /
  [27-agent-managed-driver.md](../27-agent-managed-driver.md) /
  [0029-usage-accounting.md](0029-usage-accounting.md)（出自 `handoff` の扱い）

## 背景

ミラーの過去のユーザー発言を選んで、そこまでの文脈を持った新セッションを起こしたい。

既存の `POST /sessions/{name}/fork` は会話まるごとの分岐しかできず、しかも Console からは
呼ばれていない（ミラーの分岐ボタンは引き継ぎモーダルへ統合され、そちらは *LLM による要約*
という別物）。地点分岐は 2026-06 に「Claude Code 非サポート／`idx` しかアンカーが無い／
jsonl 改変は壊れやすい」を理由に一度却下されている。

2026-08 の実測でこの前提が変わった。codex は app-server の `thread/fork` に `lastTurnId`
（inclusive）を、opencode は `POST /session/{id}/fork` に `messageID`（exclusive）を**公式に**
持つ。claude は `--resume-session-at <message id>` という隠しフラグを持つが **print モード
限定**で、TUI 起動しか無い AF では使えない。一方、claude 自身の fork が書く jsonl と元
ファイルの差分は **`sessionId` フィールドだけ**（`uuid` / `parentUuid` は元のまま）であることを
実測した。詳細と再現手順は docs/55 §55.2 / §55.11。

## 決定

1. **分岐点は kind 固有の不透明 ID（アンカー）で指す。** `transcript.Turn` に `AnchorID` を足し、
   claude = メッセージ uuid、codex = turn id、opencode = message id を入れる。転写の行番号
   `Idx` は compaction で動くため恒久アンカーにしない。
2. **Console はアンカーを解釈しない。** 受け取った文字列をそのまま fork API へ返すだけとし、
   包含（inclusive / exclusive）の差異吸収を含む kind 別の知識は Agent 側に閉じる。
3. **アンカーが空の turn からは分岐させない。** `Idx` からの推測で代用しない。分岐 affordance を
   出さないことを正とする。
4. **v1 の意味論は「選んだユーザー発言の直前まで」の 1 つに固定する。** 分岐先はその発言を
   打つ直前の状態で開き、元の発言文はコンポーザーの下書きに入れる（送信はしない）。
   「その発言と回答を含める」は v1.1 のオプションとし、アンカーを 1 つ後ろへずらして表現する。
5. **fork API は任意ボディ `{at}` へ広げる。** `at` 省略は従来の会話まるごと分岐で、後方互換を
   保つ。解決できないアンカーは **4xx で失敗させる**（会話まるごと分岐へ倒さない）。
   *実装時の補正*: エラーコードは意味で 2 つに割った — `fork_at_unsupported`（この種別／起動
   方式に地点分岐という機能が無い＝導線を出すべきでなかった）と `fork_bad_anchor`（機能は
   あるがこの分岐点が使えない）。前者はローカライズしたい定型文、後者は状態の問題で、
   ユーザーの次の行動（諦める／読み込み直す）が違う。分岐点の元発言テキストを返す `draft`
   フラグは落とした（Console が既に描画に使っており、サーバから返す意味がない）。
6. **codex / opencode は公式パラメータだけで実装する。** 非公式な rollout / ストア操作は行わない。
7. **claude は jsonl 手術を許す。** 元 jsonl をコピーし、切断点より前の行を取り、`sessionId`
   だけを書き換えて新 sid のファイルとして置く。**それ以外のフィールドには触らない。**
   `buildProgram` は自分の jsonl があれば resume するので、起動側の改修は不要。
8. **切断点は「本物のユーザープロンプト行」に限り、検査に落ちたら失敗させる。** ツール結果も
   `type:"user"` で載るため候補から除外し、切断後に `tool_result` を欠く `tool_use` が残らない
   ことを確認する。落ちたときは会話まるごと分岐を提案し、**黙って全体を分岐させない**。
9. **claude の手術は CLI ピン更新のドリフト検知に載せる。** 「切り詰めた jsonl が resume でき、
   切り詰め後の履歴だけを見ている」ことを実 CLI テストで毎版確認する（`cli-version-pin-e2e` の層）。
10. **v1 の対象は claude / codex / opencode の 3 種。** cursor / copilot は手術が効く見込みが
    あるが未検証、kiro は CLI 側が ID を採番、agy は SQLite ストアのため対象外とし、
    `Caps.CanForkAt` は false に保つ。
11. **分岐で生えたセッションの出自は既存 fork と同じ `handoff` を継ぐ。** 「人が開いた数」に
    混ぜない（ADR 0029 §6）。
12. **分岐と引き継ぎは別機能として併存させる。** 引き継ぎ＝要約して別エージェントへ渡す、
    分岐＝同じエージェントで文脈をそのまま複製する。UI でこの差を明示する。

## 採らなかった案

### claude も公式フラグ（`-p --resume-session-at`）で材料化する

分岐時に「最初の指示」を必須入力にすれば、`claude -p --resume <src> --resume-session-at <uuid>
--fork-session --session-id <new> "<指示>"` で公式に切り詰めた jsonl を作れる（実測で動作）。
非公式な書き込みを完全に避けられるのが利点。

採らない理由は、**その 1 ターンが headless で丸ごと走る**こと。ツールが動き、数分かかり、
失敗しうる。「分岐する」という操作の結果としてユーザーが見るのは、まだ何も指示していない
新しいセッションであるべきで、裏で 1 ターン完走してから開くのは別の機能になる。加えて
`--resume-session-at` 自体が `--help` に出ない隠しフラグで、「公式だから安全」の度合いは
手術との差ほど大きくない。**手術は行の取捨と `sessionId` 書き換えだけ**で、公式 fork の
出力と同形であることを実測で確認できている。

ただしこの案は捨てず、docs/55 §55.5 に代替ルートとして残す。ドリフト検知が壊れたときの
逃げ道になる。

### 転写の行番号 `idx` をアンカーにする

Console まで既に届いており追加実装がほぼ要らないが、compaction で動く。分岐という
「1 回きりだが取り返しのつく操作」で、ずれた地点から分岐しても**ユーザーは気づけない**
（それらしい履歴が付いてくる）。静かに間違うアンカーは採らない。

### 同一セッション内の巻き戻し（opencode の `revert`、claude の `/rewind`）で代替する

元の会話が失われる、または元セッションの状態を変えてしまう。「元は残したまま別方向を試す」
という要求そのものを満たさない。巻き戻しは別の機能であり、本 ADR の範囲外。

### 引き継ぎ（handoff）の要約で代替する

既に実装済みで追加コストがない。しかし要約は文脈を落とし、元の指示の言い回しも失う。
「あの指示のところからやり直す」用途では、失われた部分こそが分岐したい理由であることが多い。

### kind ごとに Console 側で包含を吸収する

codex が inclusive、opencode が exclusive、claude が「直前の行の uuid」と三者三様なので、
Console に持たせると kind が増えるたびにフロントが壊れる。Agent 側の `ResolveForkAt` に
閉じ込める（`agents.Agent` の分割方針どおり）。

### `agents.Forker` を拡張して地点分岐を持たせる

既存 `Forker` にメソッドを足すと、会話まるごと分岐だけを実装している kind のコンパイルが
壊れる。`ForkAtResolver` を別インターフェイスにして、実装の有無を `Caps.CanForkAt` で表す。
