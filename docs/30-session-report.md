# 30. セッション完了報告 → フリート・オペレーター

フリート・オペレーター（af_write アシスタント）が `create_session` / `send_to_session` で
起動・指示したセッションについて、**応答完了・質問・異常終了をオペレーターの会話へ自動報告**し、
オペレーターが（ユーザー発話なしで）後続を処理できるようにする。

> ステータス: 設計確定・実装済（本ドキュメントが正本）。docs/19（assistant-chat）の続き。

## 背景 / 何が欠けていたか

- オペレーターは `create_session(initial_prompt)` / `send_to_session` で指示は出せるが、
  完了を知る手段が「返ってきた name を覚えて get_session_status を能動ポーリング」という
  persona 指示頼みだった。会話が止まればそこで途切れる。
- 通知センター（internal/notice → CP → Console）は membership 単位のブロードキャストのみで
  「特定の会話へ届ける」宛先概念が無い。
- セッション Meta に「誰が起動したか」を示すフィールドが無い（ForkFrom は会話コピー元であり
  起動主体ではない）。

## 設計

### 紐付け — モデルの記憶に頼らない（report_to の自動付与）

- `chat_providers.go mcpConfigArgs` / `codexMCPArgs`: af_write 会話の MCP サーバ起動 args に
  `--conv <会話id>` を付与（`["mcp-stdio","--write","--conv",<id>]`）。
- `mcp_stdio.go`: `--conv` をパースし、`create_session` は `POST /sessions` の `report_to` に、
  `send_to_session` は `POST /sessions/{name}/input` の `report_to` に**ツール側で自動同梱**。
  オペレーター（モデル）は何も覚えなくてよい。
- opencode バックエンドは対象外（write 用 opencode.json が grant 単位の共有ディレクトリで
  会話 id を焼き込めない）。claude / codex の af_write 会話で有効。

### arm/disarm — 「指示1件につき報告1回・報告は完了（終端）で」

「完了」という状態は存在しない（Stop hook は**毎ターン**発火する）。素朴に配線すると
報告がターンごとに飛ぶため、one-shot の arm/disarm にする:

- ストア: `~/.config/agent-fleet/session-report/<name>.json` = `{conv, armed, at}`
  （fstore。Meta には手を入れない — 動的状態を Meta と別ファイルに置くのは record-exit と
  同じ理由のレース回避）。
- **arm**: `create_session`（report_to 付き）と `/input`（report_to 付き prompt 送信）の成功時。
- **オペレーター報告（＋disarm）は終端イベントのみ**（`session_status.go` の hook 経路）:
  - 状態遷移 `answer-ready`（＝完了・入力待ち。`reportKindAnswerReady`）
  - 異常終了 `oom` / `crashed` / `killed`（`record_exit.go`。正常 exit / 意図停止は報告しない）
- **中間の要対応イベント `question` / `plan-approval` / `permission-request` は
  オペレーター報告しない**（通知センター `notice` へは全 kind 出す）。**arm を消費させない**のが
  肝で、質問等で先に disarm されると「指示の完了」がオペレーターへ二度と届かない（実測不具合）。
  arm は `answer-ready` か異常終了まで生存する。
- 次の `send_to_session` で再 arm。managed driver のセッションは hook 経路を通らないため
  v1 の報告対象外（オペレーターの既定起動は claude tui）。

### 配送 — Agent 内で完結

hook / record-exit は独立プロセスなので、会話ファイルへの直接追記はしない
（convLocks / liveTurns はサーバプロセス内のため競合する）。代わりに:

1. hook（`recordSessionNotification`）が arm を確認し、**turn テキスト
   （pending-text バッファ、applyPendingPayloads で消える前に捕獲）の末尾抜粋**を添えて
   `POST /chat/report` を localhost Agent へ kick（AGENT_TOKEN はコンテナ env で hook にもある）。
2. サーバ（`chat_report.go handleChatReport`）が arm 検証 → disarm → 即時応答し、
   goroutine で処理:
   - 会話に **role="report"** のメッセージを追記（`Session` フィールドにセッション名。
     Console はセッション由来カードとして描画 — 「あなた」でもオペレーターでもない）。
   - `notice.Put("session-report", …, Payload={conversation_id, conversationTitle})` で
     通知センターにも配送（クリックで該当会話を開く）。
   - 自動ターン（下記）。

### 自動ターン — 既定 ON・ユーザー発話なしは最大10回

- ui-prefs `assistantAutoTurn`（既定 true、設定 > エージェント）。
- 会話レコードに `auto_turns`（ユーザー発話なしで実行した自動ターン数）。**上限 10（定数、
  無制限なし）**。ユーザーがメッセージを送ると 0 にリセット。
  ユーザー不在での暴走ループ（追撃→完了→報告→追撃…）の構造的歯止め。
- 実行: 未配送の report を定型プリアンブル付きで 1 プロンプトに連結し、通常の provider
  send（`registerLiveTurn` 登録 = 停止ボタン / in_progress 対応）。
- **未配送 report の注入**: 自動ターンが OFF / 上限 / 実行中ターンありで走らなかった report は
  `delivered=false` のまま残り、**次のターン（ユーザー送信 or 自動）のプロンプト先頭に注入**して
  から delivered にする。保存された会話とプロバイダ側コンテキストの乖離を防ぐ。
- prompt injection ガード: 報告本文はセッション出力（攻撃者影響下になり得るデータ）。
  operatorPersona に「自動報告への応答で新規セッションを起こす場合は利用者確認を先に」を明記
  （af_write の既存ガード方針を踏襲 — ゲートはツール集合＋persona）。

### Console

- ChatView: `role:"report"` をセッション報告カードとして描画（broadcast アイコン＋
  セッション表示名、本文 Markdown）。
- af_write の会話はペインがアクティブな間 5 秒ポーリングで追従（報告・自動ターンの結果が
  開いたまま見える。自動ターン中は既存の in_progress 再アタッチ表示に乗る）。
- 通知センター: kind `session-report` の文言＋クリックで会話を開く。
- 設定 > エージェント: 「報告への自動応答」トグル。

## 制限 / 将来

- managed driver（opencode/codex の pane なし）セッションは v1 報告対象外。
- opencode バックエンドの af_write 会話は report_to 自動付与なし。
- 報告は `answer-ready`（完了・入力待ち）と異常終了のみ。中間の質問/承認/許可待ちは
  オペレーター報告しない（通知センターには出る）。キュー済みプロンプトが残っている等で
  厳密なタスク完了とずれることはあり得る（オペレーターが get_session_output で確認する前提）。
- 将来: Meta への起動元（LaunchedBy）記録、managed driver 対応、報告のバッチング。
