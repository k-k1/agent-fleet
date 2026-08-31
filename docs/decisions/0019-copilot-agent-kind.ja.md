# 0019. `kind=copilot`（GitHub Copilot CLI）を第5のエージェント種別として追加する

[English](0019-copilot-agent-kind.md) | 日本語

- 状態: **採用・実装済み**（2026-07-21）。実装計画・全実測は [docs/36](../log/36-copilot-agent-kind.md)。
- 関連: [0008](0008-antigravity-cli-agent-kind.ja.md)（agy — 新種別追加の先例）、
  [0015](0015-agent-managed-driver.ja.md)（managed driver 抽象 — 本件は第3実装）。

## 背景

GitHub Copilot CLI（npm `@github/copilot`、2026-02 GA）は `-p --output-format json`
（JSONL イベント）、`--acp`（Agent Client Protocol = JSON-RPC over stdio）、
`--session-id` 外部採番、`session/load` クロスプロセス resume を備え、既存 4 種の
どれよりもオーケストレーター前提の口が揃っている（全て実バイナリ v1.0.73 実測 —
docs/36 実測記録）。認証は GitHub トークン（gh CLI アプリの OAuth を公式サポート）
で、本フリートの gh 透過認証がそのまま通ることを実測確認した。

## 決定

1. **v1 から Terminal (CLI) と Managed の両対応**とする（agy と異なり構造化出力が
   最初からあるため）。managed が既定。
2. **managed runtime は per-session child の `copilot --acp`**（stdio JSON-RPC）。
   codex（共有 daemon/WS）・opencode（共有 serve/HTTP+SSE）と異なる第3の形。
   理由: ACP に per-session のモデル指定が無く（configOptions は mode/allow_all のみ）、
   子プロセス毎の `--model`/`--effort` フラグが唯一の確実な経路。副次効果として
   exit/OOM 記録が子の `cmd.Wait()` で per-session に正確化される。メモリは TUI pane
   と同等（どちらも copilot 1 プロセス/セッション）。
3. **read 正本は `$COPILOT_HOME/session-state/<sid>/events.jsonl`**（TUI/-p/ACP 全経路
   で同一形式・ライブ追記 — 実測）。transcript・live 状態分類（working/question/idle）
   を両ドライバ共通実装にし、TUI 文字列に依存しない（false-idle 教訓）。
4. **セッション UUID は AF 側で外部採番**（`--session-id`、RFC4122 v4）。agy で
   苦労した resume ID 捕獲（0008/docs/32 202e439）は構造的に発生しない。
5. **認証は GitHub 連携相乗り**: 専用の Connections フローを作らない。
   `copilot.connected` は git プロバイダ GitHub 連携の導出値で、**GitHub 連携が先**。
   TUI は copilot 自身の gh フォールバック（ambient・実測で動作）、managed 子と
   モデルプローブは `COPILOT_GITHUB_TOKEN="$(gh auth token)"` を明示注入（隔離環境で
   ambient が切れる事故を実測 — 明示注入が正）。Copilot サブスクの有無は連携時に
   検査せず、初回ターンの CLI エラーとして表面化させる。
6. **モデルカタログはプラン連動のライブ取得**: CLI/ACP に列挙口が無く、可否は
   プラン依存（Copilot Free は Auto のみ — 実測）。使い捨て COPILOT_HOME で TUI を
   PTY 起動し `/model` ピッカーをスクレイプ（agents.Flow・10 分キャッシュ・
   stale-if-error）。Free 系バナー → 空 = ピッカーは既定（auto）のみ。
7. **権限は防御実装**: fleet 既定は `--allow-all`（plan 起動時のみ外す）だが、
   `session/request_permission` は常に Interaction(question) へ写像し /respond で
   構造化回答（「UI に出ないから発生しない」を信用しない — agy df996e4 教訓）。
8. **セッションの GitHub 同期・リモート操縦は既定オフ**（`--no-remote
   --no-remote-export`）— フリート外への会話流出と二重操縦を防ぐ。
9. Steer は driver 内キュー（ACP に mid-turn 注入なし）、Fork なし、
   DynamicMode のみ動的（`session/set_mode`）。ask_user は ACP では平文＋end_turn に
   落ちる（実測）ため質問カードは permission 用。

## 結果

- 実装は docs/36 のトラック分割どおり完了（agent 本体＋driver＋配備＋CP/Console）。
  agy 統合の全 46 コミット・指摘 23 分類の反映表を docs/36 に記録。
- 検証: Go 451 / CP 150 / console 355 全緑＋**実 CLI 契約テスト**（AF_COPILOT_LIVE=1:
  spawn→session/new→turn 完走→events.jsonl→子 kill→session/load resume→文脈保持、
  モデルプローブ含む）通過。残は実フリート再ビルド後の実機目視と docs/36 記載の
  Track D（rtk・使用量チップ・チャット headless バックエンド・画像添付等）。
- 制約として受け入れたもの: 週次リリースの CLI ドリフト（events.jsonl/ACP 依存で
  緩和＋live テストが一次検知）、classic PAT（`ghp_`）非対応（フリートの GitHub
  OAuth は `gho_` を作るため通常は非該当）、TUI の plan モードチップ非表示
  （フッタにモード表示が無く検出不能）。
