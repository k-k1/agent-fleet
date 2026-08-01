# 0037. 外部所有 Chromium はloopback CDPへattachし、ユーザークリックでConsoleペインへ開く

- 状態: 採用・設計確定（未実装、2026-08-01）
- 関連: [53-chromium-attach-view.md](../53-chromium-attach-view.md) / [0018-container-browser-pane.md](0018-container-browser-pane.md)

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

- BrowserManagerのCDP transportとscreencast/input部分を、AF所有pipe接続と外部所有WebSocket接続で再利用できるよう
  分割する必要がある。
- Console layoutへ`browserAttach` contentとaction routeが増えるが、port・target・外部URLは永続化しない。
- attachmentは外部認証済み画面を表示し得るため、frame・入力・URL・title・console本文を永続化しない。
- ownerと人間の同時入力は競合し得る。v1は`view-only/user-control/locked`を表示・強制するが、ownerのpauseは協調契約とする。
- 将来の完了自動報告は可能だが、MVPは状態取得で成立させ、永続handoff ledgerは後続段階とする。
