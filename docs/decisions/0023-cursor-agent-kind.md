# 0023. `kind=cursor`（Cursor CLI）を第7のエージェント種別として追加する

- 状態: **採用（Track A＋A2＋B＋C 実装済み）**（2026-07-23。Track 0 プローブ合格→read 層＋
  TUI＋managed driver＋配備＋CP/Console を実装・`go build`/`go test`＋`AF_CURSOR_LIVE=1`
  実 CLI 契約テスト＋Console typecheck/i18n:lint/vitest/vite build 緑）。残 D＋arm64 実機実行確認。
  Track C で **v1 は login-only を確定**（API キー手動登録は Track D 送り — 下記決定 5 追記）。
  実装計画・実測・Track A の改良2点（自己採番 UUID／JSONL 末尾状態）・Track A2 の ACP 契約
  （`cursor-agent acp` 起動・session/update からの転写メモリ構築・set_mode/cancel 実測）は
  [docs/40](../log/40-cursor-agent-kind.md)。
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
   踏んだ教訓を適用。TUI 文字列にも依存しない。**Track A で変更**: hooks.json 配線は
   v1 では**張らず**、TUI 状態は JSONL 転写末尾の分類のみで取る（グローバル
   `~/.cursor/hooks.json` の chatId→slot-sid キー付け問題を構造的に回避 —
   docs/40 §Track A の実測反映）。
4. **chat ID は `agent create-chat` で AF 側から事前採番**し sid-store に保存
   （copilot `--session-id` と同型 — agy の resume ID 捕獲問題を構造的に回避）。
   **Track A で変更（`create-chat` 事前採番は不採用）**: 実測で未知の valid v4 UUID を
   `--resume` に渡すとその ID で新規チャットが作られるため、**AF 自己採番の v4 UUID を
   `--resume` に渡す**方式に変更（copilot `--session-id` と完全同型・起動時の追加 exec が
   消える — docs/40 §Track A の実測反映）。
5. **認証は専用フロー型**: `NO_OPEN_BROWSER=1 agent login`（URL 抽出）。資格情報の保存先は
   `~/.config/cursor/auth.json`（プローブで特定）で `fs.go` denylist（`.config/cursor`＋
   `.cursor`）保護。**Track C で v1 は login-only に確定**（当初併設予定だった
   `CURSOR_API_KEY` 手動登録は Track D 送り）: ①cursor CLI は API キーの永続化コマンドが
   無く（`CURSOR_API_KEY`/`--api-key` のアンビエント利用のみ）、②活かすには各 exec への
   env 注入が要るが **TUI(tmux ペイン)には安全な注入シームが無く Program 文字列へ埋めると
   `ps` にキーが露出**（平文資格禁止に抵触）、③一方 login フロー（auth.json アンビエント）は
   TUI/managed/status/models 全経路を env 注入ゼロで賄うため。**login は code paste ではなく
   start→poll**（ブラウザ承認を CLI が自己ポーリング — codex device-auth 型。claude/agy の
   pasted code とは異なる）。
6. **モデルカタログは `agent models` によるアカウント連動ライブ取得**（公式コマンドが
   あるため copilot のような TUI スクレイプは不要見込み）。
7. **WS バー使用量表示は v1 不採用（プラン残量・セッション使用トークンの両方）**:
   - プラン残量チップ: 公式 API/CLI が無く、非公式 API は usage-chip 429 事件と同じ脆さのため。
   - セッション使用トークン数（他エージェント ContextBar 相当）: **2026-07-23 の実バイナリ
     プローブで、ライブ経路（managed=ACP／TUI=JSONL）に token/usage が一切乗らないことを
     確認**したため不採用。ACP は `session/prompt` 応答が `stopReason` のみ・`session/update` に
     usage 種別なし。JSONL は "Claude Code 互換" を謳うが `message.usage` を持たない。トークンが
     載るのは `-p --output-format json|stream-json` の終端 `result.usage` だけで、これは one-shot
     batch 経路（アシスタントチャット headless 用）でありライブセッションでは使わない。実現には
     上流が ACP に usage を載せるのを待つか、managed を `-p` 駆動に替えて **決定1（ACP
     `session/load` クロスプロセス resume）を捨てる**ことが要り、割に合わない。詳細は
     [docs/40 §使用量表示の実現可否プローブ](../log/40-cursor-agent-kind.md)。
   - 付随実測: **Free プランは named model 不可**（`Named models unavailable. Free plans can only
     use Auto.`）で Auto/composer-2.5 のみ可。起動でモデル未選択時にサーバ側既定が named に振れると
     free wall に当たり得るため、未選択時の Auto 明示前置を Track D の頑健化候補として記録。
8. **rtk は hooks seam**（`rtk hook cursor` 新設 → `beforeShellExecution` 配線）:
   CLI は `api2.cursor.sh` 経由でプロバイダ直結でないため base-URL 差し替え不可。
   コマンド書換可否はプローブし、不可なら指示ベース（codex/agy 同格）に落とす。
9. **Cursor 自前機能でフリートの責務と重なるものは使わない**: `-w/--worktree`
   （隔離は Console worktree が正）・`agent worker`（Cursor cloud 側のオーケストレーション）・
   セッションの cloud 送出（`&` プレフィックス）。
10. **配備は版付き URL の焼き込み**（`downloads.cursor.com/lab/<版>/...` — 非公開 URL 仕様
    のため e2e-smoke 版ピン検証を必須化。上流チェックサム非公開のため sha256 を自前計算して
    ピン）＋ **auto-update 封殺は公式 2 経路で確定**（Track B バンドル再解析）: 背景更新ゲート＝
    `disableAutoUpdate || channel==="static"` → `--disable-auto-update` root フラグ（全経路で
    前置）＋ `cli-config.json` channel:"static"（entrypoint 再固定）。AUR の versions 書込禁止
    fallback は不要になった。版数は日付形式で semver でない。焼き込みは root 所有の
    `/usr/local/share/cursor-agent/versions/<版>/`＋`/usr/local/bin/cursor-agent` symlink
    （読取専用でも `.running` マーカーはグレースフル劣化で動作する — 実測）。

## リスク（受け入れ）

- 週次リリースの CLI ドリフト（hooks/JSONL/ACP の公式契約依存で緩和＋live テスト一次検知）。
- 版ピン URL は非公開仕様（e2e-smoke で破壊を検知）。auto-update 無効化は**公式手段**に更新
  （`--disable-auto-update`＋channel:"static" — Track B で確定）。
- linux arm64: 配布資産の健全性は検証済（bundled node/native addon が AArch64/glibc）だが、
  実 arm64 ハードでの起動実行は未検証（本コンテナ x64。ECS/native 展開前に実機確認を条件化）。
- Docker 内 `CURSOR_API_KEY` 不調のフォーラム報告（自コンテナで要検証）。

## 結果

- Track A/A2/B/C 実装済み（2026-07-23）。Track C は auth.go の login start/poll/disconnect
  ＋両 routes.go 二重登録＋mcp_stdio.go/CP mcp.go の kind enum 総ざらい＋bridge kindLabel＋
  Console（union/SESSION_KINDS・registry descriptor 全 caps 明示・tokens.css/5 色クラスの
  cursor twin・AgentsTab CursorCard・settings.ts/agentModels.ts・i18n ja/en）。全テスト緑
  （cursor 12・agent+bridge 335・CP 222／Console typecheck・i18n:lint・vitest 392・vite build）。
- 残: Track D（API キー手動登録・使用量チップ・rtk hook seam・headless チャット・画像添付等）と
  arm64 実機起動、実フリート再ビルド後の実機目視。詳細・トラック分割・教訓反映表・プローブ一覧は
  [docs/40](../log/40-cursor-agent-kind.md)。
