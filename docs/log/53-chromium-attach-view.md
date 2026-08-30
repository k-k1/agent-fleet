# 53. Chromium Attach View — 外部所有 Chromium の表示・人間操作

> 状態: **P0実測・実装契約確定、P1〜P3実装済み**（2026-08-02。P3の対話セッション直接経路を同日補正）
> 意思決定: [decisions/0038](../decisions/0038-chromium-attach-view.md)
> P0実測: [53-chromium-attach-view-p0-verification.md](53-chromium-attach-view-p0-verification.md)
> 関連: [31-container-browser-pane.md](31-container-browser-pane.md) / [49-mcp-2026-07-28.md](49-mcp-2026-07-28.md)
> 対象: Console / Control Plane / Workspace Agent / ローカル stdio MCP / Workspace 利用契約

## 53.1 目的

Workspace 内で Playwright 等の任意プロセスが所有する headless Chromium の既存 Page を、Agent Fleet の
Console ペインへ表示し、人間が click・scroll・keyboard・IME 入力を行えるようにする。典型例は、自動処理が
フォーム入力まで進め、公開・送信・同意等の最終操作だけを人間へ引き渡す半自動ワークフローである。

この機能は特定スクリプトをAFへ移植するものではない。外部プロセスがブラウザと業務ロジックを所有し、AFは
既存 Page への一時的な表示・入力経路と、エージェントからユーザーへ引き渡す制御面だけを提供する。

受入条件:

1. Chromium を所有するプロセスは、AFをライブラリとして組み込まずにCDP endpointをloopbackで公開できる。
2. エージェントはローカルAF MCPでPageを列挙し、対象をattachし、操作用リンクをユーザーへ提示できる。
3. ユーザーはリンクを1回クリックして、対象PageをConsoleペインに表示できる。
4. ユーザーは既存ブラウザペイン相当のpointer・wheel・keyboard・日本語入力を行える。
5. detachはAFの観測・入力経路だけを閉じ、外部所有のPage、BrowserContext、Chromium processを終了しない。
6. raw CDP endpoint、cookie、credentialをCPまたはConsoleへ公開しない。
7. 通常のlocalhostブラウザペインの外部top-level navigation禁止を緩めない。

非目標:

- Firefox / WebKit / 任意GUIアプリのリモートデスクトップ化
- raw CDP、DevTools、DOM、Network、StorageのConsole公開
- AFによる業務フォームの自動入力や最終確定操作
- Chromiumをremote debugging無しで後付け検出すること
- 複数ユーザーによる同一Pageの同時共同操作
- 外部自動化プロセスと人間入力の競合をAFだけで完全に解決すること

## 53.2 用語と所有権

| 用語 | 意味 |
|------|------|
| owner | Chromiumを起動し、Pageと業務処理を所有するPlaywright等の外部プロセス |
| CDP endpoint | ownerが`127.0.0.1:<port>`だけに公開するChrome DevTools Protocol endpoint |
| target | CDPが列挙する既存の`type=page` target |
| attachment | Workspace Agentがtargetへ一時接続して保持する表示・入力セッション |
| viewer | attachmentを表示しているConsoleの1ペイン |
| handoff | 自動処理から人間操作へ制御を引き渡す任意の協調状態 |

所有権は常にownerにある。AFはattach時にtargetへCDP sessionを追加するだけで、次を行わない。

- Chromiumの起動・再起動・終了
- targetの作成・閉鎖
- BrowserContextやprofileの削除
- ownerが持つPlaywright接続の切断

## 53.3 全体構成

```text
Playwright / 任意スクリプト (owner)
  │ launch Chromium --remote-debugging-address=127.0.0.1
  │                 --remote-debugging-port=<port>
  │ connect_over_cdp / 通常の自動操作
  ▼
headless Chromium ──外部Webサイト
  ▲
  │ CDP over loopback（Agentだけが接続、CPへは出さない）
  │
Workspace Agent AttachmentManager
  │ Target discovery / attachToTarget / startScreencast / Input.*
  │ attachmentIdごとの状態・viewer lease・handoff結果
  ▼
Control Plane browser-attachment relay
  │ membership検証、REST/WS中継。CDPを解釈しない
  ▼
Console BrowserAttachPane
  │ JPEG canvas + pointer/keyboard/IME + 操作完了/中止
  ▼
ユーザー
```

既存`BrowserManager`のscreencast、入力wire、frame backpressure、Console canvasを再利用する。ただし所有権と
navigation policyが異なるため、Agent内部ではAF所有Pageとattachmentの型・lifecycleを混同しない。

## 53.4 Chromium 側の起動契約

外部プロセスはremote debuggingをloopbackだけに公開する。**portは固定せず`0`で起動し、Chromiumが選んだ実portを
`<user-data-dir>/DevToolsActivePort`から読む**（理由は§53.16。固定portは他セッションと衝突しても失敗せず、
黙って別のブラウザへattachする事故になる）。

```sh
chromium \
  --headless=new \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=0 \
  --user-data-dir=/explicit/profile/path

# 1行目 = 実port、2行目 = /devtools/browser/<GUID>（= browser_id）
cat /explicit/profile/path/DevToolsActivePort
```

- 1行目のportを`list_chromium_targets`へ渡し、2行目のGUIDを`attach_chromium`の`expected_browser_id`へ渡す。
  後者が一致しないattachは`cdp_browser_mismatch`で拒否される。
- portは`1..65535`で、Agent自身のportを使わない。
- `--remote-debugging-address=0.0.0.0`を利用契約で禁止する。
- Chromiumの新しいremote-debugging制約に備え、明示した非デフォルト`user-data-dir`を使う。
- ownerはCDP endpointがlistenしてからAF MCPを呼べる状態にする。
- Playwrightがownerの場合は、同じendpointへ`connect_over_cdp`してもよい。AFは別CDP clientとして接続する。
- Playwright固有の`launch_server` WebSocketはCDPではないため対象外とする。

AFはremote-debugging port無しの既存process、CDP pipe、Unix socketを探索しない。pipeは起動した親だけが所有するため、
汎用の後付けattach契約にしない。

## 53.5 Workspace Agent

### AttachmentManager

`AttachmentManager`をWorkspace当たり1つ持ち、既存`BrowserManager`と共通の低水準CDP client・screencast・入力変換を
再利用する。

- 接続先hostは常に`127.0.0.1`。API/MCP入力でhost、scheme、CDP WebSocket URLを受け取らない。
- `GET http://127.0.0.1:<port>/json/version`と`/json/list`を短いtimeoutで取得する。
- discovery応答のWebSocket URLからpathだけを取り出し、dial先host/portは入力検証済みloopbackへ再構成する。
- `type=page`だけを広告する。extension、service_worker、background_page等は除外する。
- attach時はbrowser WebSocketから`Target.attachToTarget(flatten=true)`し、target sessionで既存wireに必要な
  `Page`、`Runtime`、`Network`、`Log`、`Input`だけを利用する。
- raw CDP messageとWebSocket URLをREST、MCP、log、notificationへ出さない。
- attachmentは暗号学的乱数の`attachmentId`で参照し、memory上だけに保持する。
- Agent再起動時はattachmentを復元しない。ownerとtargetには影響しない。

P0でtransportの分割点を確定した。現行`browserCDP`の`Call / Events / Done / Close`をmanager境界として維持し、
request ID/pending応答とbounded event queueを共通coreにする。pipeのNUL framingとWebSocket framingはtransport adapterへ分け、
process/profile所有はpipe adapterだけに残す。`browserCDPEvent.queue *pipeCDP`は共通queue参照またはrelease callbackへ変える。
discovery、loopback URL再構成、target filterは上位の`AttachmentManager`に置き、transportへ入れない。詳細は
[P0実測 §3.1](53-chromium-attach-view-p0-verification.md#31-cdp-transportの分割点)による。

### navigation policy

AF所有Pageのloopback限定は維持する。attachmentでは既にownerが外部URLを開いているため、外部top-level URLを理由に
元URLへ強制navigationしない。toolbarからの任意URL入力は提供せず、戻る・進む・再読込だけを許可する。

targetが別originへ遷移しても同じtargetIdなら表示を継続する。`file:`、`chrome:`、`devtools:`等へ遷移した場合は
描画を停止して`unsupported-target-url`とする。通常のHTTP(S) resourceはWorkspaceのegress policyに従う。

### lifecycle

```text
discover ──副作用なし──▶ target一覧
attach ────────────────▶ attached（まだviewer無し）
Console link click ─────▶ viewer-open / screencast開始
pane非表示 ─────────────▶ screencast停止、attachmentは保持
操作完了/中止 ─────────▶ handoff結果を記録、ownerは継続
detach/TTL/target消滅 ──▶ CDP sessionとviewerだけ解放
```

- attachment作成後、viewer未接続のTTLは既定10分。
- viewer切断後の再接続猶予は既定60秒。handoff結果待ちではattachment自体のTTLを既定30分とする。
- visible viewerがある間だけWorkspaceのwarm connectionとして数える。
- detachは冪等で、存在しないIDにも`204`を返す。
- target消滅、CDP切断、owner終了はそれぞれ`target-closed`、`disconnected`状態にする。ownerを再起動しない。
- v1は1 attachmentにつきviewer 1本。二重viewerは`409 browser_already_attached`とする。
- Chromium自体は複数CDP sessionを許すが、v1のAFは同じtargetへactive attachmentを1つだけ許す。ownerが既に持つ
  Playwright/CDP sessionはこの上限に数えない。

## 53.6 Agent / CP API

CP公開APIは通常のmembership解決とWorkspace runtime解決を通し、対応するAgent内部APIへ中継する。

### target discovery

```http
GET /api/browser/attach-targets?port=9222
```

```json
{
  "targets": [
    {
      "targetId": "opaque-cdp-target-id",
      "type": "page",
      "title": "エピソード編集",
      "url": "https://example.invalid/edit"
    }
  ]
}
```

title/urlは認証済みユーザーとエージェントには見せるが、監査log・notification本文には保存しない。URLのqueryやtitleに
秘密が入る可能性があるためである。

### attach

```http
POST /api/browser/attachments
Content-Type: application/json

{
  "port": 9222,
  "targetId": "opaque-cdp-target-id",
  "browserId": "c162d83f-b0a3-41d3-9db6-e9f6012c1491",
  "viewport": {"width": 1280, "height": 900, "deviceScaleFactor": 1}
}
```

`browserId`は任意（後方互換）だが、自分で起動したChromiumへ繋ぐなら必ず渡す。`DevToolsActivePort`2行目のGUID、
`/devtools/browser/<GUID>`、advertised WebSocket URLのいずれの形でも受ける。endpointが別個体なら
`cdp_browser_mismatch`で拒否する。discovery (`GET /api/browser/attach-targets`) は同じ識別子を
`browserId`として返すので、attach前に突き合わせられる。

```json
{
  "id": "ba_7f3...",
  "state": "attached",
  "title": "エピソード編集",
  "url": "https://example.invalid/edit",
  "openUrl": "/open/browser-attachment/ba_7f3...",
  "expiresAt": "2026-08-01T12:30:00Z"
}
```

`openUrl`はCDP endpointへのURLではなく、Console action routeである。絶対URLが必要なMCP clientにはCPが
`PUBLIC_BASE_URL`を基に組み立てる。IDだけで認可せず、通常のConsole認証、membership、Workspace一致を毎回検証する。

### status / detach / handoff

```http
GET    /api/browser/attachments
GET    /api/browser/attachments/{id}
DELETE /api/browser/attachments/{id}
POST   /api/browser/attachments/{id}/control-mode
POST   /api/browser/attachments/{id}/handoff
POST   /api/browser/attachments/{id}/handoff-result
```

control mode変更:

```json
{"controlMode":"view-only"}
```

これは現在のattachmentのmodeだけを変更する。既存handoffのmessage/result/controlMode、expiry、viewer leaseは変更せず、
handoffを`pending`へ戻さない。`locked`への変更はscreencastを停止し、visible viewerで`view-only`または
`user-control`へ変更するとscreencastを再開する。

handoff作成:

```json
{
  "message": "内容を確認し、問題なければ最終確定ボタンを押してください",
  "completionLabel": "操作完了",
  "allowCancel": true,
  "controlMode": "user-control"
}
```

handoff結果:

```json
{"result":"completed"}
```

`result`は`pending | completed | cancelled`。これはユーザーの自己申告であり、外部サイト上の処理成功をAFが保証する証拠ではない。
ownerまたはエージェントは必要なら遷移先URLや業務側状態を別途検証する。

安定エラーコード:

| status | code | 意味 |
|--------|------|------|
| 400 | `bad_cdp_port` | port不正、Agent port、またはloopback以外 |
| 400 | `bad_browser_attachment` | attach requestのtargetIdまたはviewportが不正 |
| 400 | `bad_browser_handoff` | handoff messageまたはcompletionLabelが不正 |
| 400 | `bad_control_mode` | controlModeが定義外 |
| 400 | `bad_handoff_result` | handoff resultが`completed/cancelled`以外 |
| 404 | `cdp_target_not_found` | targetが存在しない |
| 409 | `cdp_port_ambiguous` | 同じportを2つ以上のプロセスがlistenしている（§53.16） |
| 409 | `cdp_browser_mismatch` | portに居るChromiumが`browserId`の個体ではない |
| 404 | `browser_attachment_not_found` | attachment不存在、期限切れ、Agent再起動 |
| 409 | `browser_already_attached` | viewer上限または同一targetの競合 |
| 409 | `browser_handoff_not_pending` | handoff作成前に結果を確定しようとした |
| 422 | `cdp_endpoint_invalid` | Chromium CDPとして応答しない |
| 502 | `cdp_unreachable` | endpointへ接続不能 |
| 502 | `cdp_disconnected` | owner終了等で切断 |

描画・入力WebSocketは既存namespaceへ混在させず、Console→CP、CP→Agentとも
`GET /ws/browser-attachments?id=<attachmentId>`を使う。CPの認証・membership検査とlatest-frame relay coreは既存browser
relayから再利用する。Agent handlerを分け、attachmentの許可message集合に`navigate`を定義しない。これにより現行
`/ws/browser?id=<browserId>`の所有権、lookup、wire v1を変更しない。

### P1 Lane A 確定API契約

2026-08-02のLane A実装で、Console/MCPが依存する公開形を次に固定した。CP公開pathの`/api`を除いた同形の
`/browser/attach-targets`、`/browser/attachments...`がAgent内部pathであり、CPはbodyを解釈・永続化せず中継する。

- `attachmentId`: `ba_` + 暗号学的乱数16 byteのlowercase hex 32文字。例:
  `ba_0123456789abcdef0123456789abcdef`。IDは認証の代わりではない。
- action URL: 常に相対path `/open/browser-attachment/{attachmentId}`。port、targetId、CDP URL、外部URLを含めない。
- viewport: `{width,height,deviceScaleFactor}`。上限`1600x1200`、scaleは`1`だけを許可する。attachでviewport全体を
  省略した場合は`1280x900@1`を使う。
- timestamp: JSONではUTC RFC 3339。viewer lease中のようにexpiry timerが停止中なら`expiresAt`を省略する。

REST response型は次である。discovery以外の成功responseはattach/status/handoff/handoff-resultで同じ
`BrowserAttachment`型を返す。DELETEだけはbody無しの`204`で、未知IDにも同じく`204`を返す。

```ts
type BrowserAttachTarget = {
  targetId: string
  type: "page"
  title: string
  url: string
}
type BrowserAttachTargetsResponse = { targets: BrowserAttachTarget[]; browserId?: string }

type BrowserAttachmentState =
  | "attached"               // viewer無し、再接続可能
  | "viewer-open"            // viewer lease有り
  | "unsupported-target-url" // file/chrome/devtools等のため描画停止
  | "target-closed"           // target/session消滅
  | "disconnected"            // browser WebSocket/owner消滅

type BrowserAttachmentControlMode = "view-only" | "user-control" | "locked"
type BrowserAttachmentHandoffResult = "pending" | "completed" | "cancelled"
type BrowserAttachmentHandoff = {
  message: string
  completionLabel: string
  allowCancel: boolean
  controlMode: BrowserAttachmentControlMode
  result: BrowserAttachmentHandoffResult
}
type BrowserAttachment = {
  id: string
  state: BrowserAttachmentState
  title?: string
  url?: string
  openUrl: string
  expiresAt?: string
  viewer: boolean
  controlMode: BrowserAttachmentControlMode
  handoff?: BrowserAttachmentHandoff
}
```

request型とHTTP statusは次に固定する。

| method / path | request | success |
|---------------|---------|---------|
| `GET /api/browser/attach-targets?port={1..65535}` | なし | `200 BrowserAttachTargetsResponse` |
| `POST /api/browser/attachments` | `{port,targetId,viewport?}` | `201 BrowserAttachment` |
| `GET /api/browser/attachments/{id}` | なし | `200 BrowserAttachment` |
| `DELETE /api/browser/attachments/{id}` | なし | `204` |
| `POST /api/browser/attachments/{id}/control-mode` | `{controlMode:"view-only"\|"user-control"\|"locked"}` | `200 BrowserAttachment` |
| `POST /api/browser/attachments/{id}/handoff` | `{message,completionLabel?,allowCancel?,controlMode?}` | `200 BrowserAttachment` |
| `POST /api/browser/attachments/{id}/handoff-result` | `{result:"completed"\|"cancelled"}` | `200 BrowserAttachment` |

handoffの省略値は`completionLabel="操作完了"`、`allowCancel=false`、`controlMode="user-control"`である。
`user-control`を要求するcallerは、そのrequestより前にowner側の対象Pageへの自動操作を停止しなければならない。
AFは停止済みかを検知できず、未停止時の入力結果は未定義である。`completed/cancelled`確定後は`locked`へ移る。

専用WebSocketのwire versionは`1`である。接続直後のtext `ready`は
`{type:"ready",version:1,state,url,title,width,height,controlMode,handoff}`、frameはbinary JPEGとする。
`viewport`（`{width,height,fit?,zoom?}`）、`visibility`、`copy`は全modeで受理する（`locked`と
`unsupported-target-url`では`copy`だけ`input_not_allowed`）。`mouse`、`wheel`、`key`、`text`、`reload`、`history`は
`user-control`だけで受理し、他modeではtext
`{type:"protocol-error",code:"input_not_allowed",message}`を返す。`navigate`はこのnamespaceのmessage型に存在せず、
`unknown_type`となる。Agentからの他のtext eventは既存wireと同じ`state`、`navigation`、`console`、`page-error`に加え、
`{type:"handoff",handoff,controlMode}`、`{type:"control-mode",controlMode}`、
`{type:"viewport",width,height}`（zoom時のlayout viewport通知）、`{type:"clipboard",text}`を使う。

**mouseのdrag（2026-08-08 修正）**: `mouse`の`move`は押下中のbuttonを載せる。`button:"none"`のmoveはBlinkでは
単なるhoverなので、スクロールバーのつまみ・テキスト選択・スライダーなど**あらゆるdragが無反応**だった
（実測: `none`でscrollX 0のまま、押下buttonを載せると448へ）。

**zoom-to-fit**: `viewport`に`fit:true`が付くと、Agentは`Page.getLayoutMetrics`でページ自身の内容幅を測り、
内容がpaneより広い場合だけ**layout viewportをその幅まで広げる**。縮小は`Page.startScreencast`の
`maxWidth/maxHeight`（= pane）が行うのでframeはpane相当のままで、帯域は増えない。layout viewportはpaneと
同じ縦横比を保つ（canvasがpaneいっぱいに引き伸ばすため、比が変わると歪む）。上限はscreencastの1600x1200とは別で、
layout側は4000x4000。pointer座標はlayout空間なので、Agentは新しいサイズを`viewport` textでviewerへ返す。

**pinch zoom（§53.19）**: `viewport`の`zoom`（省略時1、上限4）は上のbase — `fit`後のlayout、fitなしならpane —
をさらに`base / zoom`へ縮める。`fit`と直交し、view-onlyでも受理する（見えているものの見え方はページ入力ではない）。

`Emulation.setDeviceMetricsOverride`の**`scale`は使わない**。Chrome 151で実測したところ、これは出力画像を
縮めるのではなく**同じ大きさの面の中でページだけを縮めて描く**（ページが左上に小さく描かれ、右と下が空白になる）。
さらにpointer座標はlayout空間のままなので、見えている位置と入力位置が`scale`分ずれ、スクロールバーを掴めない・
ホイールが効かないという形で現れる。frame寸法だけを見る検証はこれを通してしまうため、テストは
**frameの画素**（ページの左端・右端の色）で確認する。

**キーイベント**: `Input.dispatchKeyEvent`には`windowsVirtualKeyCode`/`nativeVirtualKeyCode`を必ず載せる。
これが無いとページはkey eventを受け取れてもBlinkが既定動作を行わず、**矢印・PgUp/PgDn・Home/End・spaceで
スクロールせず、textarea内のキャレットも動かない**（文字入力は`text`で届くので無事に見え、原因が分かりにくい）。
textを持たないdownは`rawKeyDown`とする。ホイールはConsole側で`deltaMode`をpixelへ正規化してから送る
（lines/pagesのまま転送すると数px しか動かない）。

**clipboard**: コンテナ内Chromiumのclipboardはユーザーの手が届かないので、`copy`で選択文字列をwireで返し、
Consoleが利用者のclipboardへ書く。Agentが評価するのは固定の読み取り専用式（`document.getSelection()`、
focus中のinput/textareaがあればその選択範囲）だけで、結果はlogにも永続化にも出さない（§53.10）。
ペーストは逆に**転送しない**: Ctrl+Vは隠しinputのnative pasteへ通し、`paste`イベントの文字列を`text`として送る。

terminal state (`target-closed` / `disconnected`) は短い再確認猶予中だけstatusで取得でき、その後は
`browser_attachment_not_found`になる。いずれのdetach/expiry/terminal経路も
`Target.closeTarget`、`Target.disposeBrowserContext`、`Browser.close`を外部ownerへ送らない。

## 53.7 Console とワンクリック導線

### action link

エージェントがMCP結果の`openUrl`を変更せずMarkdownリンクとして提示する。

```markdown
[ブラウザを開いて操作する](/open/browser-attachment/ba_7f3...)
```

このリンクは**別タブではなくこのタブのペイン**で開く。到達経路は2つあり、どちらも同じ`openBrowserAttachment()`
（`console/src/features/browser/attachmentAction.ts`）を通す:

- **ミラー内のクリック**: MarkdownViewが`(^|/)open/browser-attachment/<id>`を判定し、`preventDefault()`して
  その場でペインを開く（ページ遷移しない）。同一originの絶対URL形も受ける。この判定は**repo相対ファイルリンクの
  分岐より前**に置く。leading `/`のpathはファイル解決側が「repoルート相対」として食べてしまい、クリックが
  「`repos/<repo>/open/browser-attachment/<id>` が見つかりません」で終わるため（2026-08-08に発生した実障害）。
- **action routeへの遷移**: 別タブ・URL直打ち・リロード。CPは`GET /open/browser-attachment/{id}`（と末尾`/`）へ
  Console shell (`index.html`) を返す。静的catch-allだけでは404になり、リンク導線が全滅する。session gateは
  他のConsole面と同じで、未ログインなら`/login?next=…`を経由して戻る。

クリック時にConsoleは:

1. routeのattachmentをGETし、認証・membership・有効期限を確認する。
2. 同じattachmentを表示中ならそのpaneをactiveにする。次にactiveなblank、layout順のblankを使う。
3. blankがなく上限未満なら、desktopは右列→下split、mobileは下splitの順で新しいslotを作る。
4. desktop 8 pane / mobile 2 paneの上限時だけ「現在のペインで開く / キャンセル」を表示し、既定focusを
   「キャンセル」にする。黙って別paneを上書きしない。
5. layout storeの正規の`commit()`経路で`{kind:"browserAttach", attachmentId}`を1回だけ設定する。
6. 成功後だけaction routeをhistoryから正規Workspace URLへ置換し、再実行を防ぐ。

`GET /api/browser/attachments`は生存中のattachmentを新しい順に返す（`{"attachments": BrowserAttachment[]}`）。
Consoleの「接続中のブラウザ」（WSバーのプレビュー）だけが読む復旧用の入口で、リンクを見失った後やペインを
閉じた後に戻れるようにする。open_urlを配る第二の経路ではなく、pushもpollingもしない。

サーバーpushやMCP呼び出しだけで利用者のlayoutを勝手に変更しない。layoutはブラウザ端末ごとのlocal stateであり、
ユーザークリックが「この端末で表示する」という明示的な意思になる。

`attachmentId`はCDP credentialではないが、layout/localStorageに保存された場合に期限切れ後は再attachせず、専用の
「操作画面の有効期限が切れました」表示にする。port、targetId、外部URL、CDP WebSocket URLはlayoutへ保存しない。

### BrowserAttachPane

既存BrowserPaneのcanvas、IME、pointer/wheel/key変換、resize debounce、console drawerを共用する。差分は:

- toolbarにhost/path入力を出さない。
- 「幅に合わせる」トグル（attachmentは既定ON）と「選択をコピー」を出す。既定ONなのは、attachの相手は
  responsive前提の自前アプリではなく他人のデスクトップサイトで、pane幅そのままだと右が切れるため。
  コンテナ内ブラウザペイン（自前アプリのプレビュー）は従来どおり等倍のまま。
  このトグルはpinch zoomのリセットも兼ねる（§53.19）。
- titleとoriginだけを表示し、URL全文のqueryは既定で隠す。
- `view-only | user-control | locked`を明示する。
- handoff messageと「操作完了」「中止」をPage外のConsole chromeに表示する。
- detachは「表示を閉じる」と表記し、Chromiumを終了しない旨をtooltipで示す。

## 53.8 ローカル stdio MCP

`workspace-agent mcp-stdio`には、認可前提が異なる次の2つの利用面がある。Chromium Attach Viewは両面へ出すが、
引数と広告集合を混ぜない。CPの対外MCPへは同時公開せず、own Workspaceのlocal stdio MCPだけを対象とする。

| 利用面 | 起動引数 | 広告集合 |
|---|---|---|
| アシスタントチャット | `mcp-stdio` / `mcp-stdio --write --conv <id>` | read 2種。`af_write`時だけprivileged 5種も追加 |
| 対話セッション builtin `af` | `mcp-stdio --self-report --chromium-attach` | `af_report`とChromium 7種だけ。他のフリートread/writeは出さない |

`--self-report`単独は後方互換として`af_report` 1本だけを広告する。`--chromium-attach`は`--self-report`との
組合せでだけ有効であり、単独指定やアシスタント面の権限を拡張しない。セッション面では`tools/list`だけでなく
`tools/call`側も同じ許可集合で検査し、`list_my_sessions`や`send_to_session`等を名前推測してもAgent RESTへ
到達させない。「advertised set IS the scope boundary」というdocs/51の境界を維持したまま、その内側へ
Chromium Attach Viewだけを明示的に加える。

### Chromium read tools

#### `list_chromium_targets`

入力: `{port}`。targetの`targetId/title/url`を返す。対象Pageを選ぶ前に呼ぶ。

#### `get_chromium_attachment`

入力: `{attachment_id}`。state、viewer有無、handoff結果、期限を返す。URL全文は必要な場合だけ返す。

### Chromium privileged tools（アシスタント面では`af_write`）

#### `attach_chromium`

入力: `{port, target_id, label?}`。attachmentを作成し、`open_url`を返す。tool descriptionに次を強制する。

- 先に`list_chromium_targets`でtargetを確認する。
- 戻った`open_url`を改変せず、ユーザーへMarkdownリンクとして提示する。
- 最終確定をエージェント自身でクリックしない。
- attach成功をPage上の業務処理成功と言い換えない。

#### `request_browser_action`

入力: `{attachment_id, message, completion_label?, allow_cancel?, control_mode?}`。handoffを作成・更新する。

#### `get_browser_action_result`

入力: `{attachment_id}`。handoffの自己申告結果を返す。読み取りだが、handoffを作成できるscopeだけに見せるため
アシスタント面では`af_write`集合に置く。

#### `set_chromium_control_mode`

入力: `{attachment_id, control_mode}`。`view-only` / `user-control` / `locked`を変更する。

#### `detach_chromium`

入力: `{attachment_id}`。AFの接続だけを終了する。ownerのPage/processを閉じない。

`attach_chromium`は認証済みPageの描画を別surfaceへ出し、入力経路も作るためread-onlyではない。現行MCPと同じく
アシスタント面では`--write`時だけツールを広告し、名前を推測したcallも拒否する。detachも共有状態を変えるため
同じgateに置く。対話セッション面ではフリート全体の`--write`を立てず、`--chromium-attach` capabilityだけで
この5種を許可する。

アシスタントチャットの`af_write`は承認UIなしのheadless turnへフリート操作を渡すため、利用者の明示opt-inを
必要とする。一方、対話セッションは既に同じWorkspaceでshell、Playwright、loopback CDPを扱う主体であり、
Chromium change toolは外部サイトの確定操作を代行せず、一時的なattachment/handoff状態だけを変更する。
Console表示にもmembership認証とaction URLのユーザークリックが残る。そのためセッション側に追加の設定opt-inは
設けず、常設builtinの狭いcapability flagを認可境界とする。各CLIのnative MCP設定・承認挙動は変えず、
アシスタントチャットにだけ必要なheadless pre-approvalをセッション面へ追加しない。

MCP結果はtoolごとの`outputSchema`を定義し、text content内の短いJSONと`structuredContent`へ同一値を必ず返す。
`attach_chromium`は少なくとも`attachment_id/open_url/expires_at`を両方へ含め、field名は`snake_case`に固定する。
P0ではClaude/Codex/Copilotがstructured値をモデルへ渡し、opencode/Cursor/Kiroはtextだけを渡した。未計測CLIを含めて
text fallbackを正とし、client別分岐は作らない。resultの確定形と実測matrixは
[P0実測 §3.4](53-chromium-attach-view-p0-verification.md#34-mcp-structured-result)による。

### 完了通知

MVPは`get_chromium_attachment`で結果を確認できれば成立する。短周期の無限pollingは指示しない。次段階でhandoff結果を
会話へlevel-drivenに報告し、既存のsession report同様に再接続やAgent再起動で通知を失わない設計を追加する。
attachment自体はmemory上でよいが、通知を保証する段階ではhandoff ledgerだけを永続化する。

## 53.9 CLAUDE.md / AGENTS.md 利用契約

Workspace共通の案内には次の短い契約を載せる。プロジェクト固有の「どの操作を人間へ残すか」は各repoの
`CLAUDE.md` / `AGENTS.md`へ置く。この案内を実行できるよう、CLIを持つ対話セッションにはmcpregのbuiltin `af`が
前節の狭いツール集合をmaterializeする。

```markdown
## ヘッドレスChromiumをユーザーへ引き渡す

コンテナ内の自動処理が人間によるブラウザ確認・操作を必要とする場合:

1. Chromiumを`--remote-debugging-address=127.0.0.1`と
   `--remote-debugging-port=<port>`付きで起動する。0.0.0.0へ公開しない。
2. AF MCPの`list_chromium_targets`で対象Pageを確認する。
3. `user-control`へ移す前に、owner側の対象Pageへの自動操作を停止する。
4. `attach_chromium`で接続し、必要なら`request_browser_action`で操作内容を設定する。
5. MCPが返した`open_url`を変更せず、「ブラウザを開いて操作する」というMarkdownリンクでユーザーへ提示する。
6. 最終確定操作は代行せず、ユーザーへ具体的に指示する。
7. 完了または中止を確認したら`detach_chromium`を呼ぶ。detachはPageやChromiumを終了しない。

CDP endpoint、cookie、password、tokenを回答・log・commitへ出力しない。
```

MCP tool descriptionにも同じ重要規則を持たせる。案内ファイルだけに依存すると、別ディレクトリや案内を持たない
セッションで規則が失われるためである。

## 53.10 セキュリティとプライバシー

1. **CDPは強い権限**: cookie取得、任意JS、network観測が可能である。Agentだけがloopbackで接続し、raw CDPを中継しない。
2. **port以外を信用しない**: host/scheme/WS URLを入力にせず、discovery応答のhostも採用しない。redirectを追わない。
3. **Console認可**: attachment IDは認証の代わりにしない。CPは毎回membershipとWorkspaceを解決する。
4. **秘密の非永続化**: frame、console log、title、外部URL、CDP discovery応答をDB・audit log・notificationへ保存しない。
5. **画面上の秘密**: Consoleは対象Pageをそのユーザー本人へだけ表示する。スクリーンショット取得・共有機能はv1に含めない。
6. **入力競合**: user-control開始前にownerが対象Pageへの自動操作を停止することを利用契約上の必須条件とする。AFは停止を
   検知・強制できないため、警告とcontrol modeを提供し、非協調ownerとの競合結果は未定義とする。
7. **外部navigation**: attachモードだけが既存外部Pageを表示する。通常BrowserManagerのloopback境界を共有フラグで緩めない。
8. **resource cap**: attachmentも既存Pageと合算してWorkspace当たり表示中2、最大1600x1200、12fps、JPEG quality 70を既定にする。

## 53.11 競合制御と協調プロトコル

v1のcontrol mode:

| mode | 意味 |
|------|------|
| `view-only` | frame表示のみ。Consoleからの入力をAgentが拒否 |
| `user-control` | 許可済みinputをtargetへ転送 |
| `locked` | frameと入力を停止。handoff準備中または終了後 |

AFがownerをpauseする標準手段はv1に含めない。`user-control`に対する必須ownerフローは:

```text
自動操作停止
  → attach / handoff(user-control)
  → ユーザー完了または中止
  → handoff結果をownerが確認
  → locked / detach
  → 自動操作再開または終了
```

将来、owner向けloopback coordination APIを追加する場合も、CDPとは分離し、handoff IDの状態待ちだけを提供する。
サイト上の成功判定や業務データは扱わない。

## 53.12 観測・監査

記録してよいもの:

- actor、Workspace、attachment作成/破棄、時刻、結果コード
- CDP port（必要なら監査値。ただし公開レスポンスや通知には不用意に載せない）
- target IDの不可逆hash
- handoffの`completed/cancelled`と時刻
- frame数、drop数、bytes、viewer接続時間

記録しないもの:

- frame画像
- keyboard/text入力
- cookie、Authorization、storage
- title、URL全文、query、fragment
- console message本文
- handoff message（利用者が秘密を含め得るため、v1はmemoryのみ）

## 53.13 段階導入

### P0 — CDP attach spike

- **完了（2026-08-02）**。system Chromium + Playwrightの同一endpointへ独立した2 clientを接続した。
- screencast、mouse、wheel、keyboard、IME、navigation、detach、target closeを実測した。
- owner操作中/停止中の競合と、対応CLIのMCP structured resultを記録した。
- 未決5項目を[P0実測](53-chromium-attach-view-p0-verification.md)と§53.15で契約化した。

### P1 — Agent/API

- AttachmentManager、target discovery、attach/status/detach。
- 既存browser wire再利用、外部URL表示、resource cap、security test。
- CP REST/WS relay。

### P2 — Console one-click View

- `BrowserAttachPane`とaction route。
- layout commit、期限切れ、上限時選択、完了/中止UI。
- headless Chromiumを使ったConsole E2E。
- **2026-08-08 補修**: 導線が両端で切れていた。(a) CPにaction routeが無く、リンク遷移が静的catch-allの404、
  (b) ミラーのMarkdownリンクがrepo相対ファイルとして解決され、クリックが「ファイルが見つかりません」で終了。
  CPのshell routeとMarkdownView側の判定を入れ、両者を`openBrowserAttachment()`へ集約した（§53.7）。
  併せてport衝突対策を入れた（§53.16）。Console E2E（実リンククリック→ペイン）は未実施のまま残る。

### P3 — local MCP / agent guidance

- **完了（2026-08-02。対話セッション直接経路を同日補正）**。
- アシスタント面のread/`af_write`分離と、対話セッション面の`af_report`＋Chromium限定集合を実装した。
- structured result、open link提示契約、Workspace共通CLAUDE.md/AGENTS.md案内を実装した。
- Chromium handlerのrelay契約とmode別tool集合を自動テストし、既存のMCP→リンク→ペイン→完了→detach検証を
  対話セッションから実行可能な配線へ直した。

### P4 — durable handoff report（任意）

- 完了/中止をアシスタント会話へ自動報告。
- handoff ledgerとlevel-driven reconciliation。
- Workspace Stop→Start、Console reload、会話非表示時の取りこぼし検証。

## 53.14 テスト契約

- unit: port/host/WS URL再構成、target filter、typed ownership、TTL、detach冪等性、control mode。
- Agent integration: fake CDPではなく短命な`/usr/bin/chromium`を用い、外部HTTPS相当はローカルfixture originで検証する。
- security: `0.0.0.0`入力不可、redirect型SSRF不可、Agent/CP/metadata endpoint拒否、raw CDP非露出。
- Console DOM: action route、**ミラーのaction linkがファイル解決へ落ちないこと**、layout上限、expired overlay、
  view-only入力拒否、日本語IME、**dragのmoveが押下buttonを載せること**、Ctrl+C/Ctrl+Vの扱い。
- zoom-to-fit: 縦横比とlayout上限（unit）、実Chromiumで**frameの画素**がページの左端・右端で埋まること
  （寸法だけでは`scale`の不具合を見逃す）。
- 操作系: 実Chromiumでホイール・PgDn・ArrowDownがスクロールし、ArrowLeftがtextareaのキャレットを動かし、
  横スクロールバーのdragが効くこと（`AF_CHROMIUM_ATTACH_LIVE=1`）。
- CP route: `GET /open/browser-attachment/{id}`がConsole shellを返し、auth exemptにならないこと。
- port衝突: 実Chromium 2本で後発が生存すること（前提の再確認）と`cdp_port_ambiguous`、`--remote-debugging-port=0`の
  `DevToolsActivePort`契約、live endpointに対する`cdp_browser_mismatch`（`AF_CHROMIUM_ATTACH_LIVE=1`）。
- **既定経路（2026-08-08 追加）**: `Create`直後の control mode が`view-only`であること、その状態でwheelとkeyが
  転送されず**viewerへ`input_not_allowed`が届く**こと、`user-control`後に転送されることを1本で押さえる
  （`TestBrowserAttachmentDefaultsToViewOnlyAndReportsRefusedInput`）。
  既存の入力系テストは**全て自分で`SetControlMode(user-control)`を呼んでから**送っていたため、
  「attachしてリンクを渡すだけ」という現場の既定経路だけが無検証で、§53.17の不具合として利用者へ届いた。
  入力の検証を足すときは「誰がuser-controlにしたか」を必ず確認する。
- Console DOM（入力の前提）: wheelが**canvasの非passive listener**でcancelされること（Reactの`onWheel`は
  root側でpassive登録されるため`preventDefault`が無効）、canvasへのfocusだけで—クリック無しで—キーが
  隠しinputへ届くこと。
- Console E2E: Playwright(owner) → Chromium → Agent attach → Console Playwright(viewer)の二層を明示して実施する。
- lifecycle: owner終了、target close、Agent restart、viewer reload、detach後もowner Pageが生存すること。
- MCP（アシスタント）: af_readにchange toolが出ない、推測call拒否、`open_url`がそのままリンク化される説明、
  二重attach/detach retry。
- MCP（対話セッション）: `--self-report --chromium-attach`が`af_report`＋Chromium 7種だけを広告し、
  Chromium change callは通す一方、他のフリートread/writeの推測callを拒否する。`--self-report`単独は
  `af_report` 1本の後方互換を保ち、`--chromium-attach`単独では権限を広げない。

## 53.15 P0確定事項

実装前の未決5項目は2026-08-02の[P0実測](53-chromium-attach-view-p0-verification.md)で閉じた。

1. `browserCDP` coreからpipe/WebSocket framingとprocess所有だけをtransport adapterへ分ける。
2. 複数target sessionのscreencastは独立して動作した。AFは自分のsessionだけをstart/stop/ACK/detachする。
3. attachmentは専用`/ws/browser-attachments?id=` namespaceへ分離する。
4. MCP resultはtool `outputSchema`、同一値のJSON text、`structuredContent`の3点を持つ。text fallbackを全CLIの正とする。
5. Consoleは既存attachment focus→blank→新規slotの順に配置し、上限時だけactive pane置換の確認を出す。

機能境界はP0結果によって拡張していない。P1以降はこの5点を再選択せず、実装・security/lifecycle testへ進む。

## 53.16 port衝突（2026-08-08 実測）

Chrome 151で実測した。**同じ`--remote-debugging-port`で2つ目のChromiumを起動しても失敗しない。**

| | listen |
|---|---|
| 先着 | `127.0.0.1:P`（IPv4） |
| 後発 | `[::1]:P`（IPv6） |

`--remote-debugging-address=127.0.0.1`を両方に付けても、後発はIPv4のbindに失敗した後IPv6 loopbackを掴んで
通常どおり起動し続け、警告もログも出さない。Agentのdiscoveryは`http://127.0.0.1:<port>`固定なので、
**後発を起動したセッションが自分のportを指定したつもりで、先着＝別セッションのChromiumのtargetを受け取り、
そのままattachできてしまう**。実際に9333を隣のセッションのPlaywrightプロファイルが握っている状態を観測した。

境界の整理:

- **コンテナは越えない。** loopbackはnetwork namespace毎で、attach APIはhostを受け取らず、advertisedな
  authorityも常に`127.0.0.1:<port>`へ再構成する（`reconstructCDPWebSocketURL`）。他ユーザーのWorkspaceへは届かない。
- **同一コンテナ内のセッション同士は素通しだった。** ここが実害。相手はログイン済みPageかもしれない。

対策は3層で、上から順に効く:

1. **起動契約**（§53.4）: `--remote-debugging-port=0` + `DevToolsActivePort`。衝突が原理的に起きない。
2. **個体照合**: `attach_chromium`の`expected_browser_id` → REST `browserId` → `cdp_browser_mismatch`。
3. **曖昧なら失敗**: discoveryが`/proc/net/tcp{,6}`のLISTEN inodeを`/proc/<pid>/fd`で引き、2プロセス以上なら
   `cdp_port_ambiguous`。**advisoryである**: procfsが読めない、socketの持ち主を特定できない場合は
   「不明」として通す（正当なattachを止めないため）。1と2が本体で、3は保険。

Chromium側を「listenに失敗させる」ことはこちらから制御できない（別family へ逃げる）ため、失敗させるのはattach側の役目とする。

## 53.17 「スクロールもキーも効かない」の真因（2026-08-08 実測）

利用者から「ペインは見えているのにスクロールとキー操作が効かない」という報告が続いた。Agent単体の
liveテスト（`AF_CHROMIUM_ATTACH_LIVE=1`）は4本ともPASSしており、実装は「動いている」ように見えていた。

真因は入力の実装ではなく **control mode の既定値と、そこへ誰も触れない導線** だった。

### 実測

自前Chromium（`--remote-debugging-port=0`）を立て、Agentの実RESTで attachment を作り、実WS
（`/ws/browser-attachments?id=`）へ Console と同じメッセージを流して`window.scrollY`を読んだ。

| control mode | wheel `deltaY=400` 後の `scrollY` | viewerへ届くもの |
|---|---|---|
| `view-only`（**attach直後の既定**） | `0` | `protocol-error` / `input_not_allowed` |
| `user-control` | `400` | — |

`Create`は`controlMode: attachmentControlViewOnly`で attachment を作る（`browser_attachment_manager.go`）。
`view-only`では`copy`と`viewport`/`visibility`以外の全入力が`handleControl`で弾かれる。つまり
**スクロールもキーもポインタも、仕様どおり全部拒否されていた**。

### なぜ誰も気付かなかったか

- **テスト**: 入力を検証するテストは`SetControlMode(user-control)`や`UpdateHandoff(user-control)`を
  自分で呼んでから送っていた。現場の既定経路＝「attachしてリンクを渡すだけ」だけが無検証だった（§53.14）。
- **ツール説明**: `attach_chromium`の説明は「`open_url`をリンクで提示せよ」で終わっており、
  `view-only`で始まることも、引き渡しに`request_browser_action`が要ることも書いていなかった。
  `workspace-notes.md`も「ユーザーに説明が要るときは`request_browser_action`」と*条件付き*に読める書き方だった。
  説明どおりに動くエージェントほど、入力の死んだペインを渡してしまう。
- **Console**: `view-only`のときチップに「閲覧のみ」と出るだけで、`attachmentStatus()`は空文字を返し、
  Agentが送る`protocol-error`は`handleText`のswitchに case が無く捨てられていた。
  **利用者から見て、動くペインと入力が全部捨てられているペインが完全に同じ見た目だった。**

### 対応（§53.11 の owner フローは変更しない）

所有権と必須フロー（自動操作停止 → handoff → 完了 → locked/detach）は正しいので、契約は据え置く。
壊れていたのは「その状態が読めないこと」と「エージェントへの指示」なので、そこだけ直す。

1. **Console**: `view-only`のとき「入力は無効。スクロールもキーも送られない」と明示する
   （`browser.attach.view_only_notice`）。
2. **Console**: `protocol-error`をconsole drawerへ出す。以後、版ずれやmodeの競合が黙って消えない。
3. **ツール契約**: `attach_chromium`の説明と`workspace-notes.md`に、既定が`view-only`であること、
   操作させるなら`request_browser_action`／`set_chromium_control_mode`が必須であることを明記する。
4. **テスト**: 既定経路の回帰テストを追加（§53.14）。

利用者が自分で操作権を取るボタンは**入れない**。owner の自動操作が動いたまま人間の入力が混ざる状態は
§53.10-6 で「競合結果は未定義」としており、AFはownerの停止を検知も強制もできないため。

### 併せて直したConsole側の2件

どちらも`view-only`を解除した後に残る、入力系の実装漏れ。

- **wheelがcancelされない**: Reactは`wheel`をroot containerへ`{passive: true}`で登録する（react-dom 19で実測）。
  そのため`onWheel`内の`preventDefault()`は無効で、リモートページと**Console自身のスクロール容器**が同時に動いた。
  canvasへ`{passive: false}`のnative listenerとして登録する。
- **クリックするまでキーが効かない**: キーは隠し`<input class="browser-ime">`が受けるが、focusは
  canvasの`pointerdown`でしか移らなかった。canvasを`tabIndex`で focusable にし、focus受領時にimeへ渡す。

## 53.18 RDP / VNC 転送の検討（2026-08-08 実測）

「Chromiumへ RDP 等で接続できないか。ペイン内でRDPクライアントを動かすのはどうか」という要望を受けて再検討した。
狙いは、CDPの screencast + `Input.*` 合成という現行方式をやめ、**OSレベルの入力処理をそのまま使う転送**へ
置き換えること。§53.17のスクロール/キーの不具合が入力合成まわりに集中していたので、動機自体は妥当である。

結論: **採らない。** ADR0038の「採らなかった案」を維持する。理由は下の3層で、**1が決定打**。
コストや導入可否（2・3）は、仮に解決しても1を覆さない。

### 1. 所有権が合わない（設計上の理由）

§53.2の前提は「ownerがChromiumを起動し所有する。AFは既存Pageへ一時的なCDP sessionを足すだけ」である。
VNC/RDPが転送するのはPageではなく**X displayという別レイヤ**なので、この機能に適用するには
ownerが自分のChromiumを**AFの用意した仮想ディスプレイ上でheadful起動している**必要がある。
つまりAFがowner側の起動方法（§53.4の起動契約）を指定することになり、
「AFはChromiumを起動しない・再起動しない」という受入条件5と真正面から衝突する。

既に走っているheadless Chromiumのpageへ後付けで適用する方法は無い。VNCは
「AFが自分でブラウザを立てて見せる」用途の道具であって、他人が所有するPageを覗く道具ではない。

### 2. このコンテナでは root 無しに完結しない（実測）

| 項目 | 結果 |
|---|---|
| `Xvfb` / `x11vnc` / `xrdp` / `vncserver` / `websockify` / `noVNC` / `xpra` / `Xephyr` / `weston` / `wayvnc` | いずれも未インストール、`DISPLAY`未設定 |
| X ランタイムlib（`libX11.so.6` / `libxcb.so.1` / `libXtst.so.6`） | 存在する |
| Chromium 151 の headful X | **可能**。`--ozone-platform=x11`は`Missing X server or $DISPLAY`だけで落ちる＝X11 backendは焼き込み済み |
| 外向き通信（SourceForge / PyPI / GitHub） | 到達する（tigervnc `200`、pypi `200`、github `302`） |
| TigerVNC 1.14.1 バイナリtarball | root無しで展開できる。`Xvnc`の不足lib`libxcvt.so.0`もDebian debを`dpkg-deb -x`で解決できた |
| **`Xvnc`の起動** | **失敗**。`/usr/bin/xkbcomp`を**絶対パスで**exec し、`PATH`を見ない。`XKB_BINDIR`相当のenv override も binary に無い。`Failed to activate virtual core keyboard`でfatal |
| user namespace による回避（`unshare -Urm`でbind mount） | **不可**。`cannot change root filesystem propagation: Permission denied` |

つまりセッション内でどうにかする道は無く、**イメージへの焼き込み＝operator案件**になる。
`~/.local`に手組みしたX userlandは、絶対パス依存が残る以上イメージ再ビルドで壊れる類のものでもある。

### 3. コストが現行方式より明確に重い

ペイン内でnoVNCを出す案は「Console側に新機能を作らずに評価できる」点は良いが、構造は
**ペイン内Chromium（noVNCを描く）→ X上のもう1つのChromium**という二重になる。
常駐物はX server + headful Chromium + Xvnc + websockify に増え、現行（headless Chromium + JPEG screencast）
より数倍のメモリを恒常的に使う。共有ホストのメモリ制約（本コンテナは 10 GiB / 8 core）に対して割に合わない。

RDPそのものは更に不利で、`xrdp`はsesman/PAM前提でroot無し導入が重く、Web клиアント（Guacamole等）は別サーバを要する。
仮にこの路線を採るなら**VNC一択**である。

### 4. そもそも今回の不具合はCDPの限界ではなかった

§53.17のとおり、真因はcontrol modeの既定値と導線であり、残りも実装漏れ（仮想キーコード、dragのbutton、
`setDeviceMetricsOverride`の`scale`誤用、Reactのpassive wheel）だった。いずれもCDPで正しく表現できるものを
表現し損ねていただけで、OSレベル入力に置き換える理由にはならない。

### 再検討の条件

次のいずれかが成立したら見直す価値がある。

- **AF自身がブラウザを起動して見せる**用途が主になったとき（ownerの所有権制約が外れるため、1の理由が消える）。
- Chromium以外のGUIアプリをペインに出す要件が出たとき。
- CDPの`Input.*`で**原理的に**表現できない入力が必要になったとき（OSレベルのD&D、ネイティブファイル選択ダイアログ、
  プラグインや`<select>`のOS popup等）。現時点でこれらは要件に無い。

この場合も前提として、workspaceイメージへ`tigervnc`（`Xvnc`）・`xkbcomp`＋xkeyboard-config・`websockify`・`noVNC`を
焼き込むこと、およびVNCをloopback限定＋認証必須で運用し、CP経由の中継経路（現行の`/ws/browser-attachments`相当）を
設計することが必要になる。

## 53.19 タッチ操作（2026-08-08 実測）

スマホでattach viewを触ると**スクロールがまともにできない**。指のpointer eventをそのままmouseへ転送していたため、
スワイプが常に「左ボタンを押したままのdrag」になり、ページはスクロールせずテキスト選択だけが走っていた。
拡大手段も無く、fit済みのデスクトップサイトは文字が1/3の大きさのまま読めない。

### ジェスチャ（Console側・`console/src/features/browser/touch.ts`）

`pointerType === "touch"`のときだけ、mouseへ転送する前に意図を判定する。マウス/ペンの経路は従来のまま。

| 操作 | 送るもの |
|------|----------|
| スワイプ | `wheel`（移動量の符号反転）。離した後は慣性を減衰させながら継続 |
| タップ | hoverの`mouse move` → `down` → `up`（IME inputへfocusは**しない**） |
| ダブルタップ | fit（またはpane）と等倍の切り替え。2度目のクリックは送らない |
| 長押し（500ms） | `mouse down`のままdragへ昇格。テキスト選択・スライダー・drag&dropはこれ以外に手段がない |
| 2本指ピンチ | `viewport`の`zoom`。指が触れている間はcanvasのCSS transformで即時プレビューし、離した時に確定する |

- タップ判定は10px/長押し500ms。スワイプに切り替わった時点で長押しは取り消す。
- スワイプの`wheel`は**1フレームに1本へまとめる**（指は毎秒60〜120回動き、1本ごとに
  Console→CP→Agent を渡る。画面は約12fpsなので細かく送っても映らない）。最初の1本は即送り、
  後続だけをまとめるので出だしが遅れない。慣性のtickも32ms間隔。
- touch defaultはcanvasの**非passive native listener**で潰す（`touchstart`/`touchmove`）。wheelと同じ罠で、
  Reactのtouch propは passive 登録される。`touch-action: none`が主だが、これが無いと2つ目以降のtouchmoveが
  非cancelableで届く（＝ブラウザが自前スクロールを確定済み・Chromium 151で実測）。iOS Safariのページ側
  ピンチズームは`gesturestart/change/end`のpreventDefaultでしか止まらないので併せて潰す。
- pinchは**ページ入力ではない**ので、view-only（attach直後の既定）でも効く。逆にスワイプ・タップ・長押しは
  従来どおり`user-control`でだけ効く。
- pinchの確定は指を離した時の1回だけ。押している間に送るとlayout変更とscreencast再起動が毎フレーム走る。
- ダブルタップの等倍は Console 側で逆算する。baseを持っているのはAgent（fitのために実測できるのはAgentだけ）
  なので、直近の`viewport` textが示すlayoutから`layout × zoom = base`、`base / pane`が等倍の倍率になる。
  fitが無ければbase == paneで等倍に行き先が無いので2倍にする。2度目のクリックは**送らない** —
  1度目を遅らせて「2度目が来るか」を待つのは、各ブラウザが何年もかけて消した300msのタップ遅延そのもの。
  3度目で戻ってしまわないよう、切り替えた時点で直前タップの記憶を捨てる。
- 1に戻すまで縮小方向は出さない。「ピンチで戻せば必ず元に戻る」ことが、リセットUIを増やさない代わりの保証になる。

### なぜ画像の拡大ではなくlayoutを縮めるのか（実測）

- `Page.startScreencast`のframeは**CSS pixel寸法で、emulateしたdeviceScaleFactorを無視する**
  （220x267 layoutはDPR 1/2/3のいずれでも220x267のframe。同じ状態で`Page.captureScreenshot`は660x801を返す。
  Chromium 151で実測）。したがって「DPRを上げてframeの画素を増やす」は効かない。
- 一方、fitで1240 CSS pxを660のpaneに詰めた状態は**すでに情報が失われている**ので、そのframeを拡大しても
  ぼやけた文字が大きくなるだけ。layoutを620へ縮めれば文字は原寸で描き直され、実際に読める。
  live testはこの差を色帯で判定する（fitならframe右端は赤帯＝ページ全体、2倍pinch後は白＝左半分を原寸で表示）。
- 等倍（layout == pane）が鮮明さの上限で、それ以上は素直な拡大になる。小さいUIを押せる大きさにする用途で
  必要なので許可し、layoutの下限だけ120pxで止める。

### テスト

- `zoomedLayout` 単体、attachment/ownedそれぞれのviewport handler、Console側は`touch.test.ts`（判定）と
  `BrowserSurface.dom.test.tsx`（配線・view-onlyでのpinch）。
- `AF_CHROMIUM_ATTACH_LIVE=1`の`TestBrowserAttachmentLivePinchZoom`が実Chromiumで再layoutとframe寸法を見る。
- Console側の配線は実装時にheadless Chromium＋`Input.dispatchTouchEvent`で実測した（スワイプ＝wheel列と慣性、
  タップ＝move/down/upとIMEへのfocus、長押し＝button downのままdrag、ピンチ＝保持中のtransformと離した時の
  1回のzoom）。jsdomはpointer captureもtouch defaultも持たないので、ここは実ブラウザでしか確かめられない。

## 53.20 スマホ実機で出た2件（2026-08-08 実測）

### スクロールがもたつく — 入力1件ごとにCDPの応答を待っていた

`Input.dispatch*`の応答はChromiumが**そのイベントを処理してから**返る＝フレーム同期で、
実測で1件あたり約16ms（wheel 20件のdispatchに312ms。スクロール自体は100ms以内に終わっている）。
viewerは制御メッセージを**読み取りループ上で**処理するので、毎秒60〜120件を生む指はこれを追い抜き、
スワイプ全体がackの後ろに詰まる。「指を止めた後もスクロールし続ける」の正体。

- `browserCDP.Send`（応答を待たない書き込み）を追加し、**pointer相当のイベントだけ**そこへ回す
  （`mouseMoved`と`mouseWheel`）。同一CDP接続はメッセージを書いた順に処理するので、順序は保たれる。
  実測: wheel 40件のdispatchが 4ms（従来換算 約625ms）。
- **クリック・キー・`insertText`は今も応答を待つ**。1操作1件で頻度が低く、待つことで
  「直後に読んだDOMがその入力を反映している」保証が続く。実際
  `TestBrowserPlainASCIIKeyTypingLandsInForm`（keyの直後に`value`を読む）が、全部を非同期化した
  最初の版で落ちてこれを教えてくれた。
- `TestBrowserAttachmentLiveInputDoesNotBlockOnAcks`が閾値150msで再発を止める。
- 表示の滑らかさ自体は screencast の約12fps（`AF_BROWSER_MAX_FPS`）が上限で、これは別の話。

### text/textarea 以外のタップでGBoardが開く

タップで隠しIME inputへ**focusしていた**のが原因。focusがキーボードを呼ぶので、リンクやボタンを
押しただけでもGBoardが出る。ページ側のフォーカスが編集可能かはAgentに聞けば分かるが、往復の後の
`focus()`はユーザージェスチャ外なので**キーボードは開かない**（各ブラウザの仕様）。したがって
「タップしてから判定して開く」は成立しない。

- タップではfocusせず、隠しinputの**位置だけ**タップ地点へ移す（IMEの変換候補がそこに出る）。
- 代わりにペイン内（左下）に**キーボードトグル**を置く。coarse pointerのときだけ出す。
  ページのフィールドをタップ → トグルで開く → 入力、という手順になる。
- touch defaultを潰しているのでcanvasをタップしてもfocusは移らず、**開いたキーボードは閉じない**。

