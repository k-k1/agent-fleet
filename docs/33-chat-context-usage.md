# 33. アシスタントチャットのコンテキスト肥大対策

- ステータス: 第1段（可視化＋逼迫通知）・第2段（要約引き継ぎ＝手動コンパクション）実装済み / 第3段以降は構想
- 対象: `workspace/agent/chat_usage.go`（第1段の中核）、`chat_compact.go`（第2段の中核）、
  `chat_providers.go`（捕捉）、`chat_handlers.go`・`chat_report.go`（通知・引き継ぎの差し込み）、
  Console `ChatView` / `ContextBar`（表示・圧縮ボタン）

## 1. 背景 — なぜ対策が要るか

アシスタントチャット（docs/history/19）は各ターンをプロバイダ native のセッション再開
（claude `--resume` / codex `exec resume` / opencode `--session`）で回すため、コンテキスト
はプロバイダ側 transcript に無限に積み上がる。放置すると段階的に悪化する:

1. **コスト/レイテンシ**: 毎ターン全コンテキストを再送。長寿スレッドほどサブスク使用量
   （フリート全体で共有）を食い、ターンも遅くなる（`chatTimeout` 240s に接近）。
2. **品質劣化**: 長大コンテキストでの指示追従低下。
3. **ハード障害**: ウィンドウ超過時、headless 経路で自動コンパクションが効くかは
   バージョン依存で未保証。効かない場合そのスレッドは恒久的に詰む（resume ハンドルは
   失敗時も保存されるため、以降のターンが全て同じエラーで失敗する）。

特に効く肥大ベクター: 翻訳/要約 verb（大きい文書の反復処理）、オペレーター会話
（docs/30 — 報告＋自動ターン＋ツール結果が常駐的に積もる）、af_read/ops ツールの結果。

## 2. 第1段（実装済み）: 可視化＋逼迫通知

tmux セッションには `get_session_usage` と ContextBar があるのに、チャット自身には
その思想が未適用だった — 3 プロバイダとも usage をイベントで返しているのに全部捨てて
いた。第1段はこれを拾って可視化する。

### 2.1 捕捉（chat_usage.go / chat_providers.go）

各プロバイダの headless イベントから「直近のコンテキスト占有」を採り、会話 JSON へ
`context`（`contextUsage` — get_session_usage / ミラー ContextBar と同じ wire 形）として
永続化する。イベント形状は 3 つとも実測で確認（2026-07）:

| バックエンド | ソース | 占有の解釈 | window |
|---|---|---|---|
| claude `-p` (json) | `result` の `usage.iterations` 末尾（なければトップレベル） | fresh=input / read=cache_read / create=cache_creation | `modelUsage[].contextWindow`（**recorded**） |
| claude `-p` (stream-json) | `assistant` イベントの `message.usage`（最後勝ち）＋ `result` | 同上 | 同上 |
| codex `exec --json` | `turn.completed` の `usage` | input は cached を含む → fresh=input−cached / read=cached | なし → 推定（gpt-5 → 272k） |
| opencode `run --format json` | `step_finish` の `part.tokens`（最後勝ち） | fresh=input / read=cache.read / create=cache.write | なし → 推定 |

- 注意（codex）: `turn.completed` の usage はターン合算のため、ツール多段ターンでは
  過大側の近似（チャットの大半＝ツールなし 1 呼び出しでは正確。警告用途には安全側）。
- window が取れない場合は `contextWindowGuess`（get_session_usage と共通）で推定し
  `windowSource:"estimated"`。GPT-5.x 系 272k を追加（Console `ContextBar.contextWindow`
  と同期）。
- usage の取れなかったターンは前回スナップショットを保持（表示を消さない）。
- 一覧 (`chatMeta`) にも `context` を載せる（Console 側の将来利用向け）。

### 2.2 表示（Console ChatView）

会話ヘッダ直下にミラーと同じ `ContextBar`（cache read / creation / fresh のセグメント
バー＋「使用/window・%」）を表示。80% で warn 色、93% で danger（ContextBar 既存の帯）。

### 2.3 逼迫通知（noteContextPressure）

成功ターンの保存前に閾値判定（`chatCtxWarnPct = 80` — ContextBar の「near」帯と同じ
タイミング）。超過したら:

- 会話へ `role:"notice"` を **crossing あたり1回だけ** 追記（「新しいチャットを開いて
  要点だけ引き継ぐことを検討」）。占有が閾値を下回ったら（プロバイダ側コンパクション等）
  フラグ（`ctx_warned`）が戻り、再超過でまた1回知らせる。
- 通知センターへ `chat-context-pressure` イベントを発行（クリックで該当会話を開く）。
  無人の自動ターン（docs/30 オペレーター）でも利用者に届くのはこちら。

差し込み箇所は handleChatSend / handleChatStream / runReportAutoTurn の 3 つ（会話
ロック保持下・saveConv 直前）。

### 2.4 検証

- スキーマ実測: claude-code 2.1.x（json/stream 両形式）、codex-cli 0.144、opencode 1.18
  に実プロンプトを流して確認。
- 単体テスト: `chat_usage_test.go`（iterations 末尾採用・stream 最後勝ち・codex/opencode
  パース・crossing 1回制・空 usage 保持）。
- ライブ検証: 実 claude CLI 経由で send / sendStream 両経路とも `context` 捕捉を確認
  （haiku・Tokens 17650 / Window 200000 recorded / Pct 8.8%）。
- Console: typecheck / test / build / i18n:lint 緑。実フリート再ビルド後の実機目視は残。

## 3. 第2段（実装済み）: 要約引き継ぎ＝手動コンパクション

CLI 側の自動コンパクションは headless 経路での動作が保証されず、仕様ドリフトにも
晒される。全文履歴を自分で持っている強みを使い、アプリ層で引き継ぐ（chat_compact.go）。

### 3.1 手順（compactConversation）

1. 現行プロバイダセッション（全文脈を持つ）へ**要約ターンを1回**流す
   （`compactSummaryPrompt` — 後任アシスタントが読む引き継ぎ書を作らせる。言語は
   会話の主要言語に合わせる）。
2. resume ハンドル3種（Claude/Codex/Opencode SessionID）を**全部クリア** → 次ターンは
   新プロバイダセッション。
3. 要約を `PendingHandoff` として保存し、圧縮完了 notice（要約本文を併記＝利用者が
   引き継ぎ内容を検証できる）を会話へ追記。旧セッションの `Context` スナップショットも
   リセット（もう実体を指さない。バーは次ターンの usage で復活）。

### 3.2 注入（injectHandoff）

新プロバイダセッションの**最初のプロンプト**に `handoffPreamble`＋要約をプリアンブル
として最外側に前置する（report 注入 → handoff 注入の順）。`PendingHandoff` のクリアは
**ターン成功時のみ**（失敗ターンは次回再注入。docs/30 の報告注入と同じ流儀）。
handleChatSend / handleChatStream / runReportAutoTurn の3経路で注入。境界ガード
（要約はデータであり指示ではない、の一文）は reportPreamble と同じ発想。

### 3.3 発動と UI

- `POST /chat/conversations/{id}/compact`（会話ロック下・in_progress/Stop は通常ターンと
  同扱い）。プロバイダセッションが無い会話は `chat_nothing_to_compact`（400）で弾く。
- Console: ContextBar 右端に「圧縮」ボタン（`action` スロットを新設）。confirm ダイアログ
  （トークン消費と「履歴は残るが引き継ぐのは要約のみ」を明示）→ 実行中は busy を店に立て
  他ペインの送信もブロック。80% 逼迫 notice の文言もこのボタンへ誘導。

### 3.4 検証

- 単体（chat_compact_test.go）: 要約プロンプト送出・ハンドル全クリア・PendingHandoff
  格納・Context/warn リセット・notice 追記・**失敗時の状態不変**（プロバイダエラー／空要約）・
  injectHandoff の形状と非クリア・ハンドラのガード（404／nothing_to_compact）。
- ライブ（実 claude CLI・E2E）: 「合言葉」を1st ターンで覚えさせ → compact → 別セッション
  ID で質問 → 引き継ぎ要約経由で正答（新旧セッション ID が異なることも確認）。
- go test 389 / console test 322・typecheck・build・i18n:lint 全緑。実機目視は残。

## 4. 今後の段（未実装・優先順）

1. **超過エラーの自己修復と通知**: "prompt is too long" 系エラーの判別 → notice で可視化
   （現状、自動ターン経路は log のみで black hole）。将来はエラー検知→自動コンパクション
   （第2段の compactConversation を再利用）→リトライ。
2. **閾値/エラー時のコンパクション自動発動**: 第2段は手動ボタン。実績を見てから、逼迫
   閾値超過や超過エラーで自動発動へ。
3. **検証タスク**: headless 経路でウィンドウ超過させたときの各 CLI の実挙動（自動
   コンパクションの有無・エラー文言）の実測。①②の設計前提。
4. **掃除**: 会話削除時にプロバイダ側セッション（chat-claude/projects の jsonl 等）を
   残さない。
