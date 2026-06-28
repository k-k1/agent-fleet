# CHANGELOG（HANDOFF 時系列ログ）

`docs/HANDOFF.md` は「現在の正しい状態」を表す構造化リファレンス。
本書はそこへ至った**時系列の作業ログ**（1 行サマリ）。詳細・現状は HANDOFF の各テーマ節を見ること。
訂正が連鎖した項目は HANDOFF 側に最終結論のみ載る（途中経過の誤りは教訓として圧縮）。

## 2026-06-26
- Phase 1 MVP 完了（commit `dd2330e`）。Workspace Agent + Control Plane + 最小 Console、`/login` フルチェーン検証。
- Connections（git/claude）実装。Claude OAuth 実承認まで検証。

## 2026-06-27
- リポジトリ管理（clone/checkout/branch/status、clone-then-start）実装。
- Connections 拡充: GitHub Device Flow / Bitbucket Auth Code Grant の実 OAuth 検証。
- per-user Workspace 化 + AuthGateway（`AUTH=proxy`）実装・検証。コンテナ間ネットワーク分離（`af-net-<user>`）。
- 資格情報 at-rest 暗号化（`secrets.enc`、AES-256-GCM）。CP↔Agent 認証（`AGENT_TOKEN`）。
- shared 形態をライブ有効化（`AUTH=proxy`、`X-Forwarded-Email`）。MVP（Phase 2 完了条件）達成。
- Phase 3 着手: P3-1 DB 化（SQLite MetadataStore）/ P3-2 identity↔tenant 多対多 + テナントピッカー。
- P3-3 封筒暗号 + custodian 抽象 / P3-4 リソースクォータ / P3-5 段1（メンバー Console: git 操作 + shell）。
- P3-5 段2（ファイルブラウザ + 機微状態の home 外退避）。管理 UI（super_admin）。
- セッション表示名 `[AF] {repo} @MMDD-HHMM`（`--name`）。タイムゾーン per-user（既定 JST）。
- ディレクトリ trust 自動化（`hasTrustDialogAccepted` seed）。

## 2026-06-28
- Console 全面刷新（React+Vite）。Claude/環境設定 UI、ツールチェーン共有（JVM image 外）。
- セッション即死リグレッション修正（`CLAUDE_CONFIG_DIR` 配下の jsonl 走査）。ゾンビ reap（`--init`）。
- 続き1: claude 終了後のセッション復帰（per-session メタ永続、`alive:false` 一覧残存）。
- 続き2: 新規セッションモーダル刷新（shell 既定）。OS ユーザー `node`→`dev`。
- 続き2: セッション一覧刷新（× 廃止 / 停止状態 / 再開 / 2 行表示、4 秒ポーリング）。
- 続き3: セッションメタを永続 home へ（Stop→Start 跨ぎ）。DB ミラー（migration 0005）。セッション recreate。
- 続き4: 表示名 = claude `--name`。bridge-session の resume クラッシュ修正（`jsonlResumable`）。
- セッション進行状態の可視化（状態フック `session_status.go`、バッジ + 到着通知）。
- フォローアップ4点: Bitbucket repo/branch 列挙 / clone submodule / ファイル畳み込み + compact folders / ファイル種別アイコン。
- セッション管理（dir 消失で再開不可）/ Git LFS / ブランチ選択モーダル化。
- 続き5: cacerts（Temurin の dangling symlink）解決 / 端末コピペ / Python 3 同梱。
- 続き6: opencode をエージェント追加（`kind="opencode"`）。
- 続き7: opencode 設定からの認証（暗号保存 + env 注入）+ 状態プラグイン。`tmux -e` 伝播バグ修正（env 前置へ）。
- 続き8: 孤児セッション（meta 無し生 tmux）を一覧に出す（`session already running` の一因）。
- 続き9: opencode のスロット毎に独立セッション（`ses_…` 捕捉 → `--session`）。
- 続き10: claude 対話 TUI に env トークンが効かない（誤: 合成 creds 方式 → 続き11/12 で訂正）。
- 続き11: claude 認証を本物の `claude auth login --claudeai` に（誤: 合成 creds 撤去 → 続き12 で真因確定）。
- 続き12: claude ログイン画面の真因は `.claude.json` の `hasCompletedOnboarding` 未設定（最終結論）。
- 続き13: UI 調整 6 件（ポーリングちらつき / 起動ドロップダウン / repo→FILES / 一括削除 / FILES 自動更新 / アーカイブ復帰）。
- 続き14: Repos→ソース管理一本化 / 履歴クリックで commit diff（`/repos/{name}/show`）/ vim 同梱。
- 続き15: `session already running` の真因は tmux `-t` の前方一致 → target を全て exact 化（`exactT`）。
- 続き16: 接続中セッションのピン留め / アイコンを codicon 化 / アーカイブモーダル。
- 続き17: リポジトリのピン留め / ファイル種別を codeleaf 風カラー SVG に。
- 続き18: カラーテーマ（ダーク/ライト + 上部/左ペイン背景色）。
- 続き19: 表示設定のサーバー保存（端末間同期、`ui_prefs.go`）/ 管理設定を別モーダルに分離（`AdminDialog`）。
- 続き20: スマホ端末対応（監視＋軽操作）。`@media(max-width:760px)` に閉じ込め、左ペインをドロワー化（ハンバーガー/バックドロップ/選択で自動クローズ）・モーダル全画面・タッチターゲット拡大。端末に最小コントロールキー列（Esc/Tab/矢印/Ctrl-C/Enter, `TermKeys.jsx`）+ `visualViewport` refit + 1本指スワイプでスクロールバック。`sendInput` は PTY 直送（Gboard を不要に呼ばない）。
- 続き21: Workspace image にツールチェーン追加。Go（公式 tarball・`ARG GO_VERSION=1.26.4`・アーキ検出、`~/go` 永続）+ C/C++ 基盤（build-essential/pkg-config/python3-dev=cgo・node-gyp・wheel ソースビルド）+ jq/unzip/zip/wget/gnupg/htop/fd/bat。実ビルド + cgo 検証済。image 約1.0G→2.82GB。git-delta は bookworm 非収録で除外、sudo は隔離維持で非導入。
- サービスプレビュー（`/preview/<port>` 経路、commit `7649c64`）。CP `handlePreview`（`rtFor` 認証 + `Bearer` 付与 + `X-Forwarded-*`）→ Agent `/proxy/<port>`（ReverseProxy）→ コンテナ内 `127.0.0.1:<port>`。隔離不変。Console は WS バーのポート入力＋新タブ（`?tenant=` fallback）。HTTP のみ（WS/HMR は次段）。詳細 [reference/preview](reference/preview.md)、HANDOFF §6.10.9。
