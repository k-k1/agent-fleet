# 0023. `kind=cursor`（Cursor CLI）を第7のエージェント種別として追加する

- 状態: **採用（Track A 実装済み）**（2026-07-23。Track 0 プローブ合格→workspace agent
  本体の read 層＋TUI を実装・`go build`/`go test` 緑）。残 A2/B/C/D。
  実装計画・実測・Track A の改良2点（自己採番 UUID／JSONL 末尾状態）は
  [docs/40](../40-cursor-agent-kind.md)。
- 関連: [0019](0019-copilot-agent-kind.md)（copilot — 直近の種別追加・本件のテンプレ）、
  [0008](0008-antigravity-cli-agent-kind.md)（agy — Terminal 専用 MVP の先例）、
  [0015](0015-agent-managed-driver.md)（managed driver 抽象）。
  ※ 0022 はエージェントメモリ版管理（未マージブランチ temp/s7in3bh）が使用中のため 0023 を採番。

## 背景

Cursor CLI（`cursor-agent`/`agent`、Anysphere）は `agent acp`（ACP = JSON-RPC over stdio、
公式ドキュメントあり）・`-p --output-format json|stream-json`（Claude Code 類似イベント）・
`agent create-chat`（chat ID 事前採番）・`--resume <chatId>`・hooks（`hooks.json`、
入力に `transcript_path`）・Claude Code 互換 JSONL 転写を備える。CLI フラグ・hooks
イベント名・設定形式は実バイナリ v2026.07.20-8cc9c0b で確認済み（docs/40 §実測記録）。
既存 6 種の中では copilot（ACP・hooks・イベント JSONL）に最も近い統合面を持つ。

## 決定

1. **v1 から Terminal (CLI) と Managed の両対応**（copilot 踏襲）。合格条件としていた
   ACP のクロスプロセス resume は **Track 0 プローブで合格**（`loadSession:true`・
   `session/load` で履歴リプレイ＋文脈保持を別プロセスから実証 — docs/40 §プローブ一覧）。
2. **表示順はユーザー指定で確定**: Claude, Codex, **Cursor**, GitHub Copilot, Antigravity, OpenCode
   （`SESSION_KINDS`・`repoLaunchKinds`・AgentsTab カード順に反映。他 UI は派生）。
3. **read 正本は公式契約面のみ**: hooks（stop/beforeSubmitPrompt → status seam）と
   JSONL 転写（`transcript_path`）。セッション実体 `~/.cursor/chats/**/store.db`
   （SQLite blob・非公開形式）は**読まない** — opencode ストア契約変更で false-idle を
   踏んだ教訓を適用。TUI 文字列にも依存しない。
4. **chat ID は `agent create-chat` で AF 側から事前採番**し sid-store に保存
   （copilot `--session-id` と同型 — agy の resume ID 捕獲問題を構造的に回避）。
5. **認証は専用フロー型**: `NO_OPEN_BROWSER=1 agent login`（URL 抽出 → claude/agy 型
   start/complete）＋ `CURSOR_API_KEY` 手動登録（codex 型）の併設。資格情報の保存先は
   非公開のためプローブで特定し `fs.go` denylist（`.cursor`）で保護。
6. **モデルカタログは `agent models` によるアカウント連動ライブ取得**（公式コマンドが
   あるため copilot のような TUI スクレイプは不要見込み）。
7. **WS バー使用量チップは v1 不採用**: プラン残量の公式 API/CLI が存在せず、非公式
   API は usage-chip 429 事件と同じ脆さのため。stream-json の per-turn トークンによる
   セッション単位表示を将来課題とする。
8. **rtk は hooks seam**（`rtk hook cursor` 新設 → `beforeShellExecution` 配線）:
   CLI は `api2.cursor.sh` 経由でプロバイダ直結でないため base-URL 差し替え不可。
   コマンド書換可否はプローブし、不可なら指示ベース（codex/agy 同格）に落とす。
9. **Cursor 自前機能でフリートの責務と重なるものは使わない**: `-w/--worktree`
   （隔離は Console worktree が正）・`agent worker`（Cursor cloud 側のオーケストレーション）・
   セッションの cloud 送出（`&` プレフィックス）。
10. **配備は版付き URL の焼き込み**（`downloads.cursor.com/lab/<版>/...` — 非公開 URL 仕様
    のため e2e-smoke 版ピン検証を必須化）＋ auto-update 封殺（手段はプローブで確定、
    最終手段は versions ディレクトリ書込禁止）。版数は日付形式で semver でない。

## リスク（受け入れ）

- 週次リリースの CLI ドリフト（hooks/JSONL/ACP の公式契約依存で緩和＋live テスト一次検知）。
- 版ピン・auto-update 無効化が非公式手段（e2e-smoke で破壊を検知）。
- linux arm64 の動作不良報告あり（ECS/native 展開前に実機確認を条件化）。
- Docker 内 `CURSOR_API_KEY` 不調のフォーラム報告（自コンテナで要検証）。

## 結果

（実装後に記載。トラック分割・教訓反映表・プローブ一覧は docs/40。）
