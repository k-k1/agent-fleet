# Console UX 刷新 — 残作業バックログ

`refactor/console-ux` ブランチの進捗と、**左ペインの後にやるべき残作業**の整理。
ブリーフ本体は `console-redesign.md` §18、進行時のルールはメモリ `console-ux-redesign` を参照。
着手時はユーザー合意を取った視覚案モック（単体 HTML）で進めたが、Console 全面リビルド（docs/22）で用済みとなり削除済み。

## 状態（2026-07-03 時点）
- ブランチ `refactor/console-ux`（main より先行・push 済み、全コミット `typecheck`+`build` グリーン、コードレビュー指摘は対応済み）。
- **完了:** 左レール操作のアイコン＋ラベル化 / Repos 行の状態チップ統一（`●`・`↑↓` 解消）/ 破壊操作の確認統一（native `confirm` 全廃・`ConfirmProvider`＋モバイル修正）/ 失敗表示のトースト化（native `alert` 全廃・エラーは sticky＋`role=alert`）/ New Session 種類の caps 駆動アイコン＋色 / 空・ローディングの `EmptyState` 統一 / 右ペイン状態表示統一（変更バッジのラベル化・SCM ahead/behind チップ）/ 外観編集を専用ポップオーバへ（ライブプレビュー維持）/ WsBar のリソース集約・close-all/Start-Stop のアイコン＋ラベル・停止確認 / DRY 整理（`useIsMobile`・`fmtGiB`・`SwatchGrid`・`useDismiss`）。
- **新規共有部品:** `components/{ConfirmProvider,ToastProvider,EmptyState,SwatchGrid}.tsx`、`lib/{bytes.ts, device.ts(useIsMobile), useDismiss.ts}`、`openSettings(section?)`。
- **追記（2026-07-03, `cbde989`〜`4ff879d`）:** A 左ペイン IA・B 右ペイン周辺 UI・C のライトテーマ色を実装済（下記チェック参照、全て push・グリーン・**要ユーザー目視**）。残りは C のモバイル/a11y（要ブラウザ）と D/E/F。

## 進め方（全項目共通）
- 小さくコミット → 各ステップ `npm run typecheck` + `npm run build` グリーン維持 → **ユーザーがブラウザで目視**（この環境は headless で画面が見えない）→ 区切りで `git push`。
- 機能は消さない（インベントリ `console-redesign.md` §18.2）。ハード制約（no-store 配信 / base-path 相対 URL / `X-AF-Tenant` / xterm 内部・pane↔term 再構成の不可侵 / 既存テーマ機構）を厳守。

---

## A. 左ペイン IA ✅ 完了（2026-07-03・要ユーザー目視）
「整え」の先の**情報階層**まで着手。
- [x] セクションの並び・密度・区切りの再検討 — 行間ディバイダ廃止・角丸 hover/active ピル化。並びは Sessions→Repos→Files のまま据置（`765cc04`）。
- [x] 各行レイアウトの一貫性 — active/選択の左アクセントバー統一・Repos 行 hover 追加・余白統一（`cc9f799`）。
- [x] セッション状態表示のピル化 — 入力待ち/進行中/質問あり/停止中を `.session-state` のピルチップに（モックの run/idle/stop 準拠、color-mix でテーマ追従、停止中は控えめな無地チップ）。ターミナルヘッダと共有。
- [x] `LayoutMap` の可読性 — セルの title/aria-label を「ペイン{n}: {名前} · {種類} · {状態}」に充実（凡例は不要と判断）（`7657307`）。
- [x] 折りたたみ状態の永続化・セクション見出しの情報設計 — `localStorage` 永続化＋件数バッジ＋`aria-expanded`（`cbde989`）。※副作用の横スクロール回帰を `ae57558` で修正。
- 参照: `console-redesign.md` §18.4（推奨 IA たたき台）。

## B. 右ペインの周辺 UI ポリッシュ（xterm 不可侵）✅ 完了（2026-07-03・要ユーザー目視）
- [x] ペイン制御クラスタ — 分割/折返し/閉じるを hover/active で薄い背景にまとめ、閉じるをディバイダで区切り（`61388cb`）。
- [x] ペインヘッダの見出し一貫化 — SCM の accent テキストを「accent アイコン＋fg 名」に、Doc/Diff アイコンも accent に統一（`d395798`）。※「全ヘッダにビュー種別ピル」は冗長リスクで見送り（要相談）。
- [x] ContextBar の自動圧縮間近の警告状態（80% warn / 93% danger）（`4ff879d`）。再開/圧縮ストリップ（`.mirror-attention`/`.mirror-compacting`）は既存で十分と判断。
- [x] SCM 変更リストの per-file 文字（`M/A/U`）は git 慣習で据置判断済。

## C. 横断 QA（目視必須・見た目回帰の温床）— ライト色は完了、残りは要ブラウザ
- [x] **ライトテーマのハードコード色:** light 上書きを追加。
  - [x] New Session 種類アイコン色 `.seg.big .seg-btn.kind-*:not(.active) .seg-ic`（`d0b6845`）。
  - [x] Files 変更バッジ色 `.chg-badge.st-*` → GitHub Light の濃い on-color（`d0b6845`）。
  - [x] BG実行中チップ `.session-state.bg` の teal（`09a2f8d`）。トースト/EmptyState/`.pane-count` は既にテーマ変数で対応済＝変更不要と確認。
- [ ] **モバイル/レスポンシブ確認:** 各ポップオーバ（外観・WsBar リソース/usage/preview）、トースト、確認カード、`ws-closeall` ラベル非表示、左レール drawer。※headless で目視不可＝**ユーザー確認待ち**。
- [ ] **アクセシビリティ:** フォーカスリング/キーボード、`useDismiss` の Escape 挙動、`aria`。※**ユーザー確認待ち**。
- [ ] **機能インベントリ回帰:** §18.2 と現 UI を突き合わせ、消えた導線がないか確認。

## D. 設定 / 管理ダイアログの一貫化 ✅ 完了（方向性モック合意済み 2026-07-03。gitconfig 等の残項目は別作業）
- [x] **タブ再編（対象＝ドメインで分割）:** 接続タブとエージェントタブを統合再編。エージェントタブ＝Claude/Codex/opencode を「接続＋挙動設定」1カードに、Git タブ＝GitHub/Bitbucket に分離。二重登場を解消。**実装済 `5e09ec9`（Git 分離）+ `1242ab7`（エージェント融合・接続撤去）**。タブは エージェント/Git/環境/SSM/MCP/表示 の6枚（要ユーザー目視）。
- [ ] **gitconfig（今後）:** Git タブに プロバイダ毎の commit identity（user.name / user.email）設定グループを追加。**新しい Agent エンドポイント（workspace/agent git identity + control-plane プロキシ）が必要**＝バックエンド変更を伴うため別途。GitTab.tsx に `TODO(gitconfig)` あり。
- Settings 各タブ（Connections/Agents/Env/SSM/MCP/表示）の余白・ラベル・見出しの体裁統一。
- [x] **接続タブ:** プロバイダを「バッジ＋名前＋状態ピル＋説明」のカードに。Codex は接続手段を推奨つき選択肢に＋デバイスコードを番号付き手順に。注記（ヘルプ文）は左罫線つきの定型ブロックで統一。**実装済 `85e6f3e`**（バッジ色はセッション kind に合わせて codex 緑/opencode 紫。要ユーザー目視）。
- **SSM タブ:** プロファイル/ホストの追加フォームを placeholder のみ→ラベル＋必須表示＋ヒント＋「必須/詳細(任意)」グループ分けに。プロファイル未登録時は無効欄でなく CTA バナー。
- `AdminDialog`（super_admin）は今回ほぼ未着手 — 新しい言語（ボタン/確認/トースト/チップ）に合わせる。
- 破壊操作の確認は `useConfirm` に寄せ済（AdminTab は既存 `ConfirmDialog` 利用）。トーンの統一のみ。

## E. オンボーディング / 初回起動 ✅ 完了（要目視）
- [x] `OnboardingCard` の CTA をアイコン＋ラベルに、next ハイライト/primary CTA の色をテーマトークンに（`9511b87`）。接続 CTA は git→Git / agent→エージェント に誘導。

## G. WsBar 右クラスタ（追加・§B 系）✅ 完了（要目視）
- [x] リソースタイル→ポップオーバ（既済）／opencode web とポートプレビューを1つの「プレビュー」ポップオーバに統合・右クラスタをチップ統一（`72e71f9`）。使用量↔リソースの並び替え・リソースPOP のグラフ整列（`abd0afb`）。

## F. 仕上げ ⏳
- [x] 最終 `typecheck`+`build` グリーン（全ステップ）。
- **PR は作成しない（ユーザー指示 2026-07-03）**。通し目視はユーザー側で継続。
- 残：gitconfig（Git タブ user.name/email、要 Agent エンドポイント）は別作業。旧 `.jsx` 前提ドキュメントの追随はマージ後。

---

## 優先度の目安（すべて実装完了・要ユーザー目視）
1. ~~A 左ペイン IA~~ ✅ / 2. ~~B 右ペイン~~ ✅ / 3. ~~C ライト色~~ ✅（モバイル・a11y は目視待ち）
4. ~~D 設定/管理~~ ✅ / 5. ~~E オンボーディング~~ ✅ / 6. ~~G WsBar~~ ✅
7. F: PR 不要。残タスクは gitconfig（別）・旧 jsx ドキュメント追随（マージ後）。
