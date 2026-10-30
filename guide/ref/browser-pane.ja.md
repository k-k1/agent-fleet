---
audience: "全員（とくに「動いている Web アプリを人に見せる」判断をするエージェント）"
source_of_truth: "用語とペインにできること／できないことはこのファイル、状態と復旧は member/14、数値は limits.ja.md、ボタンの名前は Console"
---

# ブラウザペイン — 利用契約

[English](browser-pane.md) | 日本語

Workspace の中で動いている Web アプリを見せる手段は 2 つあり、**選択を誤ると一往復
無駄になります**。その両方の契約です。

## 2 つの経路

| 用語 | 契約 |
|---|---|
| **ブラウザペイン**（ボタンは「ペインで開く」）| Workspace 内 Chromium が `http://127.0.0.1:{port}{path}` を直接開き、描画と入力だけを Console のペインへ中継する。HMR・WebSocket・SSE・cookie・redirect・絶対 path の asset、そして**人が操作する必要があるもの**はこちら。|
| **軽量プレビュー** | HTTP プロキシを新しいタブで開く低負荷の経路。JSON・health エンドポイント・単純な静的ページを**一度見るだけ**に限る。HMR / WS / SSE は使えず、root path・絶対 asset・redirect・cookie path に依存するアプリの動作は保証しない。|
| **Page** | ペインが所有する一時的な Chromium Page と、その独立したブラウザコンテキスト。Workspace 当たり何個まで持てるかは[上限](limits.ja.md#製品として固定のもの)にあり、cookie も storage も共有しない。|

**迷ったらブラウザペイン。** 軽量プレビューは「HTTP 応答を 1 回見るだけ」と言い切れる
ときの省資源な例外です。

どちらの開き方（ポートとパスの欄、どのボタンが既定になるか）もメンバーの手順です:
[member/14 プレビューとブラウザ](../member/14-preview-browser.ja.md#使い方)。ポート `7700` は Agent 自身が使うので開けません。

## どちらを選ぶか（代表例）

構成別の表（Node / Vite、Spring Boot、API のみ、frontend + API）は
[member/14 構成別の例](../member/14-preview-browser.ja.md#構成別の例)にあります。原則は、HMR・WebSocket・SSE・
cookie・redirect・絶対パスのアセットが要るものはブラウザペイン、HTTP 応答を 1 回見るだけなら軽量プレビューです。

## 状態と復旧

ペインに出る 3 つの状態（`target-unreachable`・`disconnected`・`crashed`）の意味と対処は
[member/14 状態表示と復旧](../member/14-preview-browser.ja.md#状態表示と復旧)にあります。その前提となる契約：
復旧のための操作（再接続・Console のリロード・Workspace の Stop→Start）はどれも、現在の port/path から
**新しい Page を作る**ので、cookie・storage・入力途中の状態は復元されません。

## lifecycle と資源の上限

- 表示中のブラウザ接続は Workspace を warm に保つ。
- ペインが非表示になるか、Console のブラウザタブが background になると描画を止める。
  Page は猶予（[上限](limits.ja.md#製品として固定のもの)参照）の間だけ保持して解放する。戻ったとき、保持中なら同じ Page、解放後なら
  保存済みの port/path から新しい Page を作る。
- ペインの identity を layout から削除すると Page を解放する。identity が残る場合
  （最後のペインを閉じて空表示へ戻す等）は上の猶予の扱いになり、**その猶予中も
  Page の上限を消費し得る**。
- Workspace の Stop は一時 Page を捨てるが layout の port/path は残す。Start 後、
  表示中のペインは同じ target から自動で作り直す。
- 上限（Chromium のプロセス数・同時 Page 数・viewport・フレームレート）は
  [上限](limits.ja.md#製品として固定のもの)にあります。
  **動画や高 fps の確認を目的にしない。**

## 範囲と既知の制約

- **これは汎用ブラウザではない。** top-level navigation は loopback HTTP(S) に限定し、
  外部への redirect は止める。
- Console ドロワーは接続中 Page の console message と uncaught error を上限つき（[上限](limits.ja.md#製品として固定のもの)）で
  保持し、`error` / `warn` を先に見せる。**永続ログではなく、DevTools の代替でもない**
  （DOM・Network・Sources・Storage は無い）。
- upload/download・clipboard・drag & drop・音声・動画・WebRTC・permission prompt・
  複数タブは対象外。
- **スマートフォンでは入口が `⋯` の中に移る。** スマホ幅ではワークスペース操作バーに
  「プレビュー」ボタンが無く、右端の `⋯` を開くと同じポート／パス欄と「ペインで開く」が
  並んだポップオーバーが出ます。そこから先はデスクトップと同じです（toolbar・canvas の tap・
  日本語入力・Console ドロワー）。

## ブラウザペインが無い配備

ワークスペースを `kubernetes` ランタイム（プレビュー。[配備形態](deploy-targets.ja.md)）または `ecs`（Fargate）で動かす配備には、**ブラウザ機能が一切ありません**。
ブラウザペインも、エージェント向けの Chromium の接続も、ヘッドレス Chromium もありません。
`kubernetes` のワークスペースの pod は Pod Security の `restricted` レベルで動き、NoNewPrivs が立つ
（setuid の `chrome-sandbox` が権限を上げられない）うえにランタイム既定の seccomp プロファイル
（ユーザー名前空間を作れない）が掛かります。Fargate のタスクには `SYS_ADMIN` を足せないため、
同じ既定の seccomp が、setuid ヘルパーの作る名前空間を断ります。どちらも Chromium のサンドボックスが
起動できません。サンドボックス無しで Chromium を動かすことはしないと決めています
（ADR 0106 の 2026-10-04 の追記）。`ecs-ec2`・`docker`・`native` は対象外です。

代わりに見えるもの：

- **プレビュー**の「ペインで開く」は押せない状態で表示され、ツールチップと下のヒントが理由を示します。
  **軽量プレビュー**はほかの配備と同じく使えます。
- セッション行のポートは、ペインではなく軽量プレビュー（新しいタブ）で開きます。
- 保存済みのレイアウトから戻ったブラウザペインは、ページの代わりに理由を表示し、同じポートとパスを
  軽量プレビューで開くボタンを出します。同じように戻った Chromium の接続のペインは、開くローカルの
  ポートが無いので理由だけを表示します。
- エージェントにはブラウザツールそのものが提示されません。それでも名前で呼んだ場合は、同じ説明を付けて
  `browser_unavailable` を返します。

これは配備のランタイムで決まるので、その配備の全メンバーで同じであり、ワークスペースが止まっている間も表示されます。

## Workspace の中で働くエージェントへ

**このペインを開く・操作する・見るツールはあなたに無く**、ペインは人のものです。
正確な port と path を伝え、プレビュー →「ペインで開く」へ誘導してください。
**自分が見ていないペインを根拠に「表示は正しい」と言わないこと**。自分で確かめる
必要があるなら、自前のヘッドレス Chromium を動かし、そうしたと明記してください。
環境変数 `AF_BROWSER_UNAVAILABLE` が設定されているワークスペースには、ペインもヘッドレス Chromium も
ありません（[上](#ブラウザペインが無い配備)）。テストと HTTP での確認で確かめ、人には軽量プレビューを
案内し、画面は確認していないとはっきり伝えてください。
