# コンテナ内ブラウザペイン — 利用契約

> 状態: **V2 確定**（2026-07-18、現行 Console の操作確認済み）
> 実装契約: [31-container-browser-pane.md](31-container-browser-pane.md)
> 意思決定: [ADR 0018](../decisions/0018-container-browser-pane.ja.md)
> 用途: V3 の `CLAUDE.md` / `AGENTS.md` 案内と V4 利用ガイドが参照する操作上の正

## 用語と使い分け

| 用語 | 利用契約 |
|------|----------|
| **ブラウザペイン**（UI は「ペインで開く」） | Workspace 内 Chromium が `http://127.0.0.1:{port}{path}` を直接開き、描画と入力だけを Console ペインへ中継する基本経路。HMR、WebSocket、SSE、cookie、redirect、絶対 path の asset、画面操作が必要ならこちらを使う。 |
| **軽量プレビュー** | CP の `/preview/{port}` HTTP proxy を新しいタブで開く低負荷経路。JSON、health endpoint、単純な静的ページを一度確認する用途に限る。HMR / WS / SSE は使えず、root path、絶対 asset、redirect、cookie path に依存するアプリの動作は保証しない。 |
| **Page** | ブラウザペインが所有する一時的な Chromium Page + 独立 BrowserContext。Workspace 当たり同時に最大 2 Page。Page 間で cookie や storage は共有しない。 |

迷った場合はブラウザペインを使う。軽量プレビューは「HTTP 応答を一度見るだけ」と判断できる場合の省資源な例外である。

## 推奨フロー

1. shell またはエージェントに開発サーバーを起動させる。サーバーは Workspace 内の `127.0.0.1` または全 interface で listen させる。
2. デスクトップ／タブレットのワークスペース操作バーで **プレビュー**を開く。
3. `1..65535`（`7700` を除く）の port と、必ず `/` で始まる path を入力する。host や外部 URL は入力しない。
4. 通常は **ペインで開く**を選ぶ。戻る、進む、再読み込み、port/path 変更、click、scroll、ASCII/日本語入力をペイン内で行う。
5. 単純な HTTP 確認だけなら **軽量プレビュー**を選ぶ。
6. overlay が出たら下表に従う。対象サーバーを後から起動した場合はまず **再読み込み**、接続や Chromium 自体を作り直す場合は **再接続**を使う。
7. **Console** drawer の badge と内容で対象ページの `warn` / `error` を優先確認する。

`再接続`、Console reload、Workspace Stop→Start は現在の port/path から新しい Page を作る操作であり、Page 内の cookie、storage、入力途中の状態までは復元しない。

## 代表例

| 構成 | 入力例 | 選択 |
|------|--------|------|
| Node / Vite | `5173` + `/` | HMR WebSocket を使うためブラウザペイン。 |
| Spring Boot | `8080` + `/` または `/actuator/health` | redirect、絶対 `/assets/*`、cookie を含む画面はブラウザペイン。health JSON を一度見るだけなら軽量プレビュー。 |
| API のみ | `8080` + `/api/health` | JSON／status の一回確認は軽量プレビュー。SSE、認証 cookie、redirect、対話的な確認はブラウザペイン。 |
| frontend + API | frontend `5173`、API `8080` | frontend の `5173` をブラウザペインで開く。別 loopback port への fetch / WS / SSE は利用できるが、通常のブラウザ同様に CORS 設定は必要。必要なら API を第 2 Page で開く。 |

## 状態と復旧

| 状態 | 意味 | 利用者の操作 |
|------|------|--------------|
| `target-unreachable` | Chromium は起動したが対象 port/path の HTTP 接続が成立しない。開発サーバーの起動待ちもこの状態。 | port/path と listen 状態を確認し、サーバー起動後に **再読み込み**。残る場合は **再接続**。 |
| `disconnected` | Console―CP―Agent の browser WebSocket が切れた状態。Chromium の crash を意味するとは限らない。 | Workspace が稼働中か、通信が戻ったかを確認して **再接続**。 |
| `crashed` | Workspace 内 Chromium が異常終了し、既存 Page を継続できない状態。 | **再接続**で新しい Page を作る。繰り返す場合は Workspace のメモリ使用量と対象アプリを確認する。 |

Workspace の停止／起動中に接続を試みると専用 overlay が出る。稼働中になってから再接続する。

## lifecycle と資源契約

- 表示中のブラウザ接続は Workspace を warm に保つ。
- ペインが一時的に非表示、または Console の browser tab が background になると描画を停止する。Page は既定 60 秒だけ保持し、その後解放する。再表示時は保持中なら同じ Page、解放後なら保存済み port/path から新しい Page を作る。
- ペイン identity を layout から削除すると Page を解放する。最後のペインを閉じて空表示へ戻す経路など、identity が残る場合は上記の非表示 60 秒契約になる。この猶予中も Page 上限を消費し得る。
- Workspace Stop では一時 Page を捨てるが、layout の port/path は残す。Start 後、表示中のブラウザペインは同じ target から自動再生成する。
- 上限は Workspace 当たり Chromium 1 process、同時 2 Page、viewport 最大 `1600×1200`（DPR 1）、表示中最大 12 fps（JPEG quality 70）。動画や高fps確認を目的にしない。

## 機能範囲と既知制約

- これは外部 URL を開く汎用ブラウザではない。top-level navigation は loopback HTTP(S) に限定し、外部への redirect は止める。UI も host 欄を持たず、port と `/` 始まり path だけを受け付ける。
- Console drawer は接続中 Page の console message と uncaught error を最大 200 件だけ保持し、`error` / `warn` を上に表示してコピーできる。永続ログではなく、DOM、Network、Sources、Storage 等を持つ DevTools の代替ではない。
- upload/download、clipboard、drag & drop、音声、動画、WebRTC、permission prompt、複数 tab は MVP の対象外である。
- **スマートフォンは現行の基本フロー対象外。** 390×844 相当の操作確認では、ワークスペース操作バーの `⋯` が表示領域外へはみ出し、他の操作要素と重なって通常タップできなかった。強制的にペインを開いた後の toolbar 表示、canvas tap、日本語入力、Console drawer は動作したが、開始導線が成立しないため、V3/V4 はスマートフォン対応済みと案内せずデスクトップ／タブレットを推奨する。

## V2 操作確認

現行 Console、実 CP relay、実 Workspace Agent browser handler、sandbox 有効の `/usr/bin/chromium` を使い、次を確認した。

- Vite の HMR、別 port API / SSE の console event
- Spring Boot 相当の同一 origin redirect、絶対 asset、cookie
- 軽量プレビューの新規 tab と単純 JSON
- `target-unreachable` overlay、サーバー復帰後の再読み込み／再接続
- Console drawer、Page close、2 Page／viewport 上限、最大 12 fps の product smoke
- 390×844 相当での入口不成立と、入口後の touch／日本語入力
