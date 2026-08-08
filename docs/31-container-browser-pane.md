# 31. コンテナ内ブラウザペイン — 実装設計

> 状態: **MVP 実装済み・W5 統合検証済み**（2026-07-18）
> 意思決定: [decisions/0018](decisions/0018-container-browser-pane.md)
> 利用契約: [31-container-browser-pane-ux-contract.md](31-container-browser-pane-ux-contract.md)
> 対象: Console / Control Plane / Workspace Agent / Workspace image

## 31.1 目的と受入条件

Workspace 内で開発中の Node（3000、5173 等）や Spring Boot（8080 等）を、アプリ側の base path 設定や
ポート公開なしに Console のペインへ表示し操作する。

MVP の受入条件:

1. メンバーが port と任意の `/` 始まり path を指定してブラウザペインを開ける。
2. Chromium は同じ Workspace 内の `http://127.0.0.1:{port}{path}` へ直接接続する。
3. React/Vite の HMR、Spring Boot の redirect、絶対 `/assets/*` がブラウザ内で通常の localhost と同様に動く。
4. 表示、click、scroll、ASCII/日本語入力、戻る、進む、再読込、path 移動、viewport resize が動く。
5. ペインの swap/split/一時的な別 view 表示で Page を不用意に作り直さず、ペイン削除時には解放する。
6. Console reload、Workspace Stop→Start、Chromium crash 後は、保存された port/path から Page を再生成できる。
7. 別 membership の browserId を参照できず、raw CDP と Agent 内部 port は外部公開されない。
8. Page を表示している接続は Workspace を warm に保つ。非表示・切断済み Page は無期限には pin しない。

非目標（MVP）:

- ローカル Chrome と完全同等の DevTools、拡張機能、profile 同期
- file upload/download、clipboard、drag & drop、音声、動画、WebRTC、複数タブ
- 外部 Web サイトを自由に閲覧する汎用リモートブラウザ
- エージェントによる自動クリックや視覚テスト API（同じ基盤の後続利用候補）
- 既存 `/preview/{port}` の廃止または WS/SSE 対応

## 31.2 全体構成

```text
Console BrowserPane (canvas + hidden IME input)
  │  WSS /ws/browser?id=<browserId>&tenant=<slug>
  │  binary: JPEG frame / text: restricted control messages
  ▼
Control Plane browser relay
  │  membership 解決・Workspace running 検査・接続追跡
  │  WSS <agent-endpoint>/ws/browser?id=<browserId>
  ▼
Workspace Agent BrowserManager
  │  1 Chromium process / Workspace
  │  1 incognito BrowserContext + Page / browserId
  │  CDP Page.startScreencast + Input.* + navigation
  ▼
Chromium ──HTTP/WS/SSE──▶ 127.0.0.1:{port}
```

HTTP アプリ通信は Chromium と対象プロセスの loopback 内で完結する。Console まで中継するのは圧縮済み描画と
許可済み操作だけであり、対象アプリの cookie、Authorization、response body を CP が解釈しない。

## 31.3 コンポーネント境界

### Workspace image

- Chromium binary と現在すでに入っている runtime library/font をイメージに焼き込む。
- amd64 / arm64 の双方で同じ `chromium` 実行名を提供し、ビルド smoke で `chromium --version` を確認する。
- バージョンはイメージ build の入力として追跡し、コンテナ起動時のネットワーク install に依存しない。
- Chromium は非 root の `dev` として動かし、専用の一時 user-data-dir を使用する。永続ホームを既定 profile にしない。
- Debian `chromium-sandbox`のhelperは`root:root 4755`をbuild時に検証する。Docker runtimeはnamespace作成用の
  `SYS_ADMIN`をbounding setへ追加するが、通常の`dev` processにはeffective capabilityを付けず、image内でsetuid/setgidを
  保持する実行ファイルは`chrome-sandbox`だけにする。製品起動ではsandboxを無効化せず、Docker/ECSの小さい`/dev/shm`へ
  依存しないよう`--disable-dev-shm-usage`を製品とsmokeで共通にする。

実装時に Debian package と Playwright 配布 Chromium のサイズ・multi-arch・更新運用をスパイク比較し、
最終選定を同 ADR に追記する。Agent と wire 契約はどちらを選んでも変えない。

### Workspace Agent

`BrowserManager` が Chromium と Page の唯一の所有者になる。

- 最初の `POST /browser/pages` で Chromium を遅延起動する。
- Workspace 当たり Chromium process は 1 個。Page ごとに新しい BrowserContext を作る。
- `browserId` は暗号学的乱数または UUID。port/path を ID に含めない。
- Page map はメモリ上だけに置き、Agent 再起動時には空から始める。
- Page の作成・破棄を直列化し、Chromium crash 時は全 Page を `crashed` に遷移させる。
- raw CDP debugging port を使う場合は `127.0.0.1` のみ。可能なら pipe 接続を優先する。
- Agent 終了時は process group と一時 profile を回収する。
- pipe CDPは1 message 8 MiB、event 256件かつ合計32 MiBを上限とし、security/lifecycle/screencast eventを飽和時に
  goroutineへ逃がさない。必須eventを収容できなければChromiumを停止して全Pageを`crashed`へ遷移させる。

### Control Plane

- REST は通常の Agent proxy と同じ membership / runtime 解決を使う。
- browser WebSocket は terminal と同型の専用 relay とする。CP は text/binary frame をそのまま中継する。
- workspace が stopped / starting の場合は terminal と同じ安定エラーコードで fail-fast し、自動起動しない。
- 接続中は `conns` に browser 接続として加算して Workspace を warm に保つ。Session には紐づけない。
- Agent bearer は CP→Agent handshake のみに付け、Consoleへ露出しない。

### Console

- `PaneContent` に `{kind: "browser"; port: number; path: string}` を追加する。
- `BrowserService` は terminal service と同様に `paneId` keyed の module-level map で controller/socket/canvas を所有する。
- layout から paneId が消えたら即座に Agent Page を DELETE する。単なる view 切替では非表示通知を送り、
  60秒の猶予中は温存する。猶予後の破棄と復帰時の再生成は BrowserService が吸収する。
- layout に `browserId` を保存しない。reload 後は port/path から Page を作り直す。
- Canvas の上に toolbar と IME 用 input を置く。生の対象ページDOMはConsoleに入らない。

## 31.4 公開 API 契約

### Page 作成

```http
POST /api/browser/pages
Content-Type: application/json

{
  "port": 3000,
  "path": "/",
  "viewport": {"width": 1200, "height": 800, "deviceScaleFactor": 1}
}
```

CP は `/api` を除いて Agent `POST /browser/pages` へ転送する。

```json
{
  "id": "8a5d...",
  "port": 3000,
  "url": "http://127.0.0.1:3000/",
  "state": "starting"
}
```

検証:

- port は `1..65535`。Agent 自身の 7700 は拒否する。
- path は `/` 始まり。fragment は許可、userinfo と host を含む absolute URL は拒否する。
- viewport は正整数へ丸め、最大 `1600x1200`、deviceScaleFactor は MVP では `1` に固定する。
- 同時 Page 数は既定 `2`。超過は `429 browser_page_limit`。

安定エラーコード:

| status | code | 意味 |
|--------|------|------|
| 400 | `bad_browser_target` | port/path/viewport が不正 |
| 409 | `workspace_stopped` / `workspace_starting` | Workspace が接続不能 |
| 429 | `browser_page_limit` | Page 上限 |
| 502 | `browser_start_failed` | Chromium を起動できない |
| 502 | `browser_navigation_failed` | Chromium内のnavigationを開始できない |

対象 port がまだ listen していない場合も Page 自体は作り、`target-unreachable` 状態を表示して toolbar の再読込を
許可する。Node/Spring Boot の起動待ちは通常状態であり、Page作成REST自体を失敗させない。

### 状態取得・破棄

```http
GET    /api/browser/pages/{id}
DELETE /api/browser/pages/{id}
```

GET は `{id, port, url, title, state}` を返す。DELETE は冪等にし、存在済みの破棄も未存在も `204` とする。
一覧 API は他タブの Page を不用意に列挙する必要がないため MVP では作らない。

### 描画・操作 WebSocket

```text
GET /ws/browser?id=<browserId>&tenant=<slug>
```

browserId は秘密トークンではない。CPのL1認証、membership、対象Workspace所有権を必ず検証する。Agent側も
自分の Page map に存在する ID だけを受け入れる。

## 31.5 WebSocket wire protocol v1

接続直後、Agent は最初の text message として protocol version を送る。

```json
{"type":"ready","version":1,"url":"http://127.0.0.1:3000/","title":"App","width":1200,"height":800}
```

### Agent → Console

- **binary message**: 1 message = 1 JPEG frame。MVP では独自binary headerを付けず、寸法・URL・titleはtext eventで送る。
- text `navigation`: `{"type":"navigation","url":"...","title":"...","canBack":true,"canForward":false}`
- text `console`: `{"type":"console","level":"error","text":"...","ts":"..."}`
- text `page-error`: uncaught exception。stack はサイズ上限を設ける。
- text `state`: `loading | ready | disconnected | crashed | target-unreachable`
- text `stats`: 任意。実測用の droppedFrames / fps / encodedBytes。通常UIには直接表示しない。

Console log は token/cookie を含む可能性があるのでDBへ永続化・監査ログへ記録しない。接続中の当該ペインへだけ送り、
各messageとリングバッファの長さを制限する。

### Console → Agent

```json
{"type":"viewport","width":900,"height":600,"zoom":2}
{"type":"mouse","event":"move|down|up","x":10,"y":20,"button":"left","buttons":1,"modifiers":0,"clickCount":1}
{"type":"wheel","x":10,"y":20,"deltaX":0,"deltaY":120,"modifiers":0}
{"type":"key","event":"down|up","key":"a","code":"KeyA","modifiers":0,"repeat":false}
{"type":"text","text":"日本語"}
{"type":"navigate","path":"/users/1"}
{"type":"reload","ignoreCache":false}
{"type":"history","direction":"back|forward"}
{"type":"visibility","visible":false}
```

- `viewport.zoom`（省略時 1、上限 4）はピンチズーム。Agentは layout viewport を `base / zoom` に縮めて
  そこからフレームを撮るので、文字は拡大**描画**され、画像を後から引き伸ばすのとは違う。適用後の layout は
  text `viewport` で返し、pointer座標はその空間で解釈する。`deviceScaleFactor` は上げない
  （screencast は CSS pixel サイズのフレームしか出さず emulate した DPR を無視する＝実測。詳細は 53.x）。
- path navigation は `/` 始まりだけを許し、Agentが同じ scheme/host/portのURLへ組み立てる。
- IME composition 中の個別 key は送らず、確定文字列を `text` で送る。
- pointer coordinate はCanvasのCSS pixelをviewportへ換算して送る。
- 未知 type / field は接続を落とさず protocol error を返す。過大messageは切断する。
- v1 は1 Pageにつき同時 viewer 1本。二重接続は後勝ちにせず `409 browser_already_attached` とする。

## 31.6 フレーム制御と資源上限

既定値（実機計測後に調整可能な設定値とする）:

| 項目 | 既定 |
|------|------|
| Workspace 当たり Chromium | 1 process |
| Workspace 当たり Page | 2 |
| viewport | 最大 1600×1200、DPR 1 |
| visible 時 | 最大 12 fps、JPEG quality 70 |
| hidden 時 | screencast停止 |
| frame queue | Page ごとに最新1枚 |
| CDP入力 | 1 message 8 MiB、event queue 256件かつ合計32 MiB |
| viewer 切断後の猶予 | 60秒、その後 Page 破棄 |

CDP eventを無制限にWebSocketへ積まない。Pageごとに容量1のlatest-frame slotを持ち、新しいframe到着時に未送信の
古いframeを置換する。Pageごとにbase64 decoderを1 worker、未処理CDP frameを1件だけ持ち、`Page.screencastFrameAck`を
`1/maxFPS`後まで返さない。ChromiumはACKまで次frameを生成しないため、capture/encode自体を既定12fps以下に制限する。
WebSocket側にも同じ間隔の送信tickerとwrite deadlineを設ける。CDP event queueは件数256・合計32 MiBの双方で固定し、
必須eventがいずれかの上限で飽和した場合はChromiumを停止して全Pageを`crashed`へ遷移し、待機goroutineやメモリを
増やして継続しない。visibility=falseでは`Page.stopScreencast`、再表示で再開する。

Browser接続はWorkspaceをwarmに保つが、非表示通知後またはsocket切断後の猶予Pageはwarm接続として数えない。
Chromium processはPageが0になってから数分のidle timeoutで終了し、次回遅延起動する。

## 31.7 lifecycle と復旧

```text
layoutにbrowser pane追加
  → BrowserService POST pages
  → id取得
  → WS connect
  → ready + screencast

別viewへ切替
  → visibility=false（screencast停止、60秒はPage温存、その後破棄）

同じpaneへ戻る
  → 猶予中: visibility=true（同じPageでscreencast再開）
  → 破棄後: 保存済みport/pathから新Pageを作成

layoutからpaneId消滅
  → WS close → DELETE page → BrowserService dispose

Console reload / Agent restart / Workspace Stop→Start
  → ephemeral idは捨てる
  → layoutのport/pathから新Pageを作る
```

Chromium crash時、Agentは既存WebSocketへ `crashed` を送りPage mapを無効化する。自動で無限再起動せず、Consoleの
「再接続」で新しいPageを作る。短時間に複数回crashした場合は原因とWorkspaceメモリ状況を表示する。

## 31.8 UI

WsBar の既存ポート入力に操作を2つ置く。

- **ペインで開く**: アクティブペインを browser content に切替
- **軽量プレビュー**: 現行 `/preview/{port}` を新規タブで開く

BrowserPane toolbar:

```text
[←] [→] [再読込]  127.0.0.1:[3000] [/path]  [Console] [再接続] [×]
```

- URL欄に外部hostは入力させず、portとpathを分離する。
- loading / target-unreachable / crashed / workspace-stopped をCanvasとは別のoverlayで表示する。
- Console logは件数badgeからペイン内drawerを開く。error/warnを優先し、コピー操作だけ提供する。
- `Ctrl/Cmd+R` 等のアプリ全体ショートカットとの競合は既存 keyboard dispatcher に従う。BrowserPane内の通常キーは
  remote Pageへ送るが、Console予約キーとLeaderはConsoleが先に処理する。
- pointer lock、fullscreen、permission promptはMVPで拒否し、Page側へ失敗を返す。

## 31.9 セキュリティ

### 信頼境界

対象Webアプリは同じユーザーが起動したコードでも**非信頼入力**として扱う。依存パッケージや生成コードが悪意を
持つ可能性がある。ただしChromiumは対象コードと同じWorkspace OSユーザーで動くため、BrowserContextはWorkspace内の
OS分離を強めるものではない。守る境界は次である。

- 対象アプリからConsole origin / DOM / cookieを隔離する。
- raw CDPをConsole、対象アプリ、他membershipへ渡さない。
- ブラウザ機能を使ったWorkspace外・ホスト管理面への到達を増やさない。
- 描画・logに現れた秘密を永続化しない。

### navigation policy

- top-level navigation は loopback HTTP(S) に限定する。Page内リンクによる別loopback portへの遷移は許可するが、
  Agent自身の7700と予約済み管理endpointは除外する。
- subresource / fetch / WebSocket は別loopback portも許可する。frontend `:3000` から API `:8080` を呼ぶ一般的な
  開発構成を壊さないためである。外向きHTTP(S)は既存Workspace egress方針に従い、link-localと管理面は拒否する。
- `localhost` 文字列は名前解決差を避けるためAgentが `127.0.0.1` へ正規化する。
- redirectでloopback外へtop-level遷移する場合は中止し、外部遷移としてUIへ通知する。
- `169.254.169.254`、Docker/Agent/CP管理endpoint、`file:`、`chrome:`、`devtools:` は拒否する。
- 任意の外部top-level URL許可へ暗黙に拡大しない。

対象アプリ自体は同じOSユーザーとして直接network/filesystemへ到達できるため、これは悪意あるWorkspaceプロセスを
sandboxする機能ではない。ブラウザペイン導入によって新たに外部から操作可能になるCDPとConsole境界を守る規則である。

## 31.10 観測性

秘密を含まない次のメトリクスだけを集計候補とする。

- active Chromium process / Page / viewer 数
- Chromium起動時間、Page作成時間
- frame fps、encode bytes、dropped frame数
- crash / start failure / target unreachable 件数
- Pageごとの概算送信byte（URL、title、console本文は記録しない）

監査ログはPage作成・破棄を通常は記録しない。外部通信やファイル変更ではなく一時的なread/interactionであり、入力文字や
URLを監査へ載せる方が秘密漏洩リスクを増やす。将来必要なら「portを開いた」というmetadataだけを別イベントにする。

## 31.11 テスト戦略

### Agent

- target validation、Page上限、DELETE冪等性、所有map、crash cleanupのunit test
- fake CDP transportでinput→CDP変換とframe latest-only/backpressureを検証
- smoke用HTTP+WSページをAgent test内でloopback起動し、Chromiumがある環境だけintegration test
- Chromium無しのunit testを常時CI必須、実browser smokeはimage/E2E jobへ分離

### Control Plane

- membership外、stopped/starting、bad id、Agent bearer、WS text/binary保持をhttptestで検証
- browser viewerがconnection trackerをadd/doneすることを検証
- CPがbrowser payloadをlog/DBへ保存しないことを確認

### Console

- layout migration（browser port/path検証、不正値はblank terminalへfallback）
- sameTarget/open/split/swap/closeとBrowserService reconcileのvitest
- binary JPEG、navigation/state、IME composition、coordinate scaling、visibilityのcomponent test
- Playwright E2E: test serverをWorkspace loopbackで起動し、表示、click、HMR、reload、pane close後cleanupを確認

### image smoke

- `chromium --version`
- 非rootでheadless起動
- 日本語fontを含む固定ページのscreenshot
- amd64 / arm64 buildの成立

## 31.12 段階実装と並列セッション

API/wire契約は本書 §31.4–31.6 を基準に先に固定する。各セッションは現在の専用ブランチで作業し、下記の所有ファイルを
越える変更を避ける。共通ファイル `routes.go` 等の小さな接続変更は各担当内で行い、統合時に競合を解消する。

### W1 — Workspace image（独立）

所有: `workspace/Dockerfile`、`deploy/local/e2e-smoke.sh`、必要なimage smoke。

- Chromium配布方式の小スパイク（Debian package vs Playwright配布）
- multi-arch、非root起動、バージョン表示、image sizeを記録
- Agent実装より先に単独commit可能

### W2 — Workspace Agent（W1と並列）

所有: `workspace/agent/browser*.go`、Agent `routes.go`、Agent tests、必要なGo dependency。

- BrowserManager、Page REST、CDP adapter、WS protocol、resource/lifecycle
- unit testはfake CDPで進め、実Chromium smokeだけW1統合後に実行
- 公開型を内部CDP型から分離し、CDP library選定をCP/Consoleへ漏らさない

### W3 — Control Plane（W1/W2/W4と並列）

所有: `control-plane/browser*.go`、CP `routes.go`、CP tests。

- REST route、browser WS relay、membership/runtime gate、connection tracking
- fake Agent WebSocketで単独検証
- terminal relayの共有化はこの段階では行わず、必要なら後続refactorに分ける

### W4 — Console（W1/W2/W3と並列）

所有: `console/src/features/browser/`、layout型/migration/ops、`Pane.tsx`、WsBar、i18n、Console tests。

- browser content、BrowserService、Canvas/toolbar/input、状態UI
- mock REST/WSと固定JPEGでバックエンド無しにcomponent test
- terminalのpaneId不変条件を崩さず、browser controllerも同じreconcile思想に合わせる

### W5 — 統合（W1〜W4後）

所有: 横断E2E、`docs/dev/*` 現行契約更新、guide、必要な設定・メトリクス。

- 各commitを設計commitの子孫へ統合
- Node HMR fixtureとSpring Boot相当redirect/absolute asset fixtureで実機確認
- cgroupメモリ、fps、帯域を計測して§31.6既定値を確定
- Console build、Go test/vet、image smoke、Playwright E2E
- 実装済み範囲に合わせADR状態と本書の未実装表示を更新

並列開始前にこの設計commitを全セッションの共通baseにする。W2/W3/W4は互いの未完成コードを参照せず、ここに記載した
JSONとWebSocket messageだけを契約としてfakeで進める。これにより待ち合わせをW5の一度に限定する。

## 31.13 実装決定と残る実機調整

1. Chromium配布はamd64/arm64で同じ実行名を持つDebian `chromium`を採用し、Debian revisionまで固定した。
2. AgentのCDP clientは外部libraryへ公開型を依存させず、`--remote-debugging-pipe`の最小adapterを実装した。
3. screencastはPageごとのACK pacingによりcapture/encodeを最大12fps、quality 70にした。2 Page同時の
   アニメーションfixtureで各Pageのframe数が上限内であることをsmokeする。完成イメージのCPU/帯域実測で下げる可能性がある。
4. viewer切断猶予は60秒、Chromium idle timeoutは120秒を既定にした。いずれも環境変数で調整できる。
5. target未listenはPageを保持したまま`target-unreachable`へ遷移し、reloadで復旧できる実装とした。

未決事項を理由に REST path、JSON field、WebSocket message名を各担当が独自変更しない。契約変更が必要になった場合は、
本書を先に更新して全担当へ共有する。

## 31.14 W5 統合結果（2026-07-18）

W1〜W4を設計commitの子孫である統合ブランチへ順番に取り込み、公開REST/WS契約を実装同士で照合した。
追加したopt-inライブテストは、実Chromiumを使うWorkspace Agent、Control Plane relay、Console
`BrowserController`を同時に起動し、Page作成、v1 `ready`、JPEG frame、日本語入力、対象ページのconsole event、
Page破棄を往復させる。通常のunit suiteでは環境依存を避けてskipし、次で明示実行する。

```bash
AF_BROWSER_LIVE_E2E=1 AF_BROWSER_LIVE_ALLOW_NO_SANDBOX=1 AF_CHROMIUM_BIN=/path/to/playwright-chromium \
  go -C control-plane test -run '^TestBrowserLiveW2W3W4$' -count=1
```

上記の`ALLOW_NO_SANDBOX`はsetuid helperを持たないローカルPlaywright版へテスト専用CDP factoryを注入する印で、
製品`launchPipeCDP`にはno-sandbox切替自体を持たせない。完成Workspaceイメージの`deploy/local/e2e-smoke.sh`は製品Docker
runtimeと同じ`--init --memory ... --cap-add=SYS_ADMIN`を使い、`dev(1000)`、`root:root 4755` helper、helper以外の
setuid/setgidなし、`NoNewPrivs=0`、devの`SYS_ADMIN` effectiveなし/helperのbounding setにはあり、製品と同じpipe CDP/
`--disable-dev-shm-usage`、2 Page同時描画を必須検証する。

統合時の検証結果:

| 対象 | 結果 |
|------|------|
| Workspace Agent | `go test ./...` 309件、`go test -race ./...`、`go vet ./...`、`gofmt`、実Chromium 1/2 Page smokeが成功 |
| Control Plane | `go test ./...` 147件、`go test -race ./...`、`go vet ./...`、`gofmt`が成功 |
| Console | vitest 36 files / 322件、`tsc --noEmit`、production buildが成功 |
| W2↔W3↔W4ライブ結線 | REST作成/破棄、WS text/binary、JPEG、状態、console、日本語入力の往復が成功 |

このWorkspaceにはDocker互換runtime/socketがないため、完成Workspaceイメージのamd64/arm64 build、Debian setuid sandbox、
非root起動、日本語font smokeの実行結果はimage CI待ち。smoke自体はこれらを必須条件として失敗するよう更新した。
Node HMRとSpring Boot相当redirect/absolute assetを完成イメージで
確認するフルE2E、cgroupメモリ・fps・帯域の実機計測も同じCI/実フリート検証へ残す。現在の12fps、quality 70、
Page上限2、detached grace 60秒は設計既定のままとする。
