# 96. 稼働中セッションを一望するペイン — 検討・実装・実測

> 状態: **P0 完了**（2026-09-12）。決定は [ADR 0078](../decisions/0078-sessions-overview-pane.ja.md)。
> Console だけの変更で、サーバは触っていない。本書は「何を調べて何を選び、実機（headless
> Chromium＋README 用スタブ）で何を測ったか」の記録である。
> 関連: [52-working-sets.md](52-working-sets.md)（作業グループの絞り込み規則）/
> [94-rail-lineage.md](94-rail-lineage.md) §94.10（wire に無いフィールドは CP の中継で落ちる——P1 の「最後の一言」が踏む）/
> [44-operator-interaction-graph.md](44-operator-interaction-graph.md)（別タスクに送られた相関図。本ビューとは別物）

## 96.1 要求

利用者の言葉のまま: 「稼働中のセッションを一望できるビュー。ビュー内にパネルが並ぶ。パネルには
セッションの状況が表示される。左ペインの右クリックメニューと同じ操作が可能。ビューの横幅によって
横に並ぶ数が変わる」。

検討で確認を求めた 4 点と答え: ①置き場所＝**ペイン種別**、②停止セッション＝**既定は稼働中のみ、
トグルで足す**、③カードを開いたら一覧は**残す（隣に開く）**、④列＝**CSS Grid の auto-fill**。

## 96.2 調べて分かったこと（実装前）

- **メニューは既に部品**。`features/sessions/SessionMenu.tsx` の冒頭に「セッションが出る場所すべてで
  同じ項目を出すために行から切り出した」とあり、呼び手が持つのは開閉状態と配置だけ。タブ付き
  グリッドのタブ右クリック（`Pane.tsx`）が 2 つ目の呼び手で、カードは 3 つ目。項目は 17 種。
- **状態導出も共有済み**。`lib/sessionview.ts` の `stateInfo()` は左ペインの行とペインヘッダの共通部品。
  クラス値は `on / off / off dead / off warn / off question / working / bg / question / question limited /
  on|off handoff` の 10 通り。
- **ペイン種別の追加は 8 か所**（union・`migrate.ts` の検証・`sameTarget`・`Pane.tsx` の switch・
  `paneTitle`・`LayoutMap` の略号・`canPopout`・i18n）。共有セッション種別（`5e47b714`）が同じ手順の前例。
- **DTO に無いもの**: 「最後の発話」「最終更新時刻」。`Session` には `updatedAt` が無い。
- **「フリート俯瞰図」は先約がある**が別物。ADR 0027 が「全フリート俯瞰を同じ図で兼ねる」を却下して
  別タスクに送り、ADR 0041 決定 9 が「相関図が必要になる」と確定した。それは会話×セッション×
  メッセージの**相関図**であり、本ビューはカードの**一覧**。ADR 0078 の却下案に明記した。
- **ADR 0077 は別レーンが取っていた**（PR #566・EC2 Fleet）。本 ADR は 0078。
  [[parallel-lanes-migration-version-collision]] と同じ罠が ADR 番号にもある——`gh pr list` で
  開いている PR の `docs/decisions/` を見てから番号を取る。

## 96.3 実装で踏んだこと

1. **レイアウト図はペインが 1 つだと消える**（`LayoutMap.tsx:47` `paneCount(layout) <= 1 → null`）。
   最初は図の見出し行にだけボタンを置いたが、単一ペインの画面（＝一覧を開きたい場面）で入口が
   消えるのをスクリーンショットで見て、操作バー（右に分割／下に分割／全て閉じる の並び）に足した。
2. **`features/sessions/open.ts` を keys の command 表から import すると node の試験が落ちる**
   （`ReferenceError: self is not defined`——`terminal/service.ts` 経由で `@xterm/addon-fit` が
   module 評価時に `self` を触る）。opener は `features/overview/open.ts` に分け、`layout/store` だけを
   import する。
3. **トグルの状態は React state に置けない**。タブ表示ではタブ切替でビューが unmount される
   （[[tabbed-pane-shared-container]] の「1 セル 1 コンポーネント」の裏返し）。`showStopped` を
   `PaneContent` に持たせ、`setPaneTarget` で書く。スタブ実機で `localStorage` の `af.layout2.*` に
   `{"kind":"sessions","showStopped":true}` が永続することを確認した（§96.5）。
4. **`@container paneview` はビューの根に自分で宣言しないと効かない**。コンテナを宣言しているのは
   ミラーの shell と端末の根であって Pane ではない。`.ovw` に `container: paneview / inline-size`。
5. **カードは `<button>` にできない**（中に ⋯ と序数バッジのボタンが入る＝入れ子禁止）。
   `<article role="button" tabIndex=0>` にして Enter／Space／Menu キー／Shift+F10 を自前で受ける。
   Menu キーの判定は `features/project/contextMenuKey.ts` を借りる。
6. **待ち時刻の台帳は借りない**。`waiting.ts` は「パレットの並び以外に使うな」と明記されている。
   `sortSessionsByAttention(list, () => 0)` で段だけ借り、段内は createdAt 降順（＝API の順）に固定。
   純関数試験で「同じ入力なら順序が変わらない」を陽性に取っている。

## 96.4 試験

- 純関数 `features/overview/overview.test.ts`（node）: 稼働のみ／停止込み、作業グループ（直接所属と
  repo 継承）、質問中が先頭、段内の安定、稼働数。
- DOM `features/overview/SessionCard.dom.test.tsx`（jsdom）: クリック・Enter・中クリックのすべてが
  `openSessionFromList(s, **true**, running)` を呼ぶ（隣に開く契約）／フォルダ消失＋転写無しは開かない／
  右クリック・⋯・Shift+F10 の 3 経路で `.ui-menu` に停止・改名・ID コピーが出る。
- 既存: `layout/*.test.ts`・`features/panes/*.test.ts`・`features/keys/commands.test.ts` 緑（後者は
  §96.3-2 の修正前は赤）。`typecheck`（TS7）は陽性対照つきで緑、`i18n:lint` 緑。

## 96.5 見た目の実測（headless Chromium × README 用スタブ）

`console/scripts/shots/server.mjs`（実バンドル＋fixture の API）を動的ポートで立て、Chromium は
`--remote-debugging-port=0` と `user-data-dir/DevToolsActivePort` で拾う（固定 9223 は共有コンテナで
他セッションに刺さる）。`--blink-settings=primaryHoverType=2,…` で hover を有効化。

| 場面 | 幅 | 実測 |
|---|---|---|
| 一覧だけ（1500×900） | ペイン約 1140px | カード 5・**4 列**・見出し「セッション一覧 稼働中 5」 |
| 側の列に置く（colRatios 0.72/0.28） | ペイン約 320px | カード 5・**1 列**・トグルはアイコンのみ（`@container`） |
| 右クリック | — | `.ui-menu` に 11 項目（copilot・alive）。行のメニューと同一の文言 |
| 「停止中も表示」 | — | カード 6・`.stopped` 1・`af.layout2.*` に `showStopped:true` が永続 |
| カードをクリック | — | `.pane` 2・一覧は残る・新ペインは `activeCellId` になり、カードと左ペインの行に序数 **2** |
| en / light | — | 質問カードの暖色枠・「5 running」・「Show stopped」が読める |

画像は `/tmp/ovw-shots/` に出したが成果物としては保存しない（README の 6 枚とは別・fixture は架空）。

## 96.6 P1 に送ったもの

- **「最後の一言」**: Agent が DTO に 1 行要約を足す。CP の `sessionWire` に無いフィールドは黙って
  落ちる（§94.10）ので、足すのは 4 か所。
- 文字列での絞り込み（左ペインの検索欄と同じ照合）。
- 停止カードの再開ボタン直置き（今は右クリック→再開）。
- 系譜でカードを束ねる（P2）。相関図は本ビューの外。
