# 53 P0. Chromium Attach View 実測記録

> 判定: **P0 合格・未決5項目を契約化済み**（2026-08-02）
> 実装契約: [53-chromium-attach-view.md](53-chromium-attach-view.md)
> 意思決定: [decisions/0038](../decisions/0038-chromium-attach-view.md)

## 1. 範囲と環境

P0では製品のAgent/API/Console/MCP実装を追加せず、次だけを実測した。

- Playwrightが所有するChromiumへ独立した2本のCDP clientが後から接続する挙動
- screencast、mouse、wheel、keyboard、日本語入力、navigation、detach、target close
- ownerと接続clientが同時に入力した場合の競合
- 現行`BrowserManager`のCDP transport境界、CP browser relay、Console layout演算の再利用可能範囲
- 現在サポート対象としてコンテナに入るCLIがMCP `structuredContent`をモデルへ渡すか

実測版:

| 対象 | 版 |
|------|----|
| system Chromium | 150.0.7871.181 |
| Playwright | 1.61.1 |
| Node.js | 22.23.1 |
| Claude Code | 2.1.220 |
| Codex CLI | 0.146.0 |
| opencode | 1.18.10 |
| GitHub Copilot CLI | 1.0.77 |
| Cursor Agent | 2026.07.23-e383d2b |
| Kiro CLI | 2.14.2 |

CDP再現手順:

```sh
cd console-e2e
npm ci --ignore-scripts --no-audit --no-fund
npm run probe:chromium-attach
```

`scripts/chromium-attach-p0.mjs`はloopback fixtureと短命Chromiumだけを起動し、終了時にownerとfixtureを回収する。
外部サイト、credential、既存profileは使わない。MCP実測には
`workspace/agent/testdata/mcp_structured_result_probe.mjs`をstdio serverとして各CLIへ一時接続した。textに無いmarkerを
`structuredContent`だけへ入れ、最終出力へmarkerが現れるかで、モデルに渡ったかを判定した。

## 2. CDP実測結果

ownerはPlaywrightのpipe接続を保持したまま、`127.0.0.1:<port>`へremote debuggingも公開した。そこへ
`connectOverCDP`を2回行い、それぞれから同一Pageへ独立したCDP sessionを作った。

| 項目 | 結果 |
|------|------|
| target discovery | 2 clientともownerの既存Pageを発見 |
| 同時`Page.startScreencast` | 両sessionが同時にJPEG frameを受信しACK成功 |
| client Aの`stopScreencast` | client Bは次の画面変更を継続受信 |
| client Aのdetach | owner Pageは生存し、client Bも継続受信 |
| mouse | CDP clickでfixtureのcounterが`1`へ変化 |
| keyboard | `Input.dispatchKeyEvent`の`A`を入力 |
| 日本語 | `Input.insertText`で`日本語`を入力し、値は`A日本語` |
| wheel | `scrollY`が`0`から`500`へ変化 |
| navigation | `/start`から`/next`後もtargetIdは同一 |
| target close | 両clientへcloseが伝播し、以後のCDP commandは失敗 |

複数clientのscreencastはsession単位で独立していた。したがってAFはownerや別clientのcastを停止・再設定せず、自分の
sessionでだけstart/stop/ACKする。AFのdetachは`Target.detachFromTarget`またはWebSocket切断までとし、
`Target.closeTarget`、`Target.disposeBrowserContext`、`Browser.close`を送らない。

### 2.1 入力競合

同じinputへownerのPlaywright `fill("OWNER")`と接続clientの`Input.insertText("USER")`を同時発行した20回は、
20回すべて最終値が`OWNER`になった。owner側の書込みを止めた後の`USER-PAUSED`は保持された。

CDPはclient間の入力transaction、lock、優先度を提供しない。実測の順序をChromiumの永続保証とはみなさず、
「最後に適用された操作が前の操作を上書きし得る」を契約とする。`user-control`開始前にownerが対象Pageへの自動操作を
停止することは利用契約上の必須条件である。v1のAFは停止を検知・強制できないため、違反時の結果は未定義としてUIで警告する。

## 3. P0で確定した実装契約

### 3.1 CDP transportの分割点

現行`browserCDP`の`Call / Events / Done / Close`は`BrowserManager`から見た十分な境界であり、managerは`pipeCDP`へ
downcastしていない。一方、`pipeCDP`には次の3責務が同居している。

1. Chromium process/profileの所有と回収
2. NUL区切りpipeまたはWebSocket frameというtransport framing
3. request ID/pending応答、bounded event queue、必須event/drop判定

P1では3を共通の`browserCDP` coreへ残し、1と2だけをtransport adapterへ分ける。adapterの最小契約は
「JSON messageを1件write」「JSON messageを1件read」「transportをclose」である。pipe adapterはChromium/profileも所有し、
WebSocket adapterはsocketだけを所有する。`browserCDPEvent.queue *pipeCDP`という現行の型漏れは、共通queueへの参照または
release callbackへ置換する。

CDP discovery、loopback host再構成、target filterは`AttachmentManager`の上位責務でありtransportへ入れない。
AF所有Pageのcreate/dispose/navigation policyも共通coreへ移さない。この分割ならscreencast/inputのmanager側処理を再利用しつつ、
外部ownerのlifecycleを誤って取り込まない。

### 3.2 複数CDP client

- browser WebSocket client、target session、screencastの状態をattachmentごとに1組持つ。
- 同じtargetへownerとAF、または複数AF相当clientが接続しても、AFは他sessionをdetach/stopしない。
- v1のAF APIは同一targetに作れるattachmentを1つに制限する。Chromiumが複数接続可能でも、人間操作競合を製品として
  広告しないためである。ownerのPlaywright接続はこの上限に数えない。
- target/session closeはattachmentを`target-closed`へ、browser WebSocket closeは`disconnected`へ遷移させる。

### 3.3 WebSocket namespace

attachmentは公開・Agent内部とも既存`/ws/browser`へ混在させず、次の専用namespaceを使う。

```text
GET /ws/browser-attachments?id=<attachmentId>&tenant=<slug>  # Console → CP
GET /ws/browser-attachments?id=<attachmentId>                # CP → Agent
```

CPの認証、membership解決、runtime検査、latest-frame relay実装は既存browser relayと共有するが、pathとAgent handlerは分ける。
現行`/ws/browser`はuntypedな`id`をAgent-owned Page mapへ直接解決し、wireに`navigate`も持つ。別namespaceならlookup前に
ownership modeが確定し、attachment handlerの許可message集合から`navigate`を構造的に除外できる。通常browser paneの
既存契約とテストを変更せずに済むことも分離理由である。

### 3.4 MCP structured result

`attach_chromium`はtool定義に`outputSchema`を持ち、成功時に同一値をtextと`structuredContent`の両方へ返す。

```json
{
  "resultType": "complete",
  "content": [{
    "type": "text",
    "text": "{\"attachment_id\":\"ba_...\",\"open_url\":\"/open/browser-attachment/ba_...\",\"expires_at\":\"2026-08-02T00:30:00Z\"}"
  }],
  "structuredContent": {
    "attachment_id": "ba_...",
    "open_url": "/open/browser-attachment/ba_...",
    "expires_at": "2026-08-02T00:30:00Z"
  }
}
```

field名はMCPのJSONでも既存tool入力と同じ`snake_case`に固定する。textは説明文ではなく短いJSON objectとし、
`structuredContent`非対応clientでも値を欠落させない。URL、title等の任意fieldはtoolごとの`outputSchema`に定義したものだけを返し、
raw CDP値を含めない。

実CLIのモデル入力結果:

| CLI | structured marker | 観測 |
|-----|-------------------|------|
| Claude Code 2.1.220 | 渡る | 最終JSONにstructured-only 2 field |
| Codex CLI 0.146.0 | 渡る | tool eventの`structured_content`と最終JSONの双方に出現 |
| GitHub Copilot CLI 1.0.77 | 渡る | tool resultがtextとserialized structured値を併記 |
| opencode 1.18.10 | 渡らない | tool outputと最終回答はtext fallbackだけ |
| Cursor Agent 2026.07.23 | 渡らない | MCP resultからtext contentだけを保持 |
| Kiro CLI 2.14.2 | 渡らない | MCP resultからtext contentだけを保持 |
| agy | 未計測 | この環境のbinaryが起動時`CRNGT failed`。text fallback対象として扱う |

未計測版、将来版、上表で「渡らない」clientはすべてtext fallback clientとして扱う。機能判定やCLI分岐は実装しない。

### 3.5 Consoleペイン配置

action URLをクリックした端末では、次の決定順序を1回のlayout `commit()`として適用する。

1. 同じ`attachmentId`を表示中なら、そのpaneをactiveにする。
2. blank paneがあれば、activeなblank、次にlayout順のblankへ配置する。
3. blankがなく上限未満なら、既存`openInNew`と同じくdesktopは右列を4列まで作り、その後は列を下splitする。mobileは
   active列を下splitする。
4. desktop 8 pane / mobile 2 paneの上限時は黙って別paneを上書きしない。「現在のペインで開く / キャンセル」を表示し、
   既定focusは「キャンセル」とする。利用者が前者を選んだ時だけactive paneを置換する。

通常時はリンクを含むsource paneを保持する。成功後だけaction routeを正規Workspace URLへ`replaceState`し、失敗・cancel時は
attachmentを開いた扱いにしない。現行`openInNew`は上限時に非active paneを黙って再利用するため、action routeからその最終分岐を
直接呼ばず、上限判定と確認を先に行う。

## 4. P0判定と後続境界

未決5項目はすべて上記契約で閉じた。P1以降で再検討を要するP0 blockerはない。

P0成果物は本記録、2つの再現probe、設計書・ADRの契約更新だけである。`AttachmentManager`、REST/WS route、
`BrowserAttachPane`、MCP tool本体はまだ実装しておらず、次セッションのAgent / Console / MCP各laneへ渡す。
