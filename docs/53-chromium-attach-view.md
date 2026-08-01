# 53. Chromium Attach View — 外部所有 Chromium の表示・人間操作

> 状態: **P0実測・実装契約確定、P1以降は未実装**（2026-08-02）
> 意思決定: [decisions/0037](decisions/0037-chromium-attach-view.md)
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

外部プロセスはremote debuggingをloopbackだけに公開する。

```sh
chromium \
  --headless=new \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --user-data-dir=/explicit/profile/path
```

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
  "viewport": {"width": 1280, "height": 900, "deviceScaleFactor": 1}
}
```

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
GET    /api/browser/attachments/{id}
DELETE /api/browser/attachments/{id}
POST   /api/browser/attachments/{id}/handoff
POST   /api/browser/attachments/{id}/handoff-result
```

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
type BrowserAttachTargetsResponse = { targets: BrowserAttachTarget[] }

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
| `POST /api/browser/attachments/{id}/handoff` | `{message,completionLabel?,allowCancel?,controlMode?}` | `200 BrowserAttachment` |
| `POST /api/browser/attachments/{id}/handoff-result` | `{result:"completed"\|"cancelled"}` | `200 BrowserAttachment` |

handoffの省略値は`completionLabel="操作完了"`、`allowCancel=false`、`controlMode="user-control"`である。
`user-control`を要求するcallerは、そのrequestより前にowner側の対象Pageへの自動操作を停止しなければならない。
AFは停止済みかを検知できず、未停止時の入力結果は未定義である。`completed/cancelled`確定後は`locked`へ移る。

専用WebSocketのwire versionは`1`である。接続直後のtext `ready`は
`{type:"ready",version:1,state,url,title,width,height,controlMode,handoff}`、frameはbinary JPEGとする。
`viewport`と`visibility`は全modeで受理する。`mouse`、`wheel`、`key`、`text`、`reload`、`history`は
`user-control`だけで受理し、他modeではtext
`{type:"protocol-error",code:"input_not_allowed",message}`を返す。`navigate`はこのnamespaceのmessage型に存在せず、
`unknown_type`となる。Agentからの他のtext eventは既存wireと同じ`state`、`navigation`、`console`、`page-error`に加え、
`{type:"handoff",handoff,controlMode}`を使う。

terminal state (`target-closed` / `disconnected`) は短い再確認猶予中だけstatusで取得でき、その後は
`browser_attachment_not_found`になる。いずれのdetach/expiry/terminal経路も
`Target.closeTarget`、`Target.disposeBrowserContext`、`Browser.close`を外部ownerへ送らない。

## 53.7 Console とワンクリック導線

### action link

エージェントがMCP結果の`openUrl`を変更せずMarkdownリンクとして提示する。

```markdown
[ブラウザを開いて操作する](/open/browser-attachment/ba_7f3...)
```

クリック時にConsoleは:

1. routeのattachmentをGETし、認証・membership・有効期限を確認する。
2. 同じattachmentを表示中ならそのpaneをactiveにする。次にactiveなblank、layout順のblankを使う。
3. blankがなく上限未満なら、desktopは右列→下split、mobileは下splitの順で新しいslotを作る。
4. desktop 8 pane / mobile 2 paneの上限時だけ「現在のペインで開く / キャンセル」を表示し、既定focusを
   「キャンセル」にする。黙って別paneを上書きしない。
5. layout storeの正規の`commit()`経路で`{kind:"browserAttach", attachmentId}`を1回だけ設定する。
6. 成功後だけaction routeをhistoryから正規Workspace URLへ置換し、再実行を防ぐ。

サーバーpushやMCP呼び出しだけで利用者のlayoutを勝手に変更しない。layoutはブラウザ端末ごとのlocal stateであり、
ユーザークリックが「この端末で表示する」という明示的な意思になる。

`attachmentId`はCDP credentialではないが、layout/localStorageに保存された場合に期限切れ後は再attachせず、専用の
「操作画面の有効期限が切れました」表示にする。port、targetId、外部URL、CDP WebSocket URLはlayoutへ保存しない。

### BrowserAttachPane

既存BrowserPaneのcanvas、IME、pointer/wheel/key変換、resize debounce、console drawerを共用する。差分は:

- toolbarにhost/path入力を出さない。
- titleとoriginだけを表示し、URL全文のqueryは既定で隠す。
- `view-only | user-control | locked`を明示する。
- handoff messageと「操作完了」「中止」をPage外のConsole chromeに表示する。
- detachは「表示を閉じる」と表記し、Chromiumを終了しない旨をtooltipで示す。

## 53.8 ローカル stdio MCP

コンテナ内アシスタントが利用する`workspace-agent mcp-stdio`へ次を追加する。CPの対外MCPへ同時公開する必要はなく、
最初はown Workspaceのlocal stdio MCPだけを対象とする。

### read tools

#### `list_chromium_targets`

入力: `{port}`。targetの`targetId/title/url`を返す。対象Pageを選ぶ前に呼ぶ。

#### `get_chromium_attachment`

入力: `{attachment_id}`。state、viewer有無、handoff結果、期限を返す。URL全文は必要な場合だけ返す。

### af_write tools

#### `attach_chromium`

入力: `{port, target_id, label?}`。attachmentを作成し、`open_url`を返す。tool descriptionに次を強制する。

- 先に`list_chromium_targets`でtargetを確認する。
- 戻った`open_url`を改変せず、ユーザーへMarkdownリンクとして提示する。
- 最終確定をエージェント自身でクリックしない。
- attach成功をPage上の業務処理成功と言い換えない。

#### `request_browser_action`

入力: `{attachment_id, message, completion_label?, allow_cancel?, control_mode?}`。handoffを作成・更新する。

#### `detach_chromium`

入力: `{attachment_id}`。AFの接続だけを終了する。ownerのPage/processを閉じない。

`attach_chromium`は認証済みPageの描画を別surfaceへ出し、入力経路も作るためread-onlyではない。現行MCPと同じく
`--write`時だけツールを広告し、名前を推測したcallも拒否する。detachも共有状態を変えるため同じgateに置く。

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
`CLAUDE.md` / `AGENTS.md`へ置く。

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

### P3 — local MCP / agent guidance

- read/write tool分離、structured result、open link提示契約。
- Workspace共通CLAUDE.md/AGENTS.md案内。
- MCP→リンク→ペイン→完了→detachの通し検証。

### P4 — durable handoff report（任意）

- 完了/中止をアシスタント会話へ自動報告。
- handoff ledgerとlevel-driven reconciliation。
- Workspace Stop→Start、Console reload、会話非表示時の取りこぼし検証。

## 53.14 テスト契約

- unit: port/host/WS URL再構成、target filter、typed ownership、TTL、detach冪等性、control mode。
- Agent integration: fake CDPではなく短命な`/usr/bin/chromium`を用い、外部HTTPS相当はローカルfixture originで検証する。
- security: `0.0.0.0`入力不可、redirect型SSRF不可、Agent/CP/metadata endpoint拒否、raw CDP非露出。
- Console DOM: action route、layout上限、expired overlay、view-only入力拒否、日本語IME。
- Console E2E: Playwright(owner) → Chromium → Agent attach → Console Playwright(viewer)の二層を明示して実施する。
- lifecycle: owner終了、target close、Agent restart、viewer reload、detach後もowner Pageが生存すること。
- MCP: af_readにwrite toolが出ない、推測call拒否、`open_url`がそのままリンク化される説明、二重attach/detach retry。

## 53.15 P0確定事項

実装前の未決5項目は2026-08-02の[P0実測](53-chromium-attach-view-p0-verification.md)で閉じた。

1. `browserCDP` coreからpipe/WebSocket framingとprocess所有だけをtransport adapterへ分ける。
2. 複数target sessionのscreencastは独立して動作した。AFは自分のsessionだけをstart/stop/ACK/detachする。
3. attachmentは専用`/ws/browser-attachments?id=` namespaceへ分離する。
4. MCP resultはtool `outputSchema`、同一値のJSON text、`structuredContent`の3点を持つ。text fallbackを全CLIの正とする。
5. Consoleは既存attachment focus→blank→新規slotの順に配置し、上限時だけactive pane置換の確認を出す。

機能境界はP0結果によって拡張していない。P1以降はこの5点を再選択せず、実装・security/lifecycle testへ進む。
