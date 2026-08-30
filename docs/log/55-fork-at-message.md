# 55. 発言時点からの会話分岐（fork at message）

> ◐ P1〜P5 実装済み（契約 ＋ **4 kind**（claude/codex/opencode/copilot）＋ Console 導線 ＋「続きから」）。**4 種とも
> サーバ側は実 CLI で通し確認済み**、Console からの通しはデプロイ待ち。cursor / kiro / agy は対象外（§55.5）。
> 設計判断は [decisions/0039](../decisions/0039-fork-at-message.ja.md)。
> 旧判断（会話まるごと分岐のみ・地点分岐は非サポートにつき却下）は
> [history/fork-from-chat.md](fork-from-chat.md)。本書はそれを差し替える。

ミラーに並ぶ**過去のユーザー発言を 1 つ選び、そこまでの文脈を引き継いだ新しいセッションを
起こす**。元のセッションは無傷のまま残り、分岐先は独立して進む。「あの指示のところで別の
やり方を試したい」「途中で方針を間違えたので、そこからやり直したい」を、会話をコピペし
直さずに実行するための機能。

## 55.1 いまある fork と、何が足りないか

`POST /sessions/{name}/fork`（`workspace/agent/session_handlers.go` の `handleForkSession`）は
既にあるが、**会話まるごと**の分岐しかできない。各 CLI のネイティブ fork
（`claude --fork-session` / `codex fork` / `opencode --session … --fork`）を
`session.Meta.ForkFrom` 経由で初回起動時に叩く構造で、分岐点を指定する口が無い。

さらに **Console からこの API は現在呼ばれていない**。ミラー見出しにあった「⑂ 分岐」ボタンは
引き継ぎモーダル（`console/src/features/sessions/HandoffModal.tsx`）へ統合された際に消えており、
そちらは *LLM がこれまでの会話を要約して新セッションへの指示文を作る* 別物である。要約は
文脈を落とすし、元の指示の言い回しも失われる。**バイト等価の分岐**は引き継ぎでは代替できない。

### 旧判断が変わった理由

`history/fork-from-chat.md` §21.1 は地点分岐を次の理由で却下していた。

| 当時の根拠 | 現在（2026-08 実測） |
| --- | --- |
| 任意メッセージからの分岐は Claude Code 非サポート | `--resume-session-at <message id>` が実在する（§55.2） |
| Console まで届く識別子は `idx`（行番号）だけ | 事実。だが uuid は転写に載っており、通していないだけ（§55.4） |
| `idx` は compaction で不安定 | 事実。だから `idx` ではなく **kind ごとの不透明 ID** を通す |
| jsonl 切り詰めは非サポートで壊れやすい | claude 公式 fork が書く差分は `sessionId` のみと実測（§55.2） |

加えて当時は claude 一択だった。いまは codex と opencode が**公式 API に分岐点パラメータを
持っている**（docs/27 の managed driver が既定経路）ので、少なくともこの 2 種は非公式な手段を
一切使わずに実現できる。

## 55.2 各 CLI の地点分岐能力（実測）

実測はこのコンテナの CLI。**claude 2.1.223 / codex-cli 0.146.1 / opencode 1.18.14**
（イメージのピンはそれぞれ 2.1.224 / 0.147.0 / 1.18.15 で、1 パッチ後ろ）。再現手順は §55.10。

| kind | 地点分岐の手段 | 公式性 | 既定の起動経路で使えるか |
| --- | --- | --- | --- |
| claude | ① `--resume-session-at <uuid>` ② jsonl 手術 | ① 隠しフラグ・**print モード限定** ② 非公式 | TUI 起動のみなので ① は不可。② を採る |
| codex | `thread/fork` の `lastTurnId` | **公式**（app-server スキーマに明記） | ✅ managed が既定 |
| opencode | `POST /session/{id}/fork` の `messageID` | **公式**（OpenAPI に定義） | ✅ managed が既定 |
| cursor | 無し（`/fork` は TUI 内コマンド） | — | **不可**（転写に ID が無い・§55.5） |
| copilot | 無し | — | ✅ **手術で実装済み**（events.jsonl が復元元・§55.5） |
| kiro | 無し。ID を CLI 側が採番 | — | 現実的でない |
| agy | 無し。SQLite ストア | — | 対象外 |

### claude — 隠しフラグは print モード限定

バイナリ（`@anthropic-ai/claude-code/bin/claude.exe`）に `--resume-session-at <message id>` と
`--resume-drops-turn <message id>` が入っている。`--help` には出ない。後者のヘルプ文は

> With `--resume-session-at` in print mode: declare the prompt uuid of the turn the truncating
> resume intends to discard; the resume is refused if the discarded range contains anything not
> attributable to that turn (absorbed queued messages, task notifications, content from other
> turns). **Ignored outside print mode, like `--resume-session-at`.**

**実測 1（print モードでは効く）**: ALPHA → BETA と codeword を上書きした 2 ターンの会話で、
1 ターン目末尾メッセージの uuid を指定して

```
claude -p --resume <src> --resume-session-at <uuid> --fork-session --session-id <new> "…"
```

を実行すると、`<new>.jsonl` には 1 ターン目＋新ターンだけが入り、モデルは ALPHA と答えた。
元 jsonl は無傷。

**実測 2（TUI では無視される）**: 同じフラグを付けて tmux で TUI 起動すると、ヘルプ文どおり
**全履歴が復元され BETA と答えた**。AF の claude は tmux TUI 起動で managed driver を持たない
（`workspace/agent/internal/agents/claude/` に `driver.go` は無い）ので、この経路は使えない。

**実測 3（空プロンプト不可）**: 「切り詰めた jsonl を作るだけ」を狙って空プロンプトを渡すと
`Provide a prompt to continue the conversation` で終了し、fork 先ファイルは作られない。
公式ルートを使うなら**必ず 1 ターン走る**。

**実測 4（jsonl 手術は動く／公式 fork との差分は `sessionId` だけ）**: 元 jsonl をコピーし、
選んだユーザー行の直前で切り、各行の `sessionId` を新 sid へ書き換えて `<new>.jsonl` として
置くと、`claude --resume <new>` は ALPHA と答えた。さらに **claude 自身が作った fork jsonl と
元ファイルを行単位で diff したところ、変化していたフィールドは `sessionId` のみ**で、
`uuid` / `parentUuid` / `cwd` / `version` は元のまま引き継がれていた。

この最後の事実には 2 つの意味がある。

1. 手術は「公式 fork がやっていること」の忠実な再現であって、独自スキーマを書き起こす行為では
   ない。リスクは「読む」から「同じ形で書く」への 1 段だけ増える。
2. **メッセージ uuid は fork をまたいで不変**。分岐先でさらに分岐しても同じ uuid を指せる＝
   恒久アンカーとして使える。

jsonl は `uuid` / `parentUuid` の DAG で、`type` は `user` / `assistant` のほかに
`attachment` / `queue-operation` / `last-prompt` / `mode` / `custom-title` / `agent-name` /
`permission-mode` / `file-history-snapshot` などのメタ行が混ざる（実測）。**ツール結果も
`type:"user"` 行として載る**ため、切断点の候補を素朴に `type=="user"` で拾ってはいけない（§55.5）。

### codex — `lastTurnId` が公式にある

`codex app-server generate-json-schema` が吐く `v2/ThreadForkParams.json` に定義がある。

> **lastTurnId**: Optional last turn id to fork through, **inclusive**. When specified, turns
> after `last_turn_id` are omitted from the fork. The referenced turn cannot be in progress.

turn id は rollout jsonl の `event_msg/task_started`・`turn_context`・`event_msg/task_complete`
に載っている（実測: `019fa887-a54e-7550-…`）。**CLI の `codex fork <id>` にはこの引数が無い**
ので、app-server 経由が条件。AF の codex は managed が既定（docs/27）で
`workspace/agent/internal/agents/codex/driver.go` の `threadFork` が既に `thread/fork` を
叩いているため、パラメータを 1 つ足すだけで届く。

### opencode — `messageID` は排他（exclusive）

OpenAPI（`GET /doc`、実測 1.18.14）の `POST /session/{sessionID}/fork` はボディに
`{messageID: "^msg…"}` を取る。実装（バンドル内 `Session.fork`）は

```js
for (let p of messages) {
  if (q.messageID && p.info.id >= q.messageID) break;
  …新セッションへコピー（メッセージ ID とパート ID を採番し直す）…
}
```

で、**指定した messageID そのものは含まれない**。ID は昇順採番なので `>=` の打ち切りが成立する。
compaction パートの `tail_start_id` も新 ID へ張り替えている。

この排他性は**実物でも確認済み**（1.18.15・§55.8 Tier B）。2 往復の会話で 2 番目のユーザー発言を
指すと、分岐先は 1 往復だけになり、指した発言は入らない。

同じ session 内で巻き戻す `POST /api/session/{id}/revert/stage`・`revert/commit`（`RevertState`
は `{messageID, partID, snapshot}`）も別に存在するが、本機能は**元を残す**ので使わない。

## 55.3 意味論（どこで切るか）

ユーザーが選ぶのは**自分の発言**であり、v1 の意味論は 1 つに固定する。

> **選んだユーザー発言の「直前」まで**を引き継ぐ。選んだ発言自体は分岐先に含まれない。

そのユーザー発言をやり直したいから分岐するのであって、同じ発言をもう一度読ませたいわけでは
ない。分岐先は「その発言を打つ直前の状態」で開き、コンポーザーには元の発言文を**下書きとして
入れておく**（送信はしない）。書き直すのも、そのまま送るのも 1 操作で済む。

この意味論は 4 種すべてに素直に落ちる。

| kind | 渡す値 | 包含 |
| --- | --- | --- |
| claude | 選んだユーザー行の**直前の行**の uuid（＝`--resume-session-at`）／手術では選んだ行の手前で切る | 直前まで |
| codex | 選んだ発言が属する turn の**1 つ前**の turn id（`lastTurnId` は inclusive） | 直前まで |
| opencode | 選んだ発言の `messageID` そのもの（exclusive） | 直前まで |
| copilot | 選んだ `user.message` イベントの `id`（手術では選んだ行の手前で切る） | 直前まで |

### もう 1 つのモード「この発言の続きから」（v1.1・✅ 実装済み）

「この発言と、それが得た回答まで引き継いで、その先を別方向へ」も要る。**同じ操作が 1 往復
ずれているだけ**なので、モーダルで選ばせて `{include: true}` を送る。既定は「やり直す」——
方針を間違えた直後がいちばん多い用途で、そこでは分岐点の発言も捨てたい。

各エンジンへの変換は resolver が吸収する（`agents.ForkPoint{Anchor, Include}`）。

| kind | やり直す（既定） | 続きから（include） |
| --- | --- | --- |
| claude | 選んだ行の手前で切る | **次のユーザープロンプト**の手前で切る |
| codex | 1 つ前の turn id | **その turn 自身**（`lastTurnId` が包含なので素直） |
| opencode | 選んだ `messageID` | **次のユーザー発言の `messageID`** |
| copilot | 選んだ `user.message` の `id` | **次の `user.message` の `id`** |

**最後のやり取りを「続きから」= 会話まるごと**。全部残すとはそういうことなので、resolver は
空文字（＝分岐点なし）を返し、既存の会話まるごと分岐の経路に落ちる。逆に**最初のやり取りを
「やり直す」は codex だけ表現できない**（`lastTurnId` を空にすると「まるごと」の意味になって
しまう）ので断る。この 2 つは対称ではなく、どちらもエンジン側の都合そのもの。

下書きの投入は「やり直す」のときだけ。「続きから」ではその発言が分岐先に残っているので、
入力欄にも同じ文が入ると二重に見える。

## 55.4 データ契約

### アンカー: `transcript.Turn.AnchorID`

`workspace/agent/internal/transcript/transcript.go` の `Turn` は今 `Idx`（転写の行番号）しか
持たない。`Idx` は compaction で動くので分岐点の恒久アンカーにできない。**kind 固有の不透明
文字列**を 1 本足す。

```go
// AnchorID is the agent's OWN stable identifier for this turn, opaque to the Console:
// claude = message uuid, codex = turn id, opencode = message id ("msg_…"). Empty when
// the kind has no such id (or the line predates it) — the Console then hides the
// "branch from here" affordance for that turn instead of guessing from Idx.
AnchorID string `json:"anchorId,omitempty"`
```

- **Console は中身を解釈しない。** そのまま fork API へ送り返すだけ。kind 別の分岐は Agent 側に
  閉じる（`agents.Agent` の責務分割と同じ形）。
- **空文字は「この turn からは分岐できない」を意味する。** `Idx` からの推測は禁止。
- 埋めるのは各 kind の `transcript.go`。claude は現状 `uuid` を読んでいないので追加が要る。

### セッション: `session.Meta.ForkAt`

```go
// ForkAt is the anchor INSIDE ForkFrom's conversation that this session was forked at:
// the fork carries the source's history up to (but not including) that point. Empty =
// whole-conversation fork (the pre-existing behaviour). Like ForkFrom it only affects
// the FIRST launch; later launches resume the fork's own conversation.
ForkAt string `json:"forkAt,omitempty"`
```

`ForkFrom` と同じく**初回起動でのみ効く**。再起動で再分岐しない不変条件は既存のまま。

### API

`POST /sessions/{name}/fork`（Agent）と `POST /api/sessions/{name}/fork`（CP）は現状ボディを
取らない。**任意ボディ**を受けるよう広げる。

```json
{ "at": "<anchorId>", "include": false }
```

- `at` 省略＝従来どおり会話まるごと分岐（後方互換）。ボディ無し（旧クライアント）も同じ。
- `include`（既定 false）＝分岐点の発言とその回答まで引き継ぐ（§55.3）。kind ごとの変換は
  `ForkAtResolver` が吸収するので、`Meta.ForkAt` の意味（この値の手前まで残す）は変わらない。
  ただし**壊れた JSON は 400**にする — 読めないボディを黙って捨てると、地点指定のつもりの
  要求が会話まるごと分岐に化ける。
- エラーは意味で 2 つに割る。`400 fork_at_unsupported` ＝この種別／起動方式には地点分岐という
  機能が無い（導線を出すべきでなかった）。`400 fork_bad_anchor` ＝機能はあるが、この分岐点が
  使えない（会話に無い・サブエージェント発言・ミラーが古い）。既存の `fork_unsupported_kind` /
  `fork_missing_dir` と同じ体系。
- 判定の順番も意味で決める。**機能の有無（`fork_at_unsupported`）は会話ストアを見る前**に答え、
  分岐点の解決（`fork_bad_anchor`）は「分岐できる会話がある」と分かったあとに行う。逆にすると、
  非対応の kind へ `at` を投げたとき「分岐できる会話がまだありません」のような無関係な理由が
  返り、導線の設計ミスが会話の状態の問題に見える。
- **下書きは API に載せない。** 分岐点の発言テキストは Console が既に描画に使っているので、
  サーバから返す意味がない（初版の設計では `draft` フラグを置いていたが実装時に落とした）。

`agents.Forker` は分岐点解決を持たないので、**別インターフェイスで足す**（実装しない kind の
コンパイルを壊さない）。

```go
// ForkAtResolver is the optional "fork at a point" capability. ResolveForkAt validates
// an anchor from this session's transcript and returns the value the kind's fork path
// needs (claude: cut uuid, codex: lastTurnId, opencode: messageID) — which is NOT always
// the anchor itself, since the engines disagree on inclusivity (docs/55 §55.3).
type ForkAtResolver interface {
    ResolveForkAt(m session.Meta, anchor string) (string, error)
}
```

`Caps` には `CanForkAt bool` を足し、Console 側（`console/src/agents/registry.ts` の
`AgentCaps`）にも同名の cap を足す。Console の `AgentCaps` には現在 fork 系の cap が
1 つも無い（UI ごと消えたため）ので新設になる。

## 55.5 kind 別の実装

### opencode（最初にやる — ✅ 実装済み）

`workspace/agent/internal/agents/opencode/driver.go` の `serveForkSession` は今 `{}` を
POST している。`{"messageID": …}` を送るだけ。`ResolveForkAt` は**アンカーをそのまま返す**
（exclusive なので変換不要）が、次の 2 つは弾く。

- 会話に存在しないメッセージ id。
- **子（サブエージェント）会話のメッセージ id。** 親の id 並びに属さないので、それで親を
  分岐すると無関係な地点で切れる。ミラーは sidechain を畳んでいるので導線からは選べないが、
  アンカーはクライアント由来なのでサーバ側でも確かめる。

CLI(TUI) ルートは `--session <src> --fork` に分岐点を渡す引数が無い。ハンドラが managed 以外を
断るが、`BuildLaunch` にも**同じ拒否を置いてある**（防御の二重化）— ここを素通りさせると
「地点を指したのに会話まるごと分岐」が起動時に静かに成立してしまう。

### codex（✅ 実装済み）

`driver.go` の `threadFork` に `lastTurnId` を追加。**アンカーをそのまま送らない唯一の kind**で、
`ResolveForkAt` が「選んだ発言の turn の 1 つ前の turn id」を返す（`lastTurnId` は inclusive）。

アンカーの出どころは rollout の `event_msg/task_started` の `turn_id`。これがその turn の
開始を示し、ユーザープロンプトの `response_item` より**前**に来る（実測）ので、転写を舐めながら
「いま開いている turn」を持ち回り、各ターンの `AnchorID` に入れる。`turn_context` も `turn_id` を
持つが、turn より頻繁に現れる（実測: 1 つの rollout で turn_context 19 / task_started 15）ため
補助にとどめる。最初の `task_started` より前（注入された指示文）はアンカー空＝分岐不可。

**最初のやり取りからは分岐できない。** `lastTurnId` を空にすると codex には「会話まるごと」を
意味し、意図と正反対になる。表現できないので送らずに断る（opencode は最初のメッセージ id を
渡せば空の分岐先になるので、ここだけ挙動が割れる）。

CLI 経路（`codex fork <id>`）には分岐点の引数が無いので、ハンドラの managed ゲートに加えて
`BuildLaunch` でも拒否する。

### claude（手術・✅ 実装済み）

TUI しか無いので、**Agent が `<新 sid>.jsonl` を直接書いてから** `claude --resume <新 sid>` で
起動する。既存の `buildProgram` は `SessionJSONLExists(sid)` が真なら resume を選ぶので、
**起動コマンド側の改修は不要**——fork ハンドラが起動前にファイルを置けばそのまま乗る。

書き方は実測 4 に従い、**行を選ぶ以外の加工をしない**。

1. 元 jsonl を読み、切断点（選んだユーザー行）より前の行だけを取る。
2. 各行の `sessionId` を新 sid に書き換える。**他のフィールドには触らない**
   （`uuid` / `parentUuid` を保つことがアンカーの安定性そのもの）。
3. `<config>/projects/<project>/<新 sid>.jsonl` へ書く。元ファイルは開かない・触らない。

切断点の妥当性検査（`--resume-drops-turn` が公式にやっていることの自前版）:

- 候補は `type:"user"` かつ **`message.content` がツール結果でない**行に限る。ツール結果も
  `type:"user"` で載るため、ここを誤ると `tool_use` と `tool_result` が分断され、次のターンで
  API に弾かれる。`isMeta` / `isSidechain` / compaction サマリの行も候補から外す。
- 切断後の末尾に、対応する `tool_result` を欠く `tool_use` が残っていないことを確認する。
  残るなら `fork_bad_anchor`。
- compaction サマリより手前を指した分岐は、サマリが消えるだけで整合するが、**要約前の生の
  履歴が既に無い**ことがある。その場合は素直に失敗させ、会話まるごと分岐へ誘導する。
- `file-history-snapshot` 行は分岐先へ引き継ぐ（公式 fork も落としていない）。分岐先での
  `/rewind` が元セッション由来のチェックポイントを指す点は既知の割り切り。

**縮退**: 検査に落ちたら会話まるごと分岐（既存経路）を提案する。黙って全体を分岐させない。
検査は `ResolveForkAt`（要求時）と `MaterializeForkAt`（初回起動時）が**同じ関数**
（`buildForkLines`）を通るので、「要求は通ったのに起動で失敗する」がそもそも起こらない。

**materialize の位置**: 分岐先の jsonl は claude の `BuildLaunch` が初回起動の直前に書く。
`buildProgram` は自分の jsonl があれば普通に `--resume` するので、そこから先は分岐だったことを
誰も知らなくてよい（会話まるごと分岐が初回起動後はただの resume になるのと同じ形）。書き込みは
同ディレクトリの一時ファイル＋rename で、途中まで書けた転写が resume されることを防ぐ。

**claude だけ managed を要求しない**。claude に managed driver は存在しないので、他の kind と
同じ「managed 必須」を当てると導線が永久に出ない。この差は kind 側（`ResolveForkAt` が
`agents.ErrForkAtRoute` を返すか）と Console 側（`caps.forkAtManagedOnly`）の両方に置いてある。

**代替ルート（採らないが残す）**: 分岐時に「最初の指示」を必須入力にすれば
`claude -p --resume <src> --resume-session-at <uuid> --fork-session --session-id <new> "<指示>"`
という公式フラグ経由で材料化できる（実測 1）。手術を避けられる代わりに、そのターンが
headless で丸ごと走る（ツール実行・長時間化）ため v1 では採らない。判断は ADR 0039。

### cursor — 不可（P4b の調査結果・2026-08-09 実測）

当初は「Claude Code 互換 JSONL だから claude と同型の手術が効くはず」と見ていたが、**実物を
見ると効かない**。転写（`<projects>/<cwdSlug>/agent-transcripts/<chatId>/<chatId>.jsonl`）の行は

```json
{"role":"user","message":{"content":[…]}}
{"type":"turn_ended","status":…}
```

で、**`uuid` も `parentUuid` も `sessionId` も無い**。`cursor/transcript.go` のパーサも
`role` / `type` / `message.content` しか読んでいない（＝「Claude Code 互換」は *形が似ている*
という意味で、識別子まで同じという意味ではなかった）。

したがって分岐点に使える恒久 ID が存在しない。行番号で代用するのは ADR 0039 決定 1 が
明示的に却下した道で、ずれても誰も気づけない。加えて cursor の会話の実体は非公開 SQLite
（`~/.cursor/chats/**/store.db` — 存在を確認）側にあり、AF が書ける転写 JSONL は表示用の
写しである可能性が高い。**その場合、切り詰めても resume は元の履歴を復元し、「ミラーでは
切れているのにエージェントは全部覚えている」という最悪の食い違いになる。**

→ cursor は対象外のままにする。上流が `/fork` を非対話で開けるか、転写に ID を載せるまで動かない。

### copilot — 手術で実装済み（2026-08-09）

`~/.copilot/session-state/<sid>/events.jsonl` は 1 行 1 イベントで、**全イベントが
`{id, parentId, timestamp, type, data}` を持つ**（実測）。`user.message` の `id` がそのまま
アンカーになり、claude と同型の「prefix を取って書き直す」手術が成立する形をしている。
AF 側が sid を採番する方式なのも claude と同じ。

**未知だった「events.jsonl と `session.db` のどちらから復元するか」は実験で決着した ——
events.jsonl が正**。実験（隔離した `COPILOT_HOME` で実施・実 `~/.copilot` は不使用）:

1. `copilot --session-id <S1> -p` で 2 ターン（ALPHA → BETA と codeword を上書き）。
2. `session-state/<S1>/` を `<S2>/` へ**ディレクトリごとコピー**（`session.db` は**無改変**の
   まま = 両ターンが入ったもの）。`events.jsonl` だけを 2 番目の `user.message` が属する
   `session.resume` ブロックの手前で切り、`events.jsonl` と `workspace.yaml` の中の旧 sid を
   新 sid へ置換。
3. `copilot --session-id <S2> -p "What is the codeword?"` → **ALPHA**。

`session.db` には BETA が残っているのに ALPHA と答えた。**切り詰めた events.jsonl が
文脈を決めている**＝ claude と同型の手術が成立する。分岐先の events.jsonl にも
ALPHA ターン → 新しい質問 → ALPHA だけが並び、BETA は現れない。

**索引を我々が書く必要は無い**。`$COPILOT_HOME/session-store.db` に `sessions(id, cwd, …)`
という索引があるが、**未登録の `session-state/<id>/` を resume しても copilot は普通に読み、
自分で索引へ登録する**（実測）。他プロダクトの SQLite スキーマを owns せずに済む。

> 一度「渡した id とは別のディレクトリが生えた」と記録したが、**誤読だった**（生えたと思った
> ディレクトリが分岐先そのものだった）。追試では未登録コピーを resume しても余分な
> ディレクトリは生まれず、索引に自動登録されただけだった。

実装は claude と同型で、単位だけが違う（claude=1 ファイル / copilot=ディレクトリ一式）。
**`session.db` はコピーして触らない** — 復元元が events.jsonl だと分かっている以上、意味を
知らないファイルを書き換え始めた瞬間に、この手術は「読めるものを同じ形で書き直す」から
「他プロダクトの内部状態を owns する」に変わる。書き換えるのは `events.jsonl` の切り詰めと、
`events.jsonl` / `workspace.yaml` に載っている session id の張り替えだけ。

TUI（`BuildLaunch`）と managed（driver の `Resume`）の**両方**で材料化する。managed 側を
忘れると、分岐スロットには sid が無いので `session/new` へ落ち、分岐先が空の会話として
開いてしまう（＝履歴を静かに失う）。

### kiro / agy

対象外。kiro は CLI が ID を採番するため事前採番ができず、agy は SQLite ストア。

## 55.6 Console

- ミラー（`console/src/features/mirror/MirrorView.tsx`）の**ユーザー発言ブロック**に
  「ここから分岐」を出す。出す条件は `caps.forkAt && managed && !readOnly`（セッション単位）
  × `canBranchFrom(turn)`（ブロック単位 — ユーザー発言・アンカーあり・echo/キュー/圧縮でない）。
  ターンフッターのコピーの隣に置く。**ホバーでだけ出す形にはしない**: 粗いポインタでは
  ホバーが無く、機能の存在自体に気づけないため、常時表示で色だけ落とす。
- 押すと確認モーダル。表示するのは「分岐点の発言（先頭数行）」「引き継がれる往復数」
  「元は残ること」。エージェント種別とモデルは元セッションを継ぐ（`handleForkSession` の
  現在の挙動どおり）。
- 成功したら新セッションを split ペインで開き、**コンポーザーに元発言を下書きとして入れる**
  （送信しない）。
- 文言は i18n（`console/src/lib/i18n/locales/{ja,en}.ts`）。裸和文 lint に掛かるので直書き禁止。
- キーボード体系（docs/29）に載せる。ミラーでターンにフォーカスがあるときの分岐アクション。

引き継ぎ（handoff）との併存は明示する。**引き継ぎ＝要約して別エージェントへ渡す／分岐＝
同じエージェントで文脈をそのまま複製する**。モーダルの説明文でこの差を 1 行書く。

## 55.7 出自と会計

分岐で生えたセッションの `Origin` は既存 fork と同じ `OriginHandoff`（ADR 0029 §6）を継ぐ。
「人が開いた数」に混ぜないためで、地点分岐でも理屈は同じ。`OriginConv` も親から継ぐ。

## 55.8 検証

- **単体**: 切断点判定（ツール結果行の除外・`tool_use` 孤児検出・compaction）、
  各 kind の `ResolveForkAt` の包含変換（codex の「1 つ前の turn」・opencode の素通し）。
  ✅ opencode 分は `internal/agents/opencode/fork_at_test.go`（素通し・未知/サブエージェント
  アンカーの拒否・`messageID` を載せる/載せない・400 の文言・CLI ルートの起動拒否）。
  ✅ codex 分は `internal/agents/codex/fork_at_test.go`（転写が turn id をアンカーに持つこと・
  **1 つ前の turn へ変換すること**・最初の turn の拒否・未知アンカーの拒否・`thread/fork` の
  params に `lastTurnId` を載せる/載せない・CLI ルートの起動拒否）。
  ✅ 「続きから」（include）は 3 kind とも単体で押さえてある — codex は turn 自身、opencode は
  次のユーザー発言（最後なら ""）、claude は `nextPromptUUID`（**ツール結果の user 行を
  掴まないこと**を含む）。
- **Agent HTTP**: `at` 有無での分岐、`fork_bad_anchor` の各条件、後方互換（`at` 省略）。
  ✅ `session_fork_at_test.go`（非対応 kind・CLI ルート・壊れたボディ・ボディ無しで
  新ゲートを踏まないこと）。
- **Console 単体**: ✅ `features/mirror/forkAt.test.ts`（導線を出す条件と「引き継ぐ発言数」の
  数え方）。この 2 つは**出しすぎれば必ず 400 になり、数え違えれば確認ダイアログが嘘をつく**
  ので、MirrorView から純関数に切り出してテストしている。
  ✅ `features/mirror/ForkAtModal.dom.test.tsx`（`at` 付きで叩く／成功時にセッション名を返す／
  **失敗時は閉じずに理由を出す**／name の無い 200 を成功と扱わない）。
- **実 CLI（Tier B・live）**: ✅ `TestContractLiveForkAtMessage`。実 `opencode serve`＋実モデルで
  2 往復して、2 番目のユーザー発言を指して分岐し、**分岐先が 1 往復だけ（ALPHA あり・BETA なし）**
  であることと、分岐点なしなら 2 往復まるごと来ることを確かめる。ここが本命で、モックが
  確かめられるのは「messageID を送ったこと」まで、**opencode がその messageID をどう解釈するか**
  は実物にしか聞けない。1 往復ずれても、ミラー上は「分岐できた」に見えてしまう。
  「続きから」も同じ実行の中で見る（1 番目を「続きから」＝ 2 番目を「やり直す」と同じ地点に
  なること、最後を「続きから」＝ `""` になること）。モデル呼び出しの追加は無い。
  既存の live tier と同じ `-run TestContractLive` 一括起動に自動で乗る
  （`.github/workflows/opencode-contract.yml`）。
- **実 CLI（copilot・`clicontract` タグ）**: ✅ `TestContractLiveCopilotForkAt`。実 copilot で 2 ターン
  会話を作り、転写のアンカー → `ResolveForkAt` → `MaterializeForkAt` → `--session-id <分岐先>`
  で resume して、**切り落とした発言を覚えていないこと**を確かめる。`session.db` は無改変で
  コピーされているので、これは「events.jsonl が復元元であり続けている」ことの検査そのもの。
  `copilot-contract.yml` に相乗り。`COPILOT_HOME` を隔離するので実 `~/.copilot` は不使用
  （**HOME は差し替えない** — 認証がそこから来るため）。実測 2026-08-09: PASS / 36.7s。
- **実 CLI（claude・`clicontract` タグ）**: ✅ `TestContractLiveClaudeForkAt`。実 claude で 2 ターン
  会話を作り、`MaterializeForkAt` で 2 番目の発言の手前を切って `--resume` し、**切り落とした
  発言を覚えていないこと**（ALPHA と答え BETA と答えない）を確かめる。**この一本が claude の
  唯一のドリフト検知**で、転写スキーマや resume の解釈が動けばここだけが赤くなる（合成テストは
  何が起きても緑のままになる）。`claude-tui-contract.yml` に相乗りさせた。コストは haiku 3 ターン、
  実行後は scratch 会話の転写を自分で消す。実測 2026-08-09（claude 2.1.223）: PASS / 14.1s。
- **実 CLI（codex driftlive）**: ✅ `TestLiveDriftCodexForkAtLastTurn`。実 app-server で 2 ターン
  回し、2 番目の発言を指して分岐して、**分岐先の turn が 1 つだけ**であることを `thread/read`
  （`includeTurns`）で数える。スキーマの "inclusive" は仕様書の言葉であって挙動ではないので、
  ここでしか確かめられない。分岐先の turn 数は追加のターン消費なしに読める。
  コストは実ターン 2 回（実測 2026-08-09: 合計 19,364 tokens ＝ fresh_in 158 / cached_read
  19,200 / out 6。ほぼ全部がキャッシュ読みで、正味の新規入力はごく僅か）。codex の live tier は
  ユーザーのサブスク枠を使い、`E2E_CODEX_LOCAL_AUTH=1` の手元実行では実 `~/.codex` に
  rollout が残る点に注意（ヘッダに既述）。
- **実 CLI 契約テスト（ドリフト検知）**: `cli-version-pin-e2e` の層に足す。
  - claude: **手で切り詰めた jsonl が resume でき、切り詰め後の履歴だけを見ている**こと。
    ここが claude 更新で壊れる唯一の場所なので、ピン更新のたびに回す。
  - codex: `thread/fork` に `lastTurnId` を渡した結果の履歴長。
  - opencode: `messageID` の exclusive 性（指定した発言が分岐先に無いこと）。
- **Console**: `MirrorView` の affordance 表示条件（`anchorId` 空で出ない）と下書き投入の dom テスト。

## 55.9 フェーズ

| Phase | 内容 | 状態 |
| --- | --- | --- |
| P1 | `Turn.AnchorID` / `Meta.ForkAt` / API `at` / `ForkAtResolver` / `CanForkAt` の骨組み＋ opencode 実装 | ✅ |
| P1.5 | Console の分岐導線（ユーザー発言の「ここから分岐」・確認モーダル・下書き投入） | ✅ |
| P2 | codex（`lastTurnId`＋アンカー変換） | ✅ |
| P3 | claude（jsonl 手術＋切断点検査＋縮退）＋ ドリフト検知テスト | ✅ |
| P4a | v1.1「この発言の続きから」（3 kind ＋ Console のモード選択） | ✅ |
| P4b | cursor / copilot の転写手術が効くかの調査 | ✅ 結論: **cursor=不可 / copilot=効く** |
| P5 | copilot 対応（アンカー＋手術＋TUI/managed 両経路） | ✅ |

**使える範囲は claude（TUI）／ managed の opencode・codex**。導線を出す条件は kind ごとに
違うので `canBranchInSession`（`caps.forkAt` × `caps.forkAtManagedOnly` × managed × readOnly）に
まとめてある。サーバ側も同じ判断を独立に持っている（導線だけの防御にしない）。

opencode を先にやるのは、**公式 API で最も改修が小さく、契約（アンカー・API・cap）の形を
実際に動かして確かめられる**ため。claude を最後に置くのは、唯一非公式な書き込みを伴い、
先に契約が固まっているほど手術範囲を小さく保てるため。

## 55.10 未検証（各 Phase に着手する前に潰す）

MVP（P1＋P1.5）のサーバ側は**実 CLI で通し確認済み**（§55.8 の Tier B — 実 serve・実モデルで
2 往復して分岐し、分岐先が分岐点の手前で終わることを確認。opencode 1.18.15）。残っているのは
**Console からの通し**で、これは Workspace イメージ再ビルド＋コンテナ recreate（Agent の Go 変更）
と Console バンドルのビルド＋CP 再起動が要るので、デプロイの都合に従う。


1. ~~**codex `thread/fork` は rollout をいつ書くか。**~~ **解決（実測）**: fork 直後、初回 turn を
   投げる前に rollout が存在する（`TestLiveDriftCodexForkAtLastTurn` が記録）。driver の
   「初回 turn 前のスレッドは rollout が無い」は `thread/start` の話で、fork には当てはまらない。
   したがって **TUI codex の地点分岐は原理的には可能**——`thread/fork` で分岐を作ってから
   `codex resume <新 thread>` で起動すればよい。v1 は managed 限定のままにするが、
   塞がっているからではなく、CLI ルートに口を増やすほどの需要がまだ無いため。
2. **claude の compaction 済み会話での手術。** サマリ行をまたぐ切断の実挙動。圧縮サマリ**から**の
   分岐は拒否済みだが、サマリより手前を指した分岐（要約の素になった生の履歴が既に無い場合）は
   未実測。
3. **claude の sidechain（Task サブエージェント）を含む区間の切断。** サブエージェントの発言を
   アンカーにすることは拒否済み。未実測なのは、親のターンだけを切って sidechain 行が
   宙に浮いた prefix が残る場合の resume 挙動。
4. ~~**opencode の `messageID` に assistant メッセージ ID を渡したときの挙動**~~ **不要になった**:
   「続きから」は *次のユーザー発言の id* を渡す設計にしたので、assistant の id を分岐点に
   することが無い（`>=` の打ち切りは同じだが、指す対象がユーザー発言だけで済む）。
5. ~~cursor の転写手術~~ **調査完了・不可**（§55.5 — 転写に ID が無く、実体は非公開 SQLite）。
6. ~~**copilot の復元元**~~ **解決: events.jsonl が正**（§55.5）。実装済み。「別 id の
   ディレクトリが生える」という当初の記録は誤読で、追試で否定された（§55.5）。

## 55.11 実測の再現手順

**claude**（scratch ディレクトリと使い捨て sid で行う。元セッションには触らない）

```
mkdir -p /tmp/forkprobe && cd /tmp/forkprobe
S1=$(cat /proc/sys/kernel/random/uuid)
claude --session-id $S1 -p "Remember the codeword ALPHA. Reply exactly: OK" --model haiku
claude --resume  $S1 -p "Forget that. The codeword is now BETA. Reply exactly: OK" --model haiku
# 転写は $CLAUDE_CONFIG_DIR/projects/-tmp-forkprobe/$S1.jsonl
# 1 ターン目末尾 assistant 行の uuid を U とする
claude -p --resume $S1 --resume-session-at $U --fork-session --session-id $S2 \
       "What is the codeword? Answer with one word." --model haiku   # → ALPHA
```

TUI で無視されることの確認は、同じフラグ列を `-p` 抜きで tmux ペインに載せ、
`tmux capture-pane` で復元された履歴を見る（→ BETA が残っている）。

**codex**

```
codex app-server generate-json-schema --out /tmp/codexschema
cat /tmp/codexschema/v2/ThreadForkParams.json   # lastTurnId の定義
# turn id は rollout の payload.type=task_started / turn_context に載る
```

**opencode**

```
opencode serve --port <空きポート> &
curl -s http://127.0.0.1:<port>/doc | jq '.paths["/session/{sessionID}/fork"].post.requestBody'
```

実装（`Session.fork` の `p.info.id >= q.messageID` による打ち切り）は配布バイナリの
文字列から確認した。**プローブで起こしたセッション・転写・サーバは後片付けする**こと。
