# CHANGELOG（HANDOFF 時系列ログ）

`docs/HANDOFF.md` は「現在の正しい状態」を表す構造化リファレンス。
本書はそこへ至った**時系列の作業ログ**（1 行サマリ）。詳細・現状は HANDOFF の各テーマ節を見ること。
訂正が連鎖した項目は HANDOFF 側に最終結論のみ載る（途中経過の誤りは教訓として圧縮）。

> **本ログは 2026-07-15 で打ち切り。** 以降の変遷は git 履歴（develop）と
> [docs/README.md](README.md) の索引・各設計ドキュメント（docs/NN）を参照。

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
- サービスプレビュー（`/preview/<port>` 経路、commit `23975c8`）。CP `handlePreview`（`rtFor` 認証 + `Bearer` 付与 + `X-Forwarded-*`）→ Agent `/proxy/<port>`（ReverseProxy）→ コンテナ内 `127.0.0.1:<port>`。隔離不変。Console は WS バーのポート入力＋新タブ（`?tenant=` fallback）。HTTP のみ（WS/HMR は次段）。詳細は現行 [dev/05 §5.3](build/05-api.md)（当時参照した reference/preview と HANDOFF §6.10.9 は再編で廃止）。
## 2026-06-29
- 設定→接続で**認証アカウントを表示**。claude（`claude auth status` の email/plan）、codex（`auth.json` の `auth_mode` + id_token claims から email/plan、例 `…@gmail.com · plus`）。
- 接続を**「エージェント / git ホスティング」にカテゴリ分け**。GitHub/Bitbucket も実アカウント表示（`/user`・`/2.0/user`、store キャッシュ＝polled endpoint で都度 API を叩かない、`gitEntry.Login`/`bitbucketCreds.Account`）。
- git 接続に **ID（ハンドル）+ email** を表示（GitHub `/user`、Bitbucket `/2.0/user` + `/user/emails`、`gitEntry.Email`/`bitbucketCreds.Email` にキャッシュ）。例: github `k-k1 · k1.kami@gmail.com` / bitbucket `bb-user · dev@example.com`。
- 表示: アイコンセット選択を折り返しチップ化（スマホで見切れ解消、`ChipChoice`）。設定/管理モーダルのヘッダ余白拡大 + ✕ タップ域確保（スマホは `safe-area-inset-top`）。
- セッション一覧/ファイルツリーの UI 微修正: (1) 停止中セッション名を `--muted`→`--fg` opacity0.72 で可読に / (2) 接続中セッションは**先頭固定（hoist）を維持** + pin バッジを行の**右上に絶対配置**（セッション名は左寄せ固定。`.session-row` を `position:relative`、pin は `position:absolute`）/ (3) ファイルツリー選択色のハードコード（`#2a3a44`/`#2f5a6a`）を `--hover-bg`/`--active-bg` に＝ライトモードで暗いままを解消 / (4) **`.pane-head` に `z-index:3`**＝sticky なセクションヘッダ（SESSIONS 等）が sticky なピン行に覆われないように。
- ピン留め行の sticky 固定が効かない回帰を修正。pin 右上化で足した `.list>li.session-row{position:relative}` が `.session-row.pinned{position:sticky}` を**詳細度で上書き**し上部固定を無効化（少し下にズレる/SESSIONS の隙間から見える）。pin はピン留め行のみ＝sticky が包含ブロックなので relative 不要 → 削除して sticky 復活。
- codex resume が新規セッションになるバグ修正。codex のフックは claude 同様**入れ子スキーマ** `hooks.<E>=[{hooks=[{type,command}]}]` が必要（フラットはパースは通るが無音で発火しない）。フラットだとフック未発火→session_id 未捕捉→resume で id 無し→新規化。実機で発火・session_id 捕捉・resume を確認。
- Console 接続 UI 刷新: Claude を **OAuth 接続ボタン**化（クリックでサインインを別タブ自動オープン＋コード貼付、`window.open`）/ Codex・GitHub の認証コードを**クリックでコピー**（`CopyCode`）/ 接続中の **✕→「切断」** テキストボタン（`DisconnectButton`）。端末の「sign-in URL」コピー機能は廃止（`reconstructURL` 撤去）＝設定>接続で代替。
- Workspace「**作り直す**」を WS バーから **設定>環境の危険ゾーン**（警告ダイアログ付き、`EnvTab` の `WorkspaceDangerZone`）へ移設。WS バーは Start/Stop/更新/プレビューのみに。
- Repos 行を簡素化: **fetch / 🗑削除 / ブランチ切替を右ペイン（ソース管理ヘッダ）へ移設**し、**起動ボタンをブランチ位置（名前の右）**へ。削除後は端末へ戻る。
- 左ペインのドロップダウン（起動 / セッション⋯）が下のセクション（FILES 等）に隠れる問題を修正。flex item にスコープされる z-index を、メニュー展開中だけ `:has()` で当該 `.pane-section` を上位スタッキングへ引き上げて解消。
- SCM diff を**ファイル毎の折り畳み＋旧/新行番号ガター**（codeleaf 風、`splitDiffFiles`/`diffRows`/`FileDiff`）。スマホは `scmbody` を縦積みし変更/履歴を上部（最大38vh）＋ diff 全幅。
- **SESSIONS/REPOS のピン留めを廃止**（順序が入れ替わり使いづらいとの FB）。`pinFirst`/`listutil.js`・pin バッジ・sticky を撤去し、接続中/SCM 表示中を **選択ハイライト（`.active`）のみ**に。← 6/29 前段（57/58）の「ピン先頭固定＋バッジ＋sticky 回帰修正」系はこれで撤回。
- **Workspace 利用ガイドを全エージェントへ自動配置**（`workspace/workspace-notes.md` 単一ソース・英語＝やってはいけないこと/注意点）。claude=`/etc/claude-code/CLAUDE.md`（managed policy・毎セッション読込・除外不可、image 焼込）/ codex=`~/.codex/AGENTS.md` / opencode=`~/.config/opencode/AGENTS.md`（後二者は entrypoint が毎起動 `cp -f` で refresh）。CLAUDE_CONFIG_DIR がユーザーメモリ参照に追従する保証がないため Claude は managed policy を採用。
- **ビルドメモリ実害対策**: entrypoint が `~/.gradle/gradle.properties` を seed（無い時のみ。heap 768m / `daemon.idletimeout=120000`〔既定3h でデーモン居座り＝実害〕/ parallel 無効 / workers 2 / caching）。Node/JS は適正ヒープがビルド依存ゆえ強制キャップせずガイドに指針（OOM 時のみ `NODE_OPTIONS=--max-old-space-size`、テストランナー並列抑制、watcher 放置禁止）。
- Dockerfile レイヤ順最適化: 重い go toolchain/npm(claude/opencode/codex) を前段、entrypoint/notes/vendor 等の COPY を `USER dev` 直前へ集約（小修正で重レイヤを再実行しない）。`.dockerignore` に `!workspace-notes.md`（`**/*.md` 除外の例外）。
- イメージ再ビルド＋運用者コンテナ作り直し済み。`/etc/claude-code/CLAUDE.md`・`~/.codex/AGENTS.md`・`~/.config/opencode/AGENTS.md`・`~/.gradle/gradle.properties` のライブ配置を確認。

## 2026-06-28（続き・後から追記のため日付が前後）
- codex をエージェント追加（`kind="codex"`、OpenAI Codex CLI `@openai/codex` を image 焼き込み）。認証2経路（API キー=`codex login --with-api-key` stdin / サブスク=`codex login --device-auth` device flow、ともに codex 所有の `~/.codex/auth.json` を書く＝secrets.enc 不要）。状態は claude 同型フックを**起動時 `-c` 注入**（per-slot sid 埋め込み・`--dangerously-bypass-hook-trust`）で working/idle。resume はフック stdin の `session_id` 捕捉 →`codex resume <id>`。`~/.codex` を denylist。`codex_auth.go` 新規。実 CLI 0.142.3 で hooks 形式 / device-auth 出力 / login 経路を実検証（認証完了は要 OpenAI 資格）。詳細は当時の HANDOFF §6.10.4（現 HANDOFF には無し・dev/ 再編済）。

## 2026-06-29（続き）
- **ファイルビュアーに Marp スライドプレビュー追加**（`MarpView.jsx`＋`@marp-team/marp-core`、`FileView.jsx`/`lib/filemeta.js` の `isMarpDoc()`）。frontmatter `marp: true` の `.md` を本物の Marp スライドとして表示（スライド/プレビュー/ソースの3トグル・既定スライド）。Shadow DOM 隔離・遅延 import・ステッパー＋全画面（Fullscreen API）。**ハマり**: marp-core が `mathjax-full`(~43MB)/`katex` を静的 require → `math:false` でも素では Vite ビルドがミニファイ段でハング（>9分）。`math:false` 時に実行時アクセスなしを trap-proxy で検証し、`vite.config.js` の alias で `marp-math-stub.js` に差し替えバンドル除外（28s 復帰）。詳細は当時の HANDOFF ファイルビュアー節（現 HANDOFF には無し・dev/ 再編済）。※**未目視検証**（console は headless 不可）＝要ブラウザ確認。

## 2026-06-30
- **セッションクォータを同時稼働数で数えるよう修正**（`control-plane/manager.go countSessions`）。Agent の `/sessions` は停止中（再開可能・TTL 7d）も返すため、稼働中が上限未満でも `session limit reached` で新規作成が弾かれていた。`alive==true` のみカウントへ。
- **API エラー文言を i18n 対応の形へ**: サーバは言語非依存の `code`（`quota_sessions`）＋開発者向け英語フォールバックのみ返し、表示文言は Console の `errText()`（`src/api.js`）が `code` から組み立て（未知 code はサーバ message にフォールバック）。`ReposSection`/`NewSessionModal` を移行。将来のロケール辞書はこの map を差し替えるだけ。
- **dev 反映の軽量経路を整備**: `deploy/local/restart-cp.sh` 追加（Workspace image を再ビルドせず console+CP を build → `af-cp` をその場再起動 → `/healthz` 検証。`SKIP_CONSOLE=1` で CP だけ）。§3 の OOM リスクを避ける常用手順。反映早見表（HANDOFF §2）も更新し、稼働中 Workspace の image 入れ替えは**利用者の Stop→Start**と明記。

## 2026-07-01
- **稼働中セッションに「停止する」を追加**（⋯メニュー）。Agent `POST /sessions/{name}/halt`（`handleHaltSession`）= tmux kill + **meta 保持**で停止中（再開可能）へ＝端末 quit と同等。既存 stop（meta 破棄＝一覧から削除）/ archive（隠す）と別の3つ目の動詞。CP は `/api/sessions/{name}/halt` を proxy 追加。停止でクォータ枠も解放（同時稼働カウントは alive のみ）。※ Agent 変更ゆえ稼働中 Workspace は **Stop→Start** で反映。
- **WS バーの Start/Stop を1ボタン化**（`WsBar.jsx` `ws-toggle`、状態でラベル/動作が切替・遷移中は無効・固定幅で色分け green/amber）。隣の「状態を更新」ボタンを**撤去**し、代わりに `wsState` を**4秒ポールで自動同期**（`state.jsx`。遷移中=`…` 接尾辞・タブ非表示時はスキップ、同値 setState は no-op）＝管理者 Stop / OOM 死など外部変化も自前で追従。
- **MCP PAT 発行後に `.mcp.json` テンプレートを表示**（`TokensTab.jsx`）。発行パネルにトークンそのままに加え、Claude Code 用の `.mcp.json`（`mcpServers.agent-fleet` = `type:http` / `url`=`rel("mcp")` 絶対URL / `headers.Authorization: Bearer <token>`）を整形表示＋ワンクリックコピー。PAT が membership を内包するため tenant ヘッダ不要。一度きり表示の secret を握っている間のみ生成。
- **単一ペインでも閉じるボタンを表示**（`PaneHost.jsx`/`state.jsx`）。従来 `canClose={total>1}` で1枚時は非表示だった。中身のある単一ペイン（セッション/ファイル/SCM、またはセッション表示中の端末）に閉じるボタンを出し、`closePane` の「最後の1枚は no-op」を**空端末へリセット**（`resetToTerminal`）に変更＝中身をクリア。空端末1枚（base 状態）のみ無効（WsBar「全ペインを閉じる」と同じ判定 `isBlankSingle`）。
- **WS 状態ラベルを平易化**（`WsBar.jsx` `wsLabel()`）。CP は docker 由来の生 state（`runtime.go state()` = running/stopped/**none**）を返す。Stop は `docker rm -f`＝コンテナ削除なので**通常の停止は `none`**（データは bind mount で保持・Start で再作成）、`stopped` はコンテナ自走終了（クラッシュ/OOM）時のみ。生語が UI に漏れ「none＝意味不明」だったのを `none/stopped→停止`・`running→稼働中`・transient も和訳し、生 state は tooltip に退避。表示層のみ（状態機械の値・ロジックは不変）。
- **ドキュメント整合（Phase 3 区切り）**: 実体（Console 刷新済・MCP 段1=member ライブ）に対し roadmap/README 索引/decision 0006 が「P3-6 未着手」のままだったのを修正。Phase 3 は「機能構築チャプター（P3-1〜5 + Console 刷新 + P3-6 段1）」を一旦区切り、配布工程（P3-7〜10）が残＝roadmap の完了ゲート（第2デプロイ E2E）は未達ゆえマイルストーンは進行中のまま。
- **P3-6 admin MCP ツール（read/write）実装・ライブ E2E green**。`/mcp` に admin ツールを追加: read=`list_workspaces`/`get_usage`/`list_sessions`、write=`stop_workspace`/`stop_session`/`set_user_quota`。ライブ検証（運用者デプロイ・CP 再起動後、DB 直挿しの一時 admin PAT で）= super_admin 全10ツール+host stats／tenant_admin は admin 可視・host stats 無し／plain member は member 4ツールのみ・admin は 401／write が `audit_log`(actor_kind=mcp) 記録、を確認。検証 PAT は後始末で削除。※検証中 `stop_workspace` のターゲット選定ミスで naoki の稼働 workspace を 1 件停止（データは bind mount で保持・セッション meta 永続ゆえ再接続で復帰）。`mcp.go` に `mcpTool.admin` + `adminPrincipal`（PAT→**live role 再解決**で super_admin / その PAT tenant の tenant_admin を gate、tenant はトークン固定）。read は既存 admin サービス層、write は `stopWorkspaceByMembership`/Agent `/halt`/`PutUserLimit` を薄く wrap。write は **`AuditLog`（`actor_kind=mcp`）へ記録**——その土台として **migration 0007 `audit_log` + store `InsertAudit`/`ListAuditByTenant`** を新設（従来 `AuditLog` はスキーマ未作成だった）。ハマり: 監査マイグレーションのコメント内 `;` で migrator の `Split(";")` がコメントを途中分割し SQL 構文エラー → コメントから `;` を除去。dangerous 段（rotate_key/stop_all_idle）は土台（鍵ローテ・idle 検出・tail_audit）待ちで残。

## 2026-07-15
- **docs/27＋ADR 0015 起草: エージェント制御の Managed Driver 化**。Codex TUI のモデル勝手切替バグ（暫定対処 `9414525`）を発端に、3 エージェント横断の設計を確定: Codex/OpenCode は共有 runtime（app-server / serve）＋構造化 RPC の managed を既定・ユーザー選択の CLI ルート常設、Claude は現状 TUI 維持（Session Manager 案は凍結・付録温存）。既存 read 層温存＋Driver/RuntimeSupervisor 増築、記録は「read=ネイティブストア正本／live=イベント／write=API」三層、認証・config 反映は generation＋drain、着手順 P1（Codex 観測拡張）→P1.5（Console managed UI）→P2（OpenCode）→P3（Codex 既定化）。並行設計セッション sol×fable の統合。実装未着手。
- **docs/27 P1 実装: Codex 観測を 5 イベントへ拡張＋observer の thread アタッチ**（`09c6417`＋レビュー修正 `61bfa98`）。account/rateLimits/updated・model/rerouted・thread/settings/updated・warning・thread/status/changed を構造化ログ化し、rate limits は `/codex/usage` が rollout 読みと鮮度比較。実装中の最重要発見 = **thread スコープ通知は「そのスレッドをロードした接続」にしか配送されない**（0.144.3/0.144.4 実測）→ 既存の圧縮検知（`fa7e47d`）は本番不発だった。observer が `thread/resume` で read-only アタッチする `codexObserver` を追加（rollout 不変を sha256 実測）。reroute に rate limit 起因は存在せず発端バグは TUI ナッジでほぼ確定（docs/27 §12.1）。レビュー修正 = rate limits push のスパースマージ・notLoaded で requested を forget。
- **docs/27 P1.5 実装: Console managed セッション UI の受け皿と Driver 層 IF**（`af4a17a`）。`internal/agents/driver.go`（Driver/ThreadHandle/Capabilities/TurnInput/Interaction/TurnState）、意味論エンドポイント `POST /sessions/{name}/turn`（start/steer/interrupt）・`/respond`（tui は tmux 経路へ委譲・managed は P2 の driver 登録まで 501）、driver 軸のワイヤ化（Meta.Driver→Session.driver、作成 API は managed を P2 まで拒否）、Console は送信 start/steer・停止 interrupt・質問の構造化回答 onRespond・paneless 描画（TerminalView 非マウント/トグル非表示）＋旧 Agent への /input フォールバック。pane 前提機能の棚卸し 12 項目と置き換え設計は docs/27 §10.2。CP 中継 `sessionWire` に driver を追加（無い field は silently drop される中継——なお color/context/exitReason 系は現在も drop されており docs/26 の exit chip が CP 経由で欠ける疑い＝別途要修正）。
- **CP `sessionWire` 中継の silent drop 修正**（`9b67eb6`）: title/color/context/branch 系/exitReason 系を中継 struct へ追加（回帰テスト付き）。docs/26 の exit chip・ContextBar・SSM 背景色が CP 経由で復活。
- **docs/27 P2 実装: OpenCode managed 化 — Driver 型の初のフルスタック実装**。`internal/agents/opencode/serve.go`（RuntimeSupervisor: 共有 `opencode serve` の起動/adopt・/global/event SSE 監視・generation＋drain・graceful Shutdown・daemon 死の exit 記録〔cmd.Wait＋cgroup OOM 帰属→per-session PersistExit〕→自動 reconcile）＋`driver.go`（ThreadHandle: Send/Steer/Interrupt/UpdateSettings/Respond/Events/Snapshot、turn 状態機械＋ClientMessageID 台帳、Interaction(question)、driver 内キュー steer、v1 file part 添付、permission.asked 自動 allow）。実測（docs/27 §12.2）が実装を規定: **v1/v2 二重 API**（v2 は別ストアで read 層不可視→v1 blocking /message 駆動）、**message id 辞書順依存**（クライアント採番 messageID 不可→serve 採番＋driver 台帳冪等化）、**directory スコープ**（全呼び出しに ?directory 併送・SSE は /global/event）。main 側 = driverOf 登録・作成/開始/停止/halt/archive/recreate/fork/list の driver 分岐・`sessionAlive`・`POST /sessions/{name}/settings`（動的 mode/model/effort、§9.4-3）・exitReason 判定を internal/status へ移設共用。Console = 起動 UI ドライバ選択（opencode 新規既定 managed・CLI はメモリコスト表示・旧 Agent へ tui フォールバック）・managed の添付ワイヤ化/launch seed 直送/plan トグル→/settings。E2E 実機（実バイナリ＋実 serve＋zero-auth 実 turn）で 作成→初回プロンプト→question(id 付き)→/respond→steer キュー→interrupt→halt/start→daemon SIGKILL→exit 記録(137)→reconcile(gen++) を通し検証。CLI ルートの serve アタッチ化は sid 捕捉/--model の 2 課題で P3 送り（§12.2-11）。
