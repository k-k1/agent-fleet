---
audience: "全員（とくに「動いている Web アプリを人に見せる」判断をするエージェント）"
source_of_truth: "用語・フロー・状態・上限はこのファイル、ボタンの名前は Console"
updated: "2026-08"
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
| **Page** | ペインが所有する一時的な Chromium Page と、その独立したブラウザコンテキスト。Workspace 当たり同時に最大 2 つで、cookie も storage も共有しない。|

**迷ったらブラウザペイン。** 軽量プレビューは「HTTP 応答を 1 回見るだけ」と言い切れる
ときの省資源な例外です。

## フロー

1. shell かエージェントに開発サーバーを起動させる。Workspace 内の `127.0.0.1`
   （または全 interface）で listen させること。
2. デスクトップ / タブレットで、ワークスペース操作バーの**プレビュー**を開く。
3. `1..65535`（`7700` を除く）の port と、`/` で始まる path を入力する。
   host 欄は無く、外部 URL は受け付けない。
4. 通常は**ペインで開く**。戻る・進む・再読み込み・port/path 変更・click・scroll・
   入力はすべてペインの中で行う。
5. 単純な HTTP 確認だけなら**軽量プレビュー**。
6. overlay が出たら下の状態表に従う。**対象サーバーを後から起動した場合はまず再読み込み**、
   接続や Chromium 自体を作り直すなら再接続。
7. **Console** ドロワーのバッジで、そのページの `warn` / `error` を先に見る。

再接続・Console のリロード・Workspace の Stop→Start は、いずれも現在の port/path から
**新しい Page を作る**操作です。cookie・storage・入力途中の状態は復元されません。

## どちらを選ぶか（代表例）

| 構成 | 入力例 | 選択 |
|---|---|---|
| Node / Vite | `5173` + `/` | ブラウザペイン。HMR の WebSocket に必要。|
| Spring Boot | `8080` + `/` または `/actuator/health` | redirect・絶対 `/assets/*`・cookie を含む画面はブラウザペイン。health JSON を一度見るだけなら軽量プレビュー。|
| API のみ | `8080` + `/api/health` | JSON / status の 1 回確認は軽量プレビュー。SSE・認証 cookie・redirect・対話的な確認はブラウザペイン。|
| frontend + API | frontend `5173`、API `8080` | frontend の `5173` をペインで開く。別 loopback port への fetch / WS / SSE は使えるが、**通常のブラウザと同様に CORS 設定は必要**。必要なら API を第 2 Page で開く。|

## 状態と復旧

| 状態 | 意味 | 利用者の操作 |
|---|---|---|
| `target-unreachable` | Chromium は起動したが、その port/path で HTTP に応答が無い。**開発サーバーの起動待ちもこの状態**。| port・path・listen 状態を確認し、サーバーが上がってから**再読み込み**。残るなら**再接続**。|
| `disconnected` | Console―CP―Agent の browser WebSocket が切れた。**Chromium の crash とは限らない**。| Workspace が稼働中か、通信が戻ったかを確認して**再接続**。|
| `crashed` | Workspace 内 Chromium が異常終了し、既存 Page を継続できない。| **再接続**で新しい Page を作る。繰り返すなら Workspace のメモリ使用量と対象アプリを見る。|

Workspace の停止／起動中に接続しようとすると専用の overlay が出ます。稼働中になってから
再接続してください。

## lifecycle と資源の上限

- 表示中のブラウザ接続は Workspace を warm に保つ。
- ペインが非表示になるか、Console のブラウザタブが background になると描画を止める。
  Page は既定 60 秒だけ保持して解放する。戻ったとき、保持中なら同じ Page、解放後なら
  保存済みの port/path から新しい Page を作る。
- ペインの identity を layout から削除すると Page を解放する。identity が残る場合
  （最後のペインを閉じて空表示へ戻す等）は上の 60 秒契約になり、**その猶予中も
  Page の上限を消費し得る**。
- Workspace の Stop は一時 Page を捨てるが layout の port/path は残す。Start 後、
  表示中のペインは同じ target から自動で作り直す。
- 上限: Workspace 当たり Chromium 1 プロセス、同時 2 Page、viewport 最大
  `1600×1200`（DPR 1）、表示中最大 12 fps（JPEG quality 70）。
  **動画や高 fps の確認を目的にしない。**

## 範囲と既知の制約

- **これは汎用ブラウザではない。** top-level navigation は loopback HTTP(S) に限定し、
  外部への redirect は止める。
- Console ドロワーは接続中 Page の console message と uncaught error を最大 200 件だけ
  保持し、`error` / `warn` を先に見せる。**永続ログではなく、DevTools の代替でもない**
  （DOM・Network・Sources・Storage は無い）。
- upload/download・clipboard・drag & drop・音声・動画・WebRTC・permission prompt・
  複数タブは対象外。
- **スマートフォンではこのフローを開始できない。** 390×844 相当ではワークスペース
  操作バーの `⋯` が表示領域からはみ出して他の操作要素と重なり、タップできません。
  それ以降（toolbar・canvas の tap・日本語入力・Console ドロワー）は動くのですが、
  **入口が成立しない**ので、スマートフォンの利用者に「ペインで開いてください」と
  案内しないこと。デスクトップとタブレットのみ。

## Workspace の中で働くエージェントへ

**このペインを開く・操作する・見る道具はあなたに無く**、ペインは人のものです。
正確な port と path を伝え、プレビュー →「ペインで開く」へ誘導してください。
**自分が見ていないペインを根拠に「表示は正しい」と言わないこと**——自分で確かめる
必要があるなら、自前のヘッドレス Chromium を動かし、そうしたと明記してください。
