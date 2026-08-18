# 02. Console（React + Vite + zustand）

> 正: コード（本書は地図と設計意図）/ 主な更新トリガ: Console のアーキ・IA・ビルド制約の変更 / 最終確認: 2026-07

## 2.1 スタックと設計原則

React 19 + Vite 6 + TypeScript + zustand 5 の SPA。CP が `console/dist` を静的配信し（[05 §5.4](05-api-contracts.md)）、
バックエンドとは `/api` REST・SSE・`/ws/terminal`・`/ws/browser` で会話する。2026-07 に機能パリティを保った全面リビルド
（[../22-console-rebuild.md](../history/22-console-rebuild.md)）で God-context 構造を廃した — 経緯と決定は
[decisions/0011](../decisions/0011-console-rebuild.md)。設計原則:

- **ドメイン別 zustand ストア + selector 購読**。単一 Context・`bump*()` カウンタ・ref ミラーは全廃（§2.3）。
- **レイアウト演算は純関数**（`console/src/layout/`）。副作用（永続・履歴・xterm）はストアとサービスが所有（§2.4）。
- **feature 単位の凝集**: endpoints 呼び出し・状態・UI・CSS を `features/<x>/` に同居。CSS は co-located プレーン CSS
  （CSS Modules 不使用、クラス接頭辞規約で衝突回避）。
- **StrictMode 耐性**: モジュールレベル可変シングルトン・wired-once フラグ禁止。`wire*()` は必ず unsubscribe を返す。
- **全 URL は `document.baseURI` 相対**（`rel()`）。絶対パス禁止（path-strip プロキシ配下で動くため）。

## 2.2 ディレクトリ責務（console/src/）

| ディレクトリ | 責務 |
|-------------|------|
| `app/` | シェル（App）と 2 段バー（TopBar / WsBar）・viewport。boot 順（tenant 解決 → per-tenant レイアウト復元 → ui-prefs → ポーリング開始）、履歴・drawer・通知の一括結線を所有 |
| `core/api/` | `client.ts` = fetch ラップの単一点（§2.3）。各 feature は `features/*/api.ts` で自分のスライスだけ再輸出 |
| `core/store/` | 基盤ストア: tenant（選択・membership）と workspace（状態機械 + start/stop）|
| `layout/` | ペインレイアウトの純関数エンジン（types / ops / migrate）+ layout ストア。vitest 対象（§2.4）|
| `terminal/` | `term.ts`（xterm 実装知識の塊）+ `service.ts`（TermService = xterm への唯一の入口）|
| `ui/` | プリミティブ: Button / Modal / Section / Icon / FileIcon / Toast / Confirm / EmptyState / Sparkline 等 |
| `features/*` | 機能 19 個（下表）|
| `styles/` | `tokens.css`（テーマ変数の唯一の置き場）+ `base.css`（リセット）|
| `agents/` | `registry.ts` = エージェント kind の単一真実源（§2.4）|
| `lib/` | 純ロジックと小 hook: gitgraph（レーン DAG）・fileicons/filemeta・termcolor・project グルーピング・settings（ui-prefs 同期）等。テスト可能な関数の置き場 |
| `types/` | 横断ドメイン型（session / layout / chat / memo / assistant）|

| feature | 役割（1 行）|
|---------|------|
| `panes` | PaneHost（flat-absolute 描画・drag swap・drop-to-split）・Pane・左上 LayoutMap |
| `terminal` | TerminalView（PTY 常駐・未 attach ブランドアート）・モバイル TermKeys・OnboardingCard |
| `browser` | BrowserPane（canvas/toolbar/IME）・paneId keyed BrowserController/Registry・wire v1変換 |
| `sessions` | セッション行・作成/改名/アーカイブ等のモーダル群・アクション hook・4 秒ポーリングストア・状態変化のブラウザ通知 |
| `repos` | clone/起動/ブランチ系モーダル・RepoPicker/DirPicker・repo ストア（60 秒ポーリング）|
| `project` | 左ペインの working-copy ツリー: base clone + worktree をプロジェクト単位に束ね、ノード配下にセッションとファイルをネスト。repo 外セッションの受け皿も持つ |
| `files` | ファイルツリーの共有状態と更新シグナル（実 UI は project 側の ProjectFiles）|
| `scm` | SourceControl / Changes / CommitDetail / WorkingDiff / CommitGraph / GitDiff の各ビュー |
| `viewer` | File / Code / Markdown（mermaid）/ Marp / Image / Diff / Doc ビューア |
| `editor` | CodeMirror ベースのファイル編集: dirty 管理（DirtyGuardHost）・外部変更追従・AI 修正候補（suggest）の取得と適用 |
| `mirror` | MirrorView（セッション transcript のチャットミラー）・ContextBar（コンテキスト残量警告）|
| `chat` | アシスタントチャット（headless CLI 会話・SSE ストリーミング）と左ペインのセクション |
| `memo` | メモキュー: セクション・AI 整理モーダル・選択テキスト送信 |
| `schedules` | 定時実行の左ペインセクション + 詳細モーダル（一覧・有効切替・即時実行・実行履歴。作成/編集はオペレーター会話側）|
| `keys` | キーボード操作体系: capture-phase dispatcher・コマンドパレット・WhichKey / CheatSheet・キー再割当ストア |
| `notifications` | 通知センター: 未読/既読管理・トーストログ・音声通知トグル |
| `usage` | 機能別トークン使用量ダッシュボード（UsageView。設定モーダルの「使用量」タブが薄いラッパとして表示）|
| `auth` | ログインセッション切れ（401 / 端末ソケット断）検出時の再ログインモーダル（AuthExpiredModal）|
| `settings` | SettingsDialog（3 グループ左レール × 17 タブ、§2.5）・AdminDialog・接続状態ポーリング |

## 2.3 状態管理とサーバ同期

- ストア分割: tenant / workspace / layout / sessions / repos / files / chat / memo / settings(UI)。
  selector 購読により「4 秒ポーリングで全画面再レンダー」を構造的に防ぐ。React 外
  （TermService・fetch 層）からは `getState()/setState()` で連携する。
- ストア間連携は subscribe ベース。例: シェルの wireWorkspaceRefresh が workspace の
  stopped↔running **遷移エッジ**（starting 等の未確定状態は無視）で repos/sessions/files/chat を refresh。
- ポーリング周期: workspace 4 秒 / sessions 4 秒 / repos 60 秒 / ワークスペース操作バーのリソース統計 4 秒。
  workspace の状態は CP の `running/starting/stopped/none` + `unknown`（fetch 失敗）で、
  末尾 `…` は楽観的 in-flight の印（ボタンとポーラーは busy として手を出さない）。
- `core/api/client.ts` が `window.fetch` をラップし、全リクエストに `X-AF-Tenant` を注入
  （WS・新タブ・ダウンロードは `?tenant=` query fallback — [05 §5.4](05-api-contracts.md)）。
  401 は login ランディングへ 1 回だけリダイレクト。エラーコード→和文の `errText`、
  SSE / multipart / download ヘルパもここに集約。非同期操作は同期 + ポーリングで運用（job キュー無し）。

## 2.4 Pane / レイアウトと TermService

- Layout = 最大 4 カラム × 各 1–2 ペイン。`Pane.content` は判別 union
  （terminal / browser / file / scm / changes / commit / wtdiff / doc / diff / chat）。`session` は content でなく
  **ペインレベル**に持つ: ビューを切り替えても PTY ソケットとスクロールバックは hidden のまま温存され、
  端末ビューへ戻すと同じセッションが現れる。
- **paneId 契約（ハード不変条件）**: pane の id ＝ ターミナルの同一性。xterm インスタンス・WebSocket・
  DOM ノードは paneId で keyed。swap / drop-split は同一 id のままペインを移動し、再採番・複製は禁止
  （新 id は新しい xterm + WebGL コンテキストを作り、移動した端末が白紙になる）。`layout/ops.ts` の
  純関数と vitest がこれを強制する。
- `layout/ops.ts` は `Layout in → Layout out` の純関数（no-op は入力を参照のまま返し、呼び手が
  `next === cur` で commit をスキップできる）。layout ストアの `commit()` が**唯一の変更経路**で、
  state-only `pushState`（URL 不変）と per-tenant の localStorage 永続（旧形式は読み込み時 migration）を行う。
- **タブの表示順は MRU**（タブ付きグリッド）: `View.lastUsedAt` は LRU 追い出し（`MAX_TABS`）だけでなく
  **「表示中のタブが抜けたあと何を出すか」**の順序でもある。閉じる / 別マスへ移す / 切り離すのいずれでも、
  残りのうち**最後に表示していたタブ**を選ぶ（ミラーからファイルを開いて閉じたら元のミラーへ戻る）。
  隣のタブ（右→左）は、`lastUsedAt` を持たない古い永続レイアウトのフォールバックとしてだけ残る。
  同一ミリ秒の 2 度の touch が同点にならないよう、スタンプはページセッション内で厳密単調に採る。
- **TermService**（`terminal/service.ts`）が xterm への唯一の入口。layout ストア購読 1 本で
  「レイアウトから消えた pane の端末を dispose」する reconcile を回す。`term.ts` の中身は実戦で獲得した
  ドメイン知識の塊で不可侵: ハートビート（text フレーム＝帯域外制御、binary＝PTY 出力）によるゾンビ
  ソケット検出、WebGL 描画と context-loss 復旧、フォーカス時 Keyboard Lock（全画面時にブラウザキーを
  端末へ）、左ドラッグ選択で自動コピーするクリップボード統合、ソフトキーボード追従（visualViewport fit）。
  DOM コンテナは常駐（hidden 切替のみ、re-parent しない = PaneHost の flat-absolute 戦略）。
- **BrowserRegistry**（`features/browser/service.ts`）もpaneId keyedでPage/socket/canvasを所有する。
  layoutに永続化するのは`{kind:"browser", port, path}`だけで、ephemeralなbrowserIdは保存しない。
  非表示は`visibility=false`、60秒後にPageを破棄し、再表示・reload・Workspace再起動時はport/pathから再生成する。
- `agents/registry.ts` = kind（claude / codex / cursor / agy / copilot / kiro / opencode / shell / ssm の 9 種）の**単一真実源**。
  kind ごとに descriptor（表示・availability 述語・capability set: chat / transcript / model / fork /
  planMode / ephemeral 等）を 1 個持ち、UI は capability を見て分岐する。エージェント追加 = descriptor 追加。
- **表示名の 3 段体系**（`id`/`cssClass`/`short`/`icon` は内部識別子で不変・小文字）: `short`（2字 cc/cx/cu/ag/cp/ki/oc）＝
  狭所バッジ / `label`（コンパクト proper 名 Claude・Codex・Cursor・Copilot・Kiro・OpenCode・Antigravity）＝pane ヘッダ・セッション行 /
  `displayName`（フル製品名 **Claude Code**・**GitHub Copilot**・他は label と同値）＝起動カード・設定カード。表示コードは
  `lib/sessionkind.ts` の `kindShort`/`kindLabel`/`kindDisplayName`（=`displayName || label`）経由で書き、生の label 直読み・
  名称ハードコードは避ける（`BADGE_SHORT` も registry の `short` から導出）。

## 2.5 IA（情報設計）

- **2 段バー**: 画面最上部（アプリ名・テナント picker〔単一所属時は非表示〕・外観ポップオーバー・アカウント
  メニュー・設定・管理〔super_admin のみ〕）+ ワークスペース操作（workspace 状態と Start⇄Stop・リソースチップ +
  Sparkline・ポートプレビュー〔`/preview/{port}` を新タブで〕・各エージェントの使用量チップ（claude/codex の
  サブスク枠 5h/週次、copilot のアカウントクレジット残量、agy のクォータ残量%。各 popover にプランと利用アカウントも
  表示）・分割操作）。
- **左ペイン**: LayoutMap + 常駐 3 セクション（アシスタント / メモキュー / プロジェクトツリー）+
  repo 外セッションの受け皿（無ければ非表示）。フラットな Sessions / Repos / Files セクションは
  プロジェクトツリーに統合された（project-first IA）。
- **メイン**: PaneHost。アクティブペインの content に応じて端末 / ビューア / SCM / チャットが切り替わる。
- **履歴ナビ**: URL は変えず `history.state` にレイアウトを push（path-strip プロキシ配下で URL パスは
  使えない）。戻る / 進むでレイアウト・設定/管理モーダル・スマホ drawer が復元される。スマホは左ペインを
  オフキャンバス drawer 化し、`{drawer:true}` の履歴エントリで「戻る＝drawer を閉じる/再び開く」を実現
  （端末の beforeunload ガードを誤爆させない）。エッジスワイプで開閉。
- **スマホの横スワイプ＝稼働中セッションのローテート**: drawer が閉じている間の ← は次、→ は前の
  稼働中（alive）セッションをアクティブペインに開く（`features/sessions/rotate.ts` が選択規則、
  `open.ts` の `rotateRunningSession` が副作用）。順序は `GET /api/sessions` のまま（CreatedAt 降順）、
  絞り込みは作業グループに従う＝左ペインで見えている集合と一致する。開き方は行クリックと同じ規則
  （chat 可なら mirror）。**左端始まりの → は drawer が優先**（判定順で先に 50px の drawer 分岐が
  確定し、ローテートの 70px には届かない）。drawer が開いていれば従来どおり「閉じる」が優先。起点が
  横操作を持つ面（ブラウザペイン `.browser-stage` / 入力欄・contenteditable / 横スクロール域 /
  `[data-no-swipe]`）なら見送る（`app/swipeGuard.ts`）。閾値はレール開閉の 50px に対し 70px。
- **設定モーダル**: 3 グループの左レール × 17 タブ（旧 6 タブの単段バーはスケールせず再編。
  モバイルはレール→内容の 2 段ドリルダウン）。**個人設定**＝表示 / キー操作 / 読み上げ / 通知 /
  アシスタント、**接続**＝エージェント（各 kind の接続・RTK 等）/ Gitホスティング / 運用・監視 /
  チャット連携 / MCP サーバー / MCPトークン（PAT 発行・失効）、**ワークスペース**＝使用量 /
  エージェントメモリ / ツールチェーン / AWS SSM / 内部リポジトリ / 危険な操作（Workspace 作り直し等）。
  管理機能は SettingsDialog に混ぜず **AdminDialog に分離**（TopBar の shield から、super_admin のみ）。

## 2.6 表示システム

- **テーマ**: `styles/tokens.css` が変数の唯一の置き場（`:root` dark 既定 + `[data-theme=light]` 上書き +
  region 変数 `--topbar-bg` / `--leftpane-bg` 等）。`lib/settings.ts` の `applyTheme()` が
  `<html data-theme>` と region 変数を書き込み、SURFACE_COLORS は per-theme tint（ライトで暗色バー＝
  文字潰れを回避）。highlight.js は `--hl-*` 変数でテーマ追従。**既知の限界**: xterm はライトテーマ
  未対応（ライト選択時も端末は暗いまま）。
- **エージェント kind 色**: `tokens.css` の `--kind-*`（claude/codex/cursor/agy/copilot/kiro/opencode/shell/ssm、`:root`=dark・
  `[data-theme=light]` で暗色版）が**唯一の hue 源**。使用側（kind-tag・sess-kic・LayoutMap・起動 seg アイコン・
  設定バッジ）は `var(--kind-*)`＋tint は `color-mix(… N%, transparent)` で描画し、各 CSS に色 hex を直書きしない。
  **opencode はライトスレートグレー（#aab4be / light #6e7781）**＝copilot チャコール（#7d8590 / light #30363d）・
  kiro 紫との分離のため（紫は copilot→kiro が継承。docs/43 §4-1）。
- **アイコンの役割分担**: クローム＝codicon 単色（currentColor 追従）/ ファイル種別＝カラー SVG
  （`lib/fileicons` の ext→typeKey 解決 + `ui/FileIcon`）。
- **ui-prefs**: 表示設定（テーマ・フォント・アイコンセット等）は per-user でサーバー保存
  （`GET/PUT /api/env/ui-prefs`）。localStorage を即時キャッシュにしつつ 600ms debounce で PUT、
  boot 時に `hydrateUIPrefs()` が GET して **server-wins** でマージ（不達時は localStorage で動作）＝
  別ブラウザ・別端末でも設定が追従する。
- **スマホ対応**: 方針は「監視 + 軽操作」。全分岐を `@media (max-width:760px)` に閉じ込め、
  デスクトップ側の DOM / CSS は不変に保つ。drawer・全画面モーダル・TermKeys（最小キー列、WS 直送で
  IME を呼ばない）・safe-area 対応がこの中に閉じる。

## 2.7 ビルドとハード制約

- `vite build`（`npm run build` / dev は `vite build --watch` → リロード反映、CP 再起動不要。[10](10-development.md)）。
  mermaid / marp が heap を食うため `NODE_OPTIONS=--max-old-space-size` を**コマンド単位**で付与
  （package.json scripts は 4096、`deploy/local/` の再ビルドは 3072）。sourcemap は無効（生成で heap 溢れの前科）。
- mermaid / marp-core は**遅延 import チャンク**（メインバンドルから分離。開いた時に初めて読み込む）。
- **marp-core の罠**: `math:false` で使っていても mathjax-full(~43MB) / katex を**静的 require**するため、
  素のままだと本番ビルドが minify 段でハングする。`vite.config.js` の `resolve.alias` で
  `marp-math-stub.js` に差し替えてバンドル除外している（据置のハード制約。剥がすとビルドが死ぬ）。
- CP は `console/dist` を `Cache-Control: no-store` で配信＝デプロイ即反映（[05 §5.4](05-api-contracts.md)）。
  旧 `/agent-fleet` プレフィクスは廃止（ルート配信・互換リダイレクトのみ）。
- **テスト**: vitest（node 環境・`maxWorkers=2` — 共有ホストのメモリ規律）で純ロジックのみ
  （layout/ops・lib の純関数・ストア遷移）。DOM・ビジュアルはブラウザ目視が正（[10](10-development.md)）。

## 2.8 残債（動作影響なし・随時解消）

[../22-console-rebuild.md](../history/22-console-rebuild.md) のステータス欄が正。要点:

- MirrorView 解体（transcript パーサ純関数化 + ブロック分解）— 忠実移植のまま。CommitGraph / GitDiff / ビュアー群も verbatim。
- 抽出 CSS（viewer / mirror / chat / settings 等）の未使用セレクタ刈り。
- legacy button compat（`:where` スコープ）の ui/Button 化。
- レイアウト永続キーは `af.layout2.<slug>` のまま（旧キーは migration 読み取り元として残読）。
- 非 chat 種の起動プロンプト送信は暫定実装（TUI 生存待ちの sendPromptWhenAlive）。
