# 0018. コンテナ内ブラウザペイン — Chromium を Workspace 内で動かし描画だけを中継する

- 状態: 採用・MVP実装済み（2026-07-18、W5統合検証済み）
- 関連: [31-container-browser-pane.md](../log/31-container-browser-pane.md)（実装契約・段階計画）/
  [31-container-browser-pane-ux-contract.md](../log/31-container-browser-pane-ux-contract.md)（利用契約）/
  [dev/05 §5.3](../build/05-api.md)（既存 port preview）/
  [0007-opencode-web-via-pk-webui.md](0007-opencode-web-via-pk-webui.md)（サブパス方式の既知限界）

## 背景

既存の port preview は Console から `/preview/{port}/` を新規タブで開き、CP → Workspace Agent →
コンテナ内 `127.0.0.1:{port}` へ HTTP 中継する。単純なページには十分だが、開発中の Node / Spring Boot
をそのまま確認する用途では次が障害になる。

- `/preview/{port}/` というサブパスをアプリが認識しない。`/assets/*`、redirect、cookie path、
  `location.origin` 前提のクライアントが壊れる。
- preview は HTTP のみで、Vite 等の HMR WebSocket と SSE を中継しない。
- Console と同一 origin の iframe は、任意の開発中アプリへ Console DOM・API・Service Worker の境界を
  与えてしまう。sandbox を厳しくすると今度は一般的な SPA の fetch/storage が壊れる。
- 専用サブドメインは AWS 等の本番入口では有力だが、現在のローカル／Docker 単体構成へ wildcard DNS、
  TLS、Host routing を先に持ち込むのは目的に対して大きい。

必要なのは「外部ブラウザからコンテナ内ポートを公開すること」ではなく、**コンテナ自身のブラウザで
localhost を忠実に開き、その画面を Console のペインで操作すること**である。

## 決定

1. Workspace イメージへ Chromium を焼き込み、Workspace Agent が遅延起動・監督する。ブラウザは
   Workspace ごとに最大 1 プロセス、ブラウザペインごとに独立 BrowserContext + Page とする。
2. Chromium は `http://127.0.0.1:{port}{path}` を直接開く。対象アプリの HTTP / WS / SSE / cookie は
   コンテナ loopback 内で完結させ、既存 `/preview/{port}` は通さない。
3. Agent は Chrome DevTools Protocol（CDP）の screencast を最新フレーム優先で Console へ送り、
   Console から viewport・mouse・wheel・keyboard・IME 確定文字・navigation 操作を受ける。
4. Console ↔ CP ↔ Agent は認証済み WebSocket 1 本で中継する。CP はフレームや入力を解釈せず、既存の
   terminal WS と同じ membership / Workspace 解決境界で双方向リレーする。raw CDP は公開しない。
5. `PaneContent` は `{kind:"browser", port, path}` のみを永続化する。Agent が返す `browserId` は
   ephemeral であり layout/localStorage へ保存しない。Console の BrowserService が `paneId` をキーに
   Page と socket を所有し、ペイン消滅時に破棄する。
6. 初期段階の navigation は loopback HTTP(S) に制限する。`file:`、`chrome:`、`data:`、ホスト内部の
   metadata endpoint、raw CDP、任意ファイル選択・download は許可しない。外部 URL は通常ブラウザで
   開く明示操作を別途提供する。
7. 既存の port preview は軽量な新規タブ経路として残す。将来、AWS 入口に専用 preview origin を設けても、
   ブラウザペインはエージェントによる視覚確認・自動操作・スクリーンショット基盤として残す。

### Chromium 配布方式（W1 スパイク）

Workspace image には Playwright 配布版ではなく Debian bookworm-security の `chromium` を採用する。
`CHROMIUM_VERSION` build ARG で Debian revision まで固定し、`chromium`、`chromium-common`、
`chromium-sandbox` を同じ版で導入する。amd64 / arm64 とも実行名は `/usr/bin/chromium` になる。
更新時は両archへ同じ版が公開されたことを確認して ARG を上げ、image smoke を通す。

2026-07-18 の比較結果:

| 方式 | multi-arch / 実行名 | 配布サイズの目安 | 更新・実装上の性質 |
|------|----------------------|------------------|--------------------|
| Debian package（採用） | bookworm の amd64 / arm64、`/usr/bin/chromium` | 150.0.7871.124 の browser/common/sandbox 合計: amd64 98.8 MiB download・333.2 MiB installed、arm64 93.9 MiB download・338.0 MiB installed | OS依存とsandboxをaptで解決し、Agentは固定パスを直接CDP起動できる |
| Playwright配布 Chromium | Debian 12 の x86-64 / arm64 対応、cache配下の版別パス | 公式資料のChromium例は約281 MiB（展開後、版で変動） | Playwright版とbrowser revisionの同期、cache配置から固定実行名への正規化が別途必要 |

Debian方式のinstalled値はパッケージmetadataの値で、既存imageと共有する依存libraryやDocker layer圧縮を
含む最終image差分ではない。実imageの増分と実行時メモリはW5のmulti-arch build・実機計測で確定する。
W1 image smoke はpackage revision、`chromium --version`、`root:root 4755`のsetuid helper、helper以外のsetuid/setgidなし、
`NoNewPrivs=0`、`dev(1000)`の`SYS_ADMIN` effectiveなし/helperのbounding setにはあり、sandbox有効のheadless起動、
日本語fontを使う固定ページのscreenshotを検証する。さらに製品Docker runtimeと同じ`--cap-add=SYS_ADMIN`で製品Agentの
pipe CDP経路を起動し、2 Pageを同時描画する。製品とsmokeはいずれも`--disable-dev-shm-usage`を使い、Docker/ECSの
小さい`/dev/shm`へ依存せず、コンテナ起動後のnetwork installにも依存しない。

## 却下した案

- **現在の `/preview/{port}` をそのまま iframe 化**: 同一 origin の権限が強すぎる。厳格 sandbox は
  SPA の通信・storage を壊し、緩い sandbox は Console を任意コードから隔離できない。
- **generic proxy を WS/SSE/HTML 書換まで強化**: 改善価値はあるが、root path、cookie、service worker、
  CSP、アプリ固有 redirect を完全には吸収できない。開発サーバを無改修で確認する目的の主経路にしない。
- **ペインごとに Chromium プロセス**: 分離は単純だが、メモリ制約のある Workspace で多重プロセスは重い。
  BrowserContext をセキュリティ境界とは見なさず、同一ユーザー Workspace 内の状態分離として使う。
- **Xvfb + noVNC**: ブラウザ chrome や DevTools まで表示できる一方、転送・入力・解像度管理が重く、
  ペインへ Web アプリを表示する用途には過剰。将来の「完全リモートデスクトップ」要件まで保留する。
- **raw CDP を Console へ中継**: Console が強いブラウザ制御権を持ち、プロトコル互換・認可・秘密情報の
  露出面も拡大する。Agent が許可した高水準操作だけを公開する。

## 帰結

- Workspace イメージが大きくなり、Chromium の更新・脆弱性対応がリリース作業に加わる。
- 画面転送は terminal より帯域と CPU を使うため、同時 Page 数・解像度・fps・JPEG 品質を制限し、
  非表示ペインは停止する必要がある。
- 完全なローカルブラウザ互換ではない。MVP は表示、通常入力、navigation、Console log に絞り、
  upload/download、clipboard、音声、動画、DevTools、複数タブは後続とする。
- Page は ephemeral なので Workspace Stop / Agent 再起動 / Chromium crash 後は同じ port/path から再生成する。
  Web アプリ自身の状態永続は対象アプリ側に委ねる。
