# 0037. 外部所有 Chromium はloopback CDPへattachし、ユーザークリックでConsoleペインへ開く

- 状態: 採用・P0実測で実装契約確定（P1以降は未実装、2026-08-02）
- 関連: [53-chromium-attach-view.md](../53-chromium-attach-view.md) /
  [53 P0実測](../53-chromium-attach-view-p0-verification.md) / [0018-container-browser-pane.md](0018-container-browser-pane.md)

## 背景

既存ブラウザペインはWorkspace AgentがChromium、BrowserContext、Pageを所有し、コンテナ内localhost Webアプリを
表示する。外部top-level navigationを拒否し、raw CDPを公開しないことがセキュリティ境界である。

一方、Playwright等の外部プロセスがheadless Chromiumを所有して外部管理画面を自動操作し、最後の確認や確定だけを
ユーザーへ渡したい用途がある。この場合、業務スクリプトをAFへ移植せず、既に存在するPageの描画・入力だけを
Consoleへ中継する必要がある。エージェントはCLAUDE.md / AGENTS.mdの指示に従い、Workspace AgentのローカルMCPから
この引き渡しを準備できることが望ましい。

## 決定

1. 「Chromium Attach View」を既存ブラウザペインの第2モードとして追加する。AF所有Pageと外部所有Pageは所有権、
   navigation、lifecycleを型で分離する。
2. 外部ownerはChromiumを`--remote-debugging-address=127.0.0.1`と明示port付きで起動する。Agentはhostを受け取らず、
   loopback CDPだけへ接続する。
3. Agentは既存targetへCDP sessionをattachし、既存基盤と同じscreencastと許可済みInput操作だけをConsoleへ中継する。
   raw CDP、cookie、storage、response bodyは公開しない。
4. detachはAFのCDP sessionとviewerだけを解放する。外部ownerのPage、BrowserContext、profile、Chromium processを
   閉じない。
5. 通常ブラウザペインのloopback限定は変更しない。attachmentはownerが開いたHTTP(S)外部Pageを表示できるが、
   Consoleから任意URLを入力する汎用ブラウザにはしない。
6. ローカルAF MCPへtarget列挙、attach、handoff要求、状態取得、detachを追加する。target列挙と状態取得はread、
   表示・入力経路を作るattach/handoff/detachは`af_write`だけに広告する。
7. attach結果は短命なopaque attachment IDとConsole action URLを返す。エージェントはURLを変更せずMarkdownリンクで
   ユーザーへ提示する。
8. MCPやサーバーpushだけでConsole layoutを変更しない。ユーザーがaction URLを1回クリックした時に、そのConsole端末の
   layout storeが正規経路でペインを作る。これを表示に対する明示的なユーザー意思とする。
9. handoffの「完了」「中止」はユーザーの自己申告であり、外部サイト上の処理成功の証拠とはみなさない。
10. v1はChromium/CDPだけを対象とし、Firefox/WebKitや任意GUIへ一般化しない。
11. P0実測に基づき、CDP共通coreはrequest/response multiplexとbounded event queueまでとする。pipe/WebSocket framingと
    接続が所有するresourceのcloseだけをtransport adapterへ分け、discoveryやPage ownershipを共通化しない。
12. 複数CDP clientのscreencastはtarget sessionごとに独立して動作する。AFは自分のsessionだけをstart/stop/ACK/detachし、
    v1のAFは入力競合を避けるため同一targetのactive attachmentを1つに制限する。
13. attachmentの描画・入力は既存`/ws/browser`へ混在させず、専用`/ws/browser-attachments` namespaceへ分離する。
    CP relay coreは共有してよいが、Agent handlerと許可message集合を分ける。
14. MCP tool resultは`outputSchema`、短いJSON text、同一値の`structuredContent`を持つ。structured値をモデルへ渡さない
    CLIが実在するため、text fallbackを全CLIで必須の正とし、client別分岐は作らない。
15. action linkの配置は既存attachment focus、blank pane、新規slotの順とする。pane上限時は別paneを黙って上書きせず、
    active pane置換またはcancelをユーザーに選ばせ、cancelを既定focusにする。

## 採らなかった案

### 既存ブラウザペインの外部navigation制限を解除する

既存PageはAgent所有であり、外部自動化のPage・profile・sessionと同一ではない。制限解除だけではPlaywrightと同じPageを
共有できず、localhost境界も失う。所有モードを分ける。

### raw CDPをConsoleまたはMCPへ公開する

CDPは任意JS実行、cookie、network、target管理を含む強い権限である。Consoleに必要なのは描画と限定入力だけなので、
Agentで高水準wireへ縮退する。

### Xvfb + VNC/noVNC

Chromium以外も表示できるが、追加daemon、画面全体の転送、認可、解像度、clipboard、process ownershipが増える。
CDP screencast基盤が既にあるAFには過剰である。

### Playwright protocolへ接続する

Playwrightの`launch_server` protocolはCDPではなく、言語・版への結合が強い。Chromium標準のloopback CDPをownerとの
最小契約にする。

### MCP呼び出し時に自動でペインを開く

layoutはユーザー、テナント、ブラウザ端末ごとのlocal stateであり、Agentはどの端末に表示するか決められない。
予期しない画面変更にもなる。認証済みaction linkを返し、ユーザークリックで開く。

### 業務スクリプトをAFへ組み込む

AFがサイト固有selector、credential、規約、投稿状態を所有することになる。AFは汎用の人間操作surfaceとhandoffだけを
提供し、業務処理は各プロジェクトに残す。

## 帰結

- BrowserManagerのrequest/response multiplex、bounded event queue、screencast/input部分を再利用し、pipe/WebSocket framingと
  resource所有をtransport adapterへ分割する必要がある。`browserCDPEvent`の`*pipeCDP`型漏れも解消する。
- Console layoutへ`browserAttach` contentとaction routeが増えるが、port・target・外部URLは永続化しない。
- CP/Agentへ`/ws/browser-attachments`が増える。既存`/ws/browser`のlookupとwire契約は変更しない。
- attachmentは外部認証済み画面を表示し得るため、frame・入力・URL・title・console本文を永続化しない。
- ownerと人間の同時入力は上書き競合する。`user-control`前のowner停止を利用契約上の必須条件とし、v1は
  `view-only/user-control/locked`を表示・強制するが停止自体は検知・強制しない。
- MCPはtext fallbackとstructured resultを重複して返す。Claude/Codex/Copilotはstructured値を利用できる一方、
  opencode/Cursor/KiroはP0時点でtextだけをモデルへ渡す。
- 将来の完了自動報告は可能だが、MVPは状態取得で成立させ、永続handoff ledgerは後続段階とする。
