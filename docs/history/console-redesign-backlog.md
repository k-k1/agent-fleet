# Console UX 刷新 — 残作業バックログ

`refactor/console-ux` ブランチの進捗と、**左ペインの後にやるべき残作業**の整理。
ブリーフ本体は `console-redesign.md` §18、進行時のルールはメモリ `console-ux-redesign` を参照。
着手時にユーザー合意を取った視覚案モック: [`console-ia-mock.html`](./console-ia-mock.html)（ブラウザで開ける単体 HTML）。

## 状態（2026-07-03 時点）
- ブランチ `refactor/console-ux`（main より先行・push 済み、全コミット `typecheck`+`build` グリーン、コードレビュー指摘は対応済み）。
- **完了:** 左レール操作のアイコン＋ラベル化 / Repos 行の状態チップ統一（`●`・`↑↓` 解消）/ 破壊操作の確認統一（native `confirm` 全廃・`ConfirmProvider`＋モバイル修正）/ 失敗表示のトースト化（native `alert` 全廃・エラーは sticky＋`role=alert`）/ New Session 種類の caps 駆動アイコン＋色 / 空・ローディングの `EmptyState` 統一 / 右ペイン状態表示統一（変更バッジのラベル化・SCM ahead/behind チップ）/ 外観編集を専用ポップオーバへ（ライブプレビュー維持）/ WsBar のリソース集約・close-all/Start-Stop のアイコン＋ラベル・停止確認 / DRY 整理（`useIsMobile`・`fmtGiB`・`SwatchGrid`・`useDismiss`）。
- **新規共有部品:** `components/{ConfirmProvider,ToastProvider,EmptyState,SwatchGrid}.tsx`、`lib/{bytes.ts, device.ts(useIsMobile), useDismiss.ts}`、`openSettings(section?)`。

## 進め方（全項目共通）
- 小さくコミット → 各ステップ `npm run typecheck` + `npm run build` グリーン維持 → **ユーザーがブラウザで目視**（この環境は headless で画面が見えない）→ 区切りで `git push`。
- 機能は消さない（インベントリ `console-redesign.md` §18.2）。ハード制約（no-store 配信 / base-path 相対 URL / `X-AF-Tenant` / xterm 内部・pane↔term 再構成の不可侵 / 既存テーマ機構）を厳守。

---

## A. 左ペイン IA（次にやる）
まだ「整え（ラベル・チップ・空状態）」までで、**情報階層そのもの**は未着手。
- セクションの並び・密度・区切りの再検討（Sessions / Repos / Files の階層と視認性）。
- 各行レイアウトの一貫性（2 行構成・余白・アクティブ/ホバー表現）の総点検。
- `LayoutMap`（ペイン・ミニマップ）の置き場と可読性（2 文字略号＋状態ドット）— 凡例やツールチップの要否。
- 折りたたみ状態の永続化・セクション見出しの情報設計。
- 参照: `console-redesign.md` §18.4（推奨 IA たたき台）。

## B. 右ペインの周辺 UI ポリッシュ（xterm 不可侵）
- ペイン制御クラスタ（折返し/分割/閉じる/グリップ・順序チップ）の見せ方（過密回避しつつ意味付け）。
- ペインヘッダの情報（kind バッジ＋ビュー名＋パンくず）の一貫化。
- ターミナル/チャット（Mirror）ヘッダの ContextBar・再開/自動圧縮 UI のポリッシュ。
- SCM 変更リストの per-file 文字（`M/A/U`）は git 慣習で据置判断済 — 必要なら再検討。

## C. 横断 QA（目視必須・見た目回帰の温床）
- **ライトテーマ確認:** 新規要素の一部に**テーマ非依存のハードコード色**がある（要検証・必要なら light 上書き）:
  - New Session 種類アイコン色 `styles.css` `.seg.big .seg-btn.kind-* .seg-ic`。
  - Files 変更バッジ色 `.chg-badge.st-*`。
  - トーストの severity 色・EmptyState・チップ類全般。
- **モバイル/レスポンシブ確認:** 追加した各ポップオーバ（外観・WsBar リソース/usage/preview）、トースト、確認カード、`ws-closeall` ラベル非表示、左レール drawer。
- **アクセシビリティ:** フォーカスリング/キーボード操作、`useDismiss` 化した各メニューの Escape 挙動、`aria` 属性（トースト `role=alert`/`status` は対応済）。
- **機能インベントリ回帰:** §18.2 と現 UI を突き合わせ、消えた導線がないか確認。

## D. 設定 / 管理ダイアログの一貫化
- Settings 各タブ（Connections/Agents/Env/SSM/MCP/表示）の余白・ラベル・見出しの体裁統一。
- `AdminDialog`（super_admin）は今回ほぼ未着手 — 新しい言語（ボタン/確認/トースト/チップ）に合わせる。
- 破壊操作の確認は `useConfirm` に寄せ済（AdminTab は既存 `ConfirmDialog` 利用）。トーンの統一のみ。

## E. オンボーディング / 初回起動
- `OnboardingCard` と初回フロー（接続・起動導線）を新しい空状態/CTA 言語に揃える。
- `openSettings("connections"|"display"|…)` のタブ直開き導線を活用可。

## F. 仕上げ
- 最終 `typecheck`+`build`、通し目視。
- `main` への PR 作成（本文にサマリ・スクショはユーザー取得）。必要なら `/code-review ultra`（クラウド多エージェント）で最終確認。
- マージ後、旧 `.jsx` 前提の記述が残るドキュメントの追随。

---

## 優先度の目安
1. **A 左ペイン IA**（次セッション）
2. **C 横断 QA のうちライトテーマ＆モバイル**（見た目回帰を早めに潰す）
3. **B 右ペイン周辺 UI**
4. **D 設定/管理の一貫化**、**E オンボーディング**
5. **F 仕上げ・PR**
