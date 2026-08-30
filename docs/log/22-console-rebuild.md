# 22. Console リビルド（機能そのまま・React + Vite 続投）

> ℹ️ **文書番号の注記**: 歴史的経緯で history/ の番号は重複している — **#22** は本書のほか
> [chat-opencode-codex](chat-opencode-codex.md)・[agent-cli-self-update](agent-cli-self-update.md)、
> **#21** は [21-memo-queue](21-memo-queue.md) と [fork-from-chat](fork-from-chat.md)、
> **#19** は [19-assistant-chat](19-assistant-chat.md) と [p3-9-idle-stop](p3-9-idle-stop.md)。
> 他文書から「docs/22」（Console 全面リビルド、ADR0011）として参照されるのは**本書**。

増改築で混沌化した Console（`console/src`, 約 31.5k 行）を、**機能パリティを保ったまま**
新アーキテクチャ上に作り直す。フレームワークは変更しない（React 19 + Vite + TS）。
バックエンド（CP / Agent）は一切触らない。

> ステータス: **P0〜P8 完了 — スワップ済み**（2026-07-08、branch `rebuild/console`）。`index.html` が新コンソール、
> 旧コード（state.tsx / components/ / views/ / settings/ / styles.css / 旧 App・main）は削除済み。スワップ時の暫定エイリアス
> `next.html`（+ vite multi-entry の `next` 入力）も撤去済み（`index.html` 単一エントリ）。api.ts→`core/api/client.ts`、term.ts→`terminal/term.ts`、
> viewport→`app/`、FileIcon→`ui/` に吸収済み。チェックリストは全数 ✔（機能はコード検証、外観はフェーズ毎のユーザー目視）。
> 決定の要約は [decisions/0011-console-rebuild.md](../decisions/0011-console-rebuild.ja.md)。
>
> **スワップ後に残す意図的な負債（動作パリティに影響なし・随時解消）:**
> - MirrorView は「transcript パーサ純関数化+ブロック分解」でなく **忠実移植のまま**（品質リスク優先で解体を見送り。分解は次の独立タスク）。CommitGraph/GitDiff/ビュアー群も verbatim。
> - 抽出 CSS（viewer/mirror/chat/wsbar/topbar/memo/settings.css）の未使用セレクタ刈りは未実施（ピクセル一致優先）。
> - `:where(スコープ)` の legacy button compat（mirror/chat/wsbar/topbar/onboard/settings/admin）は ui/Button 化と同時に解消する。
> - レイアウト永続キーは `af.layout2.<slug>` のまま（旧 `af.layout.<slug>` は読み取り migration 元として残読）。
> - 非 chat 種の起動プロンプトは暫定 sendPromptWhenAlive。旧 WsBar に「作り直す」ボタンは無い（設定>環境 = EnvTab が正）。

## 背景と診断 — なぜリファクタでは足りないか

個々のコード品質ではなく、**増築をすべて吸い込む構造が 3 つ固定化している**：

1. **God Context** — `state.tsx`（1,266 行）の単一 `AppContext` に 110+ キー、31 ファイルが
   `useApp()` を購読。セクション間連携は `bump*()` カウンタ（sessionsKey/reposKey/connKey/
   filesKey/memosKey）という手作りイベントバス。値オブジェクトは非メモ化で、4 秒のセッション
   ポーリングを含む全更新が全消費者の再レンダー候補になる。全機能追加がここを通る設計のため、
   直しても次の機能でまた太る。
2. **Pane 構造の暗黙契約** — `Pane` は 13 個の optional フィールドを平坦に持つ wide struct
   （判別 union の潰れた形）。レイアウト演算（open/split/close/swap/drop、空カラム collapse）
   が state.tsx 内に散在・3 箇所重複。**paneId という文字列が React key / `term.ts` の `insts`
   Map / `history.state` の 3 系統を暗黙に結合**し、正しさは「約 7 個の effect と 4 個の
   wired-once フラグが正しい順に発火すること」に依存。契約はどこにも明文化されていない。
3. **種類軸ディレクトリ + 単一 CSS** — `components/` `views/` `lib/` の種類分けで機能の凝集が
   なく、`styles.css` 8.8k 行に全機能のスタイルが混在。God コンポーネント：`MirrorView` 2,192 行 /
   `AdminTab` 1,378 行 / `FilesSection` 1,043 行 / `WsBar` 711 行。

加えてモジュールレベル可変シングルトン（`insts` Map、`selectedTenant`、fetch モンキーパッチ、
wired-once フラグ群）が StrictMode・テストと根本的に相性が悪く、自動テストはゼロ
（typecheck + build + ブラウザ目視のみ）。

## 据え置く資産（再発明しない）

リビルドでも**書き直さず移設**するもの：

- **`term.ts` の xterm 内部ロジック** — heartbeat ゾンビソケット検出、WebGL context-loss 復旧、
  クリップボード / Keyboard Lock、ソフトキーボード対応、Unicode11 幅。実戦で獲得したドメイン
  知識の塊。中身は不可侵、所有権の境界だけ引き直す（後述 TermService）。
- **`api.ts` のコア設計** — `rel()` の baseURI 相対解決、fetch=ヘッダ / WS・新タブ=`?tenant=`
  の使い分け、SSE パーサ（`chatStream`）、エラーコード→和文の `ERR_TEXT`。
- **PaneHost の flat-absolute 戦略** — 「pane 移動で xterm を re-parent しない」問題への正解。
  戦略は維持し、演算だけ純関数化。
- **`panebadge.ts` / `panehover.tsx` / `sessionview.ts` / `viewport.ts`** — 理想形の見本。ほぼそのまま。
- **`ConfirmProvider` / `ToastProvider` / `EmptyState` / `Modal` / `Section`** — 新 `ui/`
  プリミティブ層の種。
- **テーマ機構** — `data-theme` + CSS 変数 + `SURFACE_COLORS` + ui-prefs サーバ同期
  （localStorage 即時キャッシュ + 600ms debounce PUT + boot は server-wins）。
- **`lib/gitgraph.ts`（レーン DAG）、fileicons/filemeta、termcolor** 等の純ロジック群。

## ハード制約（再掲・全て据置）

- CP は `console/dist` を `Cache-Control: no-store` で静的配信。dev は `vite build --watch`。
- 全 URL は `document.baseURI` 相対（`rel()`）。絶対パス禁止（path-strip プロキシ対応）。
- 全リクエストに `X-AF-Tenant`（fetch はヘッダ、WS/新タブ/ダウンロードは `?tenant=`）。
- xterm: fit + web-links + unicode11 + WebGL、JetBrainsMono。**pane↔term 再構築は不可侵**。
- モバイルは「監視 + 軽操作」（`@media (max-width:760px)` に閉じる）。
- 検証はヘッドレスゆえ `npm run typecheck` + `npm run build` 緑 + **ユーザーのブラウザ目視**。
- Marp: `vite.config.js` の mathjax/katex → `marp-math-stub.js` alias を維持（minify ハング回避）。
- ホストはメモリ制約（build OOM 前科）。watch は 1 プロセス、heap は現行設定を維持。

## 目標アーキテクチャ

```
console/src/
  app/          シェル・プロバイダ組立・エントリ（新: next.html → 完成後 index.html）
                history 統合（pushState/popstate でモーダル・ドロワー・レイアウト復元）
  core/
    api/        client.ts（rel/tenant 注入/ERR_TEXT/SSE）+ ドメイン別 endpoints/*.ts
    store/      zustand ストア群の共通ユーティリティ
    theme/      tokens・applyTheme・ui-prefs 同期（lib/settings.ts の移設）
  layout/       ★ペインレイアウトエンジン = 純関数モジュール（vitest 対象）
    types.ts    判別 union 化した PaneContent（terminal|file|scm|changes|commit|wtdiff|doc|diff|chat）
    ops.ts      open/split/close/swap/drop/collapse — Layout in → Layout out の純関数
    store.ts    zustand: layout + activeId + 履歴 push、単一 commit ファネル
  terminal/     ★TermService: term.ts 内部を包む明示的境界
                layout ストアを購読して insts を宣言的に reconcile する一本道
  ui/           プリミティブ: Button/IconButton/Modal/Popover/Menu/Field/Pill/Card/Tabs
                + 各自の co-located CSS
  features/     機能単位で凝集（endpoints 呼び出し・状態・UI・CSS を同居）
    sessions/  repos/  files/  scm/  chat/  mirror/  memo/
    settings/  admin/  onboarding/  workspace/(WsBar)  terminal-view/
  styles/       tokens.css（テーマ変数・SURFACE_COLORS のみ）— 8.8k 単一 CSS は解体
```

### 状態管理 — zustand をドメイン別に

単一 Context を廃し、**zustand** のドメイン別ストア（tenant / workspace / sessions / layout /
dialogs / settings）へ分割。selector 購読で「4 秒ポーリングが全画面を再レンダー」を根絶。
`bump*()` カウンタは廃止し、ストアの通常の更新伝播に置換。React 外（TermService・fetch 層）
からも `store.getState()/setState()` で素直に連携でき、wired-once フラグと ref ミラー
（`tenantRef`/`wsStateRef`/…）を全廃できる。

### Pane 型と paneId 契約

- `Pane` を判別 union に（`{id, kind:'terminal', session} | {id, kind:'file', path} | …`）。
  `blankPane()` の 13 nullable フィールドを廃止。
- localStorage `af.layout.<slug>` は読み込み時に旧形式→新形式マイグレーション（既存レイアウトを
  壊さない）。history.state も同様。
- **paneId 契約を明文化**：「pane の同一性 = ターミナルの同一性。移動・swap は id を保持、
  複製・再採番は禁止」。layout/ops.ts の型とユニットテストで強制。
- 旧コードの active-pane 平坦投影（`mode`/`scmRepo`/`filePath`/`session` の後方互換レイヤ）は
  全廃 — 全コンポーネントを最初から pane-aware に。

### TermService

`term.ts` の xterm ロジックは温存しつつ、モジュールグローバルの `insts` Map と散在 effect を
**layout ストア購読 1 本**に集約：layout 変化 → `reconcile(activePaneIds)`（不要 dispose /
必要 ensure / attach）。再接続経路（focus / visibility / heartbeat / reveal）は TermService 内の
明示的な状態機械に整理。DOM コンテナは従来どおり常駐（hidden 切替、unmount しない）。

### API 層

fetch モンキーパッチ（ヘッダ全数保証 + 401→login）は**維持**。ただしアプリコードは
`core/api/endpoints/*.ts` の型付き関数経由に統一し、コンポーネントからの生 `api()` 呼び出しを
なくす。SSE / multipart / download ヘルパはコアに移設。

### CSS / テーマ

`styles/tokens.css` にテーマ変数（dark 既定 + `[data-theme=light]` 上書き + region 変数
`--topbar-bg`/`--leftpane-bg` 等）だけを置き、コンポーネント別スタイルは co-located の
プレーン CSS ファイルへ分割（CSS Modules は使わず、クラス接頭辞規約で衝突回避 — 既存クラス名
資産と目視デバッグのしやすさを優先）。highlight.js の `--hl-*` 連動も維持。

### テスト — 初めての安全網

**vitest**（`--maxWorkers=2`、ホストメモリ制約順守）を導入し、純ロジックのみ対象：
layout/ops、gitgraph、Mirror の transcript パーサ、fmttok/bytes/reponame、ストアの遷移。
DOM・ビジュアルは従来どおりユーザー目視（並行エントリで新旧比較）。

### MirrorView の解体

2,192 行を「**transcript パーサ（純関数・vitest 対象）**」と「ブロックコンポーネント群
（Turn/Thinking/ToolTrace/TaskChecklist/PendingQuestions/PlanBlock/SpendBar/Composer）」に分解。

## 移行戦略 — 並行エントリ（決定）

Console は日常運用の運転席であり、テストゼロの状態で big-bang 切替はできない。

- 同一 Vite プロジェクトに multi-entry：`index.html`（現行）+ **`next.html`（新）**。
  CP は静的配信のままなので**バックエンド変更ゼロ**で `…/next.html` が実バックエンドで動く
  → ブラウザで新旧を並べて目視比較（目視 QA 前提の開発規律と噛み合う）。
- `term.ts` コア・`api` コア・`types` は両エントリから共有 import。
- **旧側は凍結**（バグ修正のみ）。新機能は該当領域の移植完了後に新側へ実装。
- 全領域移植 + パリティチェックリスト消し込み後、`next.html` → `index.html` にスワップし
  旧コードを一括削除。localStorage / ui-prefs のキーは全据置なので設定・レイアウト・下書きは
  切替時にそのまま引き継がれる。

## フェーズ計画

各フェーズ = typecheck + build 緑 + ユーザー目視 + push。目視 1 回 30 分以内に収まる粒度で刻む。

| # | 内容 | 備考 |
|---|---|---|
| P0 | 骨格：tokens.css / ui プリミティブ / app シェル / zustand 基盤 / api コア移設 / vitest / next.html 配線 | 本ドキュメント + チェックリスト commit |
| P1 | **ターミナル + レイアウトコア**：layout エンジン（テスト付き）→ TermService → PaneHost/Pane → attach | 最難関を最初に。ここが動けば残りは低リスク |
| P2 | 左レール：Sessions / Repos / Files + 関連モーダル（NewSession/NewRepo/Launch/Branch 系/Archived/SSM ログイン…） | |
| P3 | SCM 一式：SourceControl / Changes / CommitDetail / WorkingDiff / CommitGraph / GitDiff | |
| P4 | ビューア：File/Code/Markdown/Marp/Image/Diff（mermaid・marp の lazy 維持） | |
| P5 | チャット + ミラー：ChatView / AssistantSection / AssistantModal / MirrorView 分解移植 | 最大の解体作業 |
| P6 | メモキュー / TopBar / WsBar / オンボーディング / ContextBar | |
| P7 | Settings 6 タブ + AdminDialog | |
| P8 | モバイル・a11y 仕上げ → スワップ → 旧削除 | |

規模感：書き直し対象 ~25k 行（新構造では 18–20k 程度の見込み）。目視往復込みで実働 2–3 週間規模。

## 機能パリティ・チェックリスト

移植完了の判定基準。**1 つも落とさない**（フェーズごとに消し込み、スワップ前に全数 ✔）。

### ビュー（メインペイン）
- [x] TerminalView — PTY 常駐・hidden 切替・未 attach 時ブランドアート
- [x] ChatView — ストリーミング・draft→昇格・貼り付け画像プレビュー
- [x] MirrorView — 折りたたみ/thinking/ツールトレース/TaskChecklist/PendingQuestions（クリック送信・複数選択）/PlanBlock 承認・却下/spend バー/コンテキスト行/下書き永続/送信モード
- [x] FileView（+ linemarks 変更バー）/ CodeView（行番号・minimap・折返し）
- [x] MarkdownView（mermaid lazy・リンク解決 onOpenFile/Dir）/ MarpView（スライド・全画面）/ ImageView（zoom/pan）
- [x] DiffView（Edit 系ツール差分）/ WorkingDiffView / ChangesView（stage/unstage/discard/commit/identity）
- [x] SourceControlView（graph 300・fetch/ff/checkout）/ CommitDetailView / DocView

### 左レール
- [x] LayoutMap（クリック/ドラッグ・a11y title）・Section collapse 永続・カウントバッジ
- [x] SessionsSection — dir グループ・start/stop/halt/fork/recreate/archive・タイトル/ブランチ改名（AI 提案含む）
- [x] ReposSection — clone/削除/fetch/ff・起動（LaunchModal）・provider 表示
- [x] FilesSection — 遅延ツリー・changes ビュー・upload/download/mkdir/newfile/rename/delete・reveal-in-files
- [x] AssistantSection（+新規ピッカー・履歴 rename/delete）
- [x] MemoQueueSection + MemoTidyModal（AI 整理・PATCH 承認）

### モーダル
- [x] NewSessionModal（kind/model/RepoPicker/DirPicker/SSM 継続）
- [x] NewRepoModal / LaunchModal（テンプレート・履歴）/ BranchModal+BranchList / BranchRenameModal / SessionTitleModal
- [x] SendSelectionModal（宛先記憶）/ SsmLoginModal / ArchivedModal / AssistantModal
- [x] RepoPicker（Bitbucket/GitHub/内部 git タブ）/ DirPicker / InternalRepoBrowser

### バー・シェル
- [x] TopBar（テナント picker 単一所属時非表示・アカウントメニュー・外観ポップオーバー）
- [x] WsBar（state/Start-Stop/recreate・リソースチップ + Sparkline・プレビュー ポップオーバー・opencode web・Claude/Codex 使用量）
- [x] OnboardingCard（自動チェック・dismiss 永続）/ ContextBar（80%/93% 警告）
- [x] PaneHost（4 列×2 行・drag swap・drop-to-split・divider）/ TermKeys（モバイル）
- [x] history 統合（Back でモーダル/ドロワー/レイアウトが戻る・URL 不変）

### Settings / Admin
- [x] 表示 / ワークスペース(toolchains) / エージェント(Claude device flow・Codex device+API key・opencode・RTK・通知・Remote Control) / Git(GitHub/Bitbucket OAuth・identity・内部 git 管理) / AWS SSM(プロファイル・ホスト・端末色) / MCP(PAT 発行・失効)
- [x] AdminDialog — テナント CRUD・メンバー/ロール・クォータ・稼働セッション・強制停止・使用量・egress allowlist/mode・監査ログ・clean-home

### 横断
- [x] テーマ（dark/light・surface 4 色・フォント/サイズ・アイコンセット）+ ui-prefs サーバ同期（server-wins）
- [x] Toast（error は sticky + role=alert）/ Confirm（danger）/ EmptyState
- [x] モバイル（ドロワー・全画面モーダル・TermKeys・visualViewport fit・safe-area）
- [x] 401→login リダイレクト・テナント切替・super_admin ゲート
- [x] localStorage キー据置：`af-tenant` `af.layout.<slug>` `af-left-open` `af-left-mode` `af-files-view` `af-session-groups-collapsed` `af-display-settings` `af-kind-avail` `af.onboarding.dismissed` `af.sendsel.lastSession` `af.repo-prompts.<repo>` `af.repo-lastkind.<repo>` `af.repo-model.<repo>` セクション collapse 各キー・EnvTab キャッシュ・Mirror 下書きキー

## リスクと手当

- **二重メンテ期間** → 旧側凍結（決定）。緊急バグ修正のみ旧側に許容。
- **目視 QA の負荷** → 並行エントリで「同じ操作を新旧で試す」だけにし、フェーズを細かく刻む。
- **ビルドメモリ** → 単一 vite 設定で両エントリを 1 プロセスビルド。vitest は `--maxWorkers=2`。
- **レイアウト永続の互換** → `af.layout.<slug>` / history.state の読み込みマイグレーションを P1 で実装し、旧形式は読み捨てでなく変換。
- **StrictMode** — 新側は最初から StrictMode 二重実行に耐える構造（シングルトン排除）で書く。
