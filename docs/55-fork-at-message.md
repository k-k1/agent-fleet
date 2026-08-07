# 55. 発言時点からの会話分岐（fork at message）

> 設計確定・未実装。設計判断は [decisions/0039](decisions/0039-fork-at-message.md)。
> 旧判断（会話まるごと分岐のみ・地点分岐は非サポートにつき却下）は
> [history/fork-from-chat.md](history/fork-from-chat.md)。本書はそれを差し替える。

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
| cursor | 無し（`/fork` は TUI 内コマンド） | — | 転写手術なら理屈上可（未検証） |
| copilot | 無し | — | 同上（未検証） |
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

同じ session 内で巻き戻す `POST /api/session/{id}/revert/stage`・`revert/commit`（`RevertState`
は `{messageID, partID, snapshot}`）も別に存在するが、本機能は**元を残す**ので使わない。

## 55.3 意味論（どこで切るか）

ユーザーが選ぶのは**自分の発言**であり、v1 の意味論は 1 つに固定する。

> **選んだユーザー発言の「直前」まで**を引き継ぐ。選んだ発言自体は分岐先に含まれない。

そのユーザー発言をやり直したいから分岐するのであって、同じ発言をもう一度読ませたいわけでは
ない。分岐先は「その発言を打つ直前の状態」で開き、コンポーザーには元の発言文を**下書きとして
入れておく**（送信はしない）。書き直すのも、そのまま送るのも 1 操作で済む。

この意味論は 3 種すべてに素直に落ちる。

| kind | 渡す値 | 包含 |
| --- | --- | --- |
| claude | 選んだユーザー行の**直前の行**の uuid（＝`--resume-session-at`）／手術では選んだ行の手前で切る | 直前まで |
| codex | 選んだ発言が属する turn の**1 つ前**の turn id（`lastTurnId` は inclusive） | 直前まで |
| opencode | 選んだ発言の `messageID` そのもの（exclusive） | 直前まで |

「この発言と回答まで含めて分岐したい」（＝続きから別方向）は v1.1 の追加オプションとする。
どのエンジンでも**アンカーを 1 つ後ろへずらすだけ**で表現できるので、契約は変えずに済む。

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
{ "at": "<anchorId>", "draft": true }
```

- `at` 省略＝従来どおり会話まるごと分岐（後方互換）。
- `at` を解決できない（そんな anchor が無い／進行中 turn／切断不可地点）ときは
  `400 fork_bad_anchor`。既存の `fork_unsupported_kind` / `fork_missing_dir` と同じ体系。
- `draft` は Console 用のヒント。true なら応答に**分岐点の元発言テキスト**を含め、Console が
  コンポーザーの下書きに入れる。Agent 側は転写から取れるので追加の状態は要らない。

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

### opencode（最初にやる）

`workspace/agent/internal/agents/opencode/driver.go` の `serveForkSession` は今 `{}` を
POST している。`{"messageID": …}` を送るだけ。`ResolveForkAt` は**アンカーをそのまま返す**
（exclusive なので変換不要）。

### codex

`workspace/agent/internal/agents/codex/driver.go` の `threadFork` に `lastTurnId` を追加。
`ResolveForkAt` は**選んだ発言の turn の 1 つ前の turn id** を rollout から引く（inclusive の
ため）。進行中 turn は `thread/fork` が拒否するので、その手前で `fork_bad_anchor` にする。

TUI 経路（`codex resume`）は、fork が rollout をいつ書くかに依存する。driver に
「初回 turn 前のスレッドは rollout が無く resume できない」実測コメントがあるため、
**fork 直後に rollout が存在するかは §55.9 の未検証項目**。存在しないなら TUI codex は
v1 対象外（managed が既定なので実害は小さい）。

### claude（手術）

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

**代替ルート（採らないが残す）**: 分岐時に「最初の指示」を必須入力にすれば
`claude -p --resume <src> --resume-session-at <uuid> --fork-session --session-id <new> "<指示>"`
という公式フラグ経由で材料化できる（実測 1）。手術を避けられる代わりに、そのターンが
headless で丸ごと走る（ツール実行・長時間化）ため v1 では採らない。判断は ADR 0039。

### cursor / copilot / kiro / agy

v1 対象外。cursor は Claude Code 互換 JSONL、copilot は `session-state/<sid>/events.jsonl` で、
どちらも **AF 側が session id を採番する**方式（`registry` の実測コメント）なので claude と
同型の手術が効く見込みはある。kiro は CLI が ID を採番するため事前採番ができず、agy は
SQLite。いずれも未検証で、`CanForkAt` は false のままにする。

## 55.6 Console

- ミラー（`console/src/features/mirror/MirrorView.tsx`）の**ユーザー発言ブロック**に
  「ここから分岐」を出す。出す条件は `caps.canForkAt && turn.anchorId`。ホバー／フォーカスで
  現れる控えめな affordance にし、既存のターンフッターの並びに入れる。
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
- **Agent HTTP**: `at` 有無での分岐、`fork_bad_anchor` の各条件、後方互換（`at` 省略）。
- **実 CLI 契約テスト（ドリフト検知）**: `cli-version-pin-e2e` の層に足す。
  - claude: **手で切り詰めた jsonl が resume でき、切り詰め後の履歴だけを見ている**こと。
    ここが claude 更新で壊れる唯一の場所なので、ピン更新のたびに回す。
  - codex: `thread/fork` に `lastTurnId` を渡した結果の履歴長。
  - opencode: `messageID` の exclusive 性（指定した発言が分岐先に無いこと）。
- **Console**: `MirrorView` の affordance 表示条件（`anchorId` 空で出ない）と下書き投入の dom テスト。

## 55.9 フェーズ

| Phase | 内容 |
| --- | --- |
| P0 | 未検証項目（§55.10）の実測。特に codex の rollout 生成タイミング |
| P1 | `Turn.AnchorID` / `Meta.ForkAt` / API `at` / `ForkAtResolver` / `CanForkAt` の骨組み＋ opencode 実装 |
| P2 | codex（`lastTurnId`）＋ Console UI（分岐 affordance・確認モーダル・下書き投入） |
| P3 | claude（jsonl 手術＋切断点検査＋縮退）＋ ドリフト検知テスト |
| P4 | v1.1: 「この発言と回答を含めて分岐」オプション、cursor / copilot の実験 |

opencode を先にやるのは、**公式 API で最も改修が小さく、契約（アンカー・API・cap）の形を
実際に動かして確かめられる**ため。claude を最後に置くのは、唯一非公式な書き込みを伴い、
先に契約が固まっているほど手術範囲を小さく保てるため。

## 55.10 未検証（実装前に潰す）

1. **codex `thread/fork` は rollout をいつ書くか。** 初回 turn 前に書かないなら TUI codex の
   分岐は成立しない（managed のみ対応になる）。
2. **claude の compaction 済み会話での手術。** サマリ行をまたぐ切断の実挙動。
3. **claude の sidechain（Task サブエージェント）を含む区間の切断。** 親 turn だけを切って
   sidechain 行が孤立した場合の resume 挙動。
4. **opencode の `messageID` に assistant メッセージ ID を渡したときの挙動**（コード上は同じ
   `>=` 比較で成立するはずだが、v1.1 の「含める」で使うので実測しておく）。
5. cursor / copilot の転写手術（P4）。

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
