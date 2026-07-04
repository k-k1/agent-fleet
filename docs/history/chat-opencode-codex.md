# 22. チャット（MirrorView）を codex / opencode へ汎化

> 🗄 **設計＋実装記録**。claude 専用だったチャット（MirrorView）を、極力共通化したまま codex と opencode へ広げる。
> 段1（codex）・段2（opencode）ともに実装済み。

## 22.1 ゴールと発見

claude セッションだけが持っていた「ターミナル⇄チャット」を、codex / opencode でも同じ UI・同じ操作で使いたい。
調査で分かった要点は次の2つ。

- **入力はタダ**。`POST /sessions/{name}/input` は tmux send-keys で TUI に打ち込むだけでエージェント非依存。
  codex / opencode の TUI にもそのままプロンプトを送れる。コンポーザは原理的にもう動く。
- **claude 固有なのは読み取り側だけ**。`session_transcript.go` が claude の `<sid>.jsonl` スキーマに密結合していた。
  ここを汎化すればチャットが成立する。

土台は既に整っていた。Go（`agent.go` の `Agent` インターフェース＋`agentCaps`）と Console（`agents/registry.ts` の
`AgentCaps`）の双方にケイパビリティ駆動のレジストリがあり、codex/opencode は caps が全 false ゆえチャットが出ないだけ
だった。「デスクリプタを1つ足せば点灯する」構造は完成済み。

## 22.2 実スキーマ（稼働コンテナで採取・検証済み）

### codex — rollout JSONL（段1の対象）

`~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<session_id>.jsonl`。1 行 1 イベントの append-only JSONL。
codex の session_id は既に捕捉済み（`codexSids`、status フックの stdin から）。ゆえに id でファイルを glob できる。

- `{"type":"session_meta","payload":{cwd, git:{branch}, ...}}` — 先頭。cwd/branch のコンテキスト。
- `{"type":"response_item","payload":{"type":"message","role":"user"|"assistant"|"developer","content":[{"type":"input_text"|"output_text","text":...}]}}`
  - `developer` はシステム指示（permissions / skills / collaboration mode）＝ノイズ、破棄。
  - `user` の先頭ターンは `<environment_context>` 等の注入コンテキストのことがある＝ラッパー判定で破棄。
- `{"type":"response_item","payload":{"type":"function_call",name,arguments}}` — ツール呼び出し。assistant 側の薄い痕跡。
  `function_call_output` / `reasoning` は破棄。
- `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{input_tokens,cached_input_tokens,output_tokens}}}}`
  — トークン使用量。直前の assistant ターンへ付与（コンテキストバーが claude と同様に効く）。
- 他 `event_msg`（user_message / agent_message / task_started / task_complete）は clean text の別経路だが未使用。

**JSONL ゆえ claude と同じ行ベースの発想が乗る。低リスク＝段1で採用。**

### opencode — SQLite（段2の対象・未実装）

`~/.local/share/opencode/opencode.db`（WAL）。ファイル分散でなく **SQLite**。opencode の `ses_…` id は捕捉済み（`opencodeSids`）。

- `message(id, session_id, time_created, time_updated, data)` — `data` は JSON:
  `{role:"user"|"assistant", tokens:{input,output,reasoning,cache:{read,write}}, modelID, providerID, time:{created,completed}, ...}`
- `part(id, message_id, session_id, time_created, data)` — `data.type` = `text` / `reasoning` / `tool` / `patch` / `step-start` / `step-finish`。
  text/reasoning は `.text`。tool はツール呼び出し情報、patch は差分。step-* は骨組みで破棄。
- 補助: `todo`（ToDo）、`permission`（承認）テーブルも存在＝将来 ToDo/許可の汎化余地。

段2は message を `session_id` で time_created 順に読み、part を message_id で結合して正規化する（`opencode_transcript.go`
の `readOpencodeTranscript`/`opencodeReadSession`）。Go は純 Go の modernc/sqlite（CP で既採用、Agent モジュールへ追加）を
**read-only**（`?mode=ro&_pragma=busy_timeout(3000)`、journal_mode は書込ゆえ設定しない）で開く。Agent は opencode と同一
uid ゆえ WAL の `-wal`/`-shm` を読め、WAL は db ファイルから自動検出される。**単一 jsonl が無い**ため行カーソルは使えず、
message 序数を Idx にしてターン単位カーソル（22.3 の汎用パス）に乗せる。part 種別: text→本文、tool→痕跡（`state.input`
の command/path 等を Info に）、patch→ファイル列の痕跡、reasoning/step-*→破棄。トークン（message.tokens.input/output/
cache）とモデル（modelID）はコンテキストバー/ヘッダへ。実 WAL DB（運用者の opencode.db、19 ターン）で read-only 読取を実機確認。

## 22.3 汎化アーキテクチャ（実装＝段1）

claude のホットパスは触らずリスクを新規コードに隔離する **ハイブリッド** を採る。

- `Agent` インターフェースに `transcript(meta) (turns []chatTurn, path string, ok bool)` を追加。native ストアを
  共通の `chatTurn`/`chatPart` へ正規化して返す。claude は自前の行パスを使うので `ok=false`（`noGenericTranscript`
  を埋め込むだけ）。codex が実装（`readCodexTranscript`）。
- `handleSessionMessages` は kind==claude なら従来パス、それ以外は `handleGenericMessages` へ分岐。
- `handleGenericMessages` は全ターンを受け取り、**claude と同じセマンティクス**で窓を切る（初回 tail・`before` 逆ページ・
  `since=<cursor>` 増分・shrink で reset）。カーソルはここでは「ターン数」だが、クライアントは reset/firstLine/hasMore で
  不透明に扱うので互換。MirrorView のカーソル機構は無改修。

codex パーサ（`codex_transcript.go`）は response_item を背骨に、message→ターン、function_call→assistant のツール痕跡
（Console の `groupTurns` が隣接 assistant ブロックへ併合）、token_count→直近 assistant のトークンへ写す。developer と
ラッパー user は破棄。ターンは絶対行 index を Idx に保持（安定キー＋ページング単位）。

### Console（段1）

`registry.ts` で codex を点灯: `chat / transcript / contextBar`（fork は無し＝codex は --session-id ピン不可、質問/プラン/
許可インラインも無し＝あれは claude フック payload）。claude 固有の表示を caps 化: assistant 署名を `assistantName`
（Claude/Codex/opencode）に、画像貼付を新 cap `imagePaste`（claude のみ）でゲート。pending（質問/プラン/許可）・ToDo・
terminalState はサーバが claude にしか送らないので自然に非表示。MirrorView は `agentOf(kind)` で署名と caps を解決。

## 22.4 機能マトリクス

| 機能 | claude | codex(段1) | opencode(段2) | 方針 |
|---|---|---|---|---|
| ターン表示 / ツール痕跡 | ✅ | ✅ | ✅ | 汎化（核） |
| プロンプト送信 | ✅ | ✅ | ✅ | tmux で既存 |
| コンテキストバー | ✅ | ✅ | ◯ | 両者トークン有り＝汎化 |
| 停止セッションの履歴閲覧 | ✅ | ✅ | ◯ | transcript cap |
| fork | ✅ | ✕ | ✕ | claude 限定（--fork-session） |
| 質問/プラン/許可インライン | ✅ | ✕ | ✕ | claude フック payload、ゲート |
| ToDo | ✅ | ✕ | △ | opencode は todo 表あり＝後続 |
| 画像貼付 | ✅ | ✕ | ✕ | Read ツール前提、ゲート |
| diff ペイン | ✅ | △ | △ | ツール名マップ拡張で後追い |

## 22.5 検証

- **段0（実スキーマ採取）**: 稼働コンテナ `af-ws-*` で codex rollout（10 ファイル）と opencode `opencode.db` の実イベント/
  テーブル/JSON 形状を採取・確認（22.2）。
- codex パーサ: `codex_transcript_test.go`（developer/ラッパー破棄・user/assistant・function_call 痕跡・token_count 付与・
  絶対 index・空/システムのみ→0 ターン）。Go `build`+`vet`+`test` 通過。
- Console `vite build`+`tsc --noEmit` 通過。
- 実会話 E2E は Agent イメージ再ビルド＋コンテナ recreate が要るため反映後に実ブラウザで確認（合成テストで担保）。

## 22.6 触れたファイル

- Agent（段1 codex）: `workspace/agent/codex_transcript.go`＋`_test`（新規）、`workspace/agent/agent.go`（`transcript`
  seam＋`noGenericTranscript`＋codex caps/実装）、`workspace/agent/session_transcript.go`（分岐＋`handleGenericMessages`
  ＋`capTurnsNewest`）、`workspace/agent/codex_auth.go`＋`codex_trust_test.go`（`ensureCodexFolderTrusted`）。
- Agent（段2 opencode）: `workspace/agent/opencode_transcript.go`＋`_test`（新規、modernc/sqlite read-only）、
  `workspace/agent/agent.go`（opencode caps/実装）、`workspace/agent/go.mod`（modernc/sqlite 追加）。
- Console: `console/src/agents/registry.ts`（codex/opencode 点灯＋`imagePaste`/`assistantName`）、
  `console/src/views/MirrorView.tsx`（`agentOf` 解決・署名汎化・画像貼付ゲート・モデル名）。

## 22.7 残り / 既知の限界

- **反映**: Agent 変更ゆえ Workspace イメージ再ビルド＋コンテナ recreate、CP/Console 再起動が要る。
- **codex トークンの遅延**: 最終 assistant ターンの token_count は同ターンでは再送されない（カーソルは append のみ）ため、
  そのターンのコンテキストバーは次ターンまで欠けうる。軽微。
## 22.8 拡張（reasoning / 実窓 / ツール出力・差分 / ToDo）

段1・段2 の後、codex/opencode チャットを claude と対称化する 4 拡張を追加。共通の chatTurn/chatPart を拡張し、
汎用パスに相乗り（Console はデータのあるターンだけ描画）。

1. **思考(reasoning)ブロック** — chatPart に `thinking` kind を追加。codex=`reasoning` response_item の summary_text、
   opencode=`reasoning` part の text。Console は `ThinkingBlock`（折りたたみ「思考」）。
2. **実コンテキスト窓** — chatTurn に `CtxWindow`。codex の token_count `model_context_window`(258400)を assistant
   ターンへ。`ContextBar` は明示 window を優先し、モデル名推測(200k 固定)のズレを解消。opencode は窓を記録せず推測継続。
3. **ツール出力・差分** — chatPart に `Output`。codex=`function_call_output` を call_id で紐付け、opencode=tool part の
   `state.output`。Console は出力を折りたたみ表示。差分は opencode `write`/`edit` の filePath+content/old/new を
   File+Edits に載せ、既存の差分ペインで開く（codex apply_patch のパース差分は後続、当面は出力表示）。
4. **ToDo** — `transcript()` の戻りを `transcriptData{turns,path,tasks}` に拡張し tasks を汎用パスから返す。
   opencode=`todo` 表(session_id/content/status/position)、codex=`update_plan` function_call(plan[] を全再送=最新採用)。
   Console 既存の `TaskChecklist`(resp.tasks)がそのまま描画。

検証: opencode は実 WAL DB で reasoning/tool 出力/write 差分を確認、todo は合成 SQLite の単体テスト（実 DB は行なし）。
**codex の reasoning/function_call/update_plan は運用データがトリビアルなセッションのみで実在せず、仕様ベース実装＋
合成テストで担保**。実 codex コーディングセッションでの目視は後続。Go 41 tests/vet、Console tsc 通過。

## 22.9 残り / 既知の限界

- **codex 実データ検証**: reasoning/function_call 出力/update_plan は実 codex セッションで未目視（22.8）。
- **後続**: 質問/許可インライン（opencode `permission` 表・codex request_user_input）、codex apply_patch の差分パース、
  opencode コスト($)表示、codex レート制限、opencode の SQLite スキーマのバージョン揺れ監視（modernc/sqlite・ro・WAL）。
