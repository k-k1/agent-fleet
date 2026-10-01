---
audience: "API の境界に触れる人"
source_of_truth: "コード（本書は地図と普遍設計。個々の要求 / 応答の形は code-as-contract）"
updated: "2026-09"
---

# 05. API 境界と中継 — 契約はコードが正

[English](05-api.md) | 日本語

境界は 2 つ: **公開面**（Console ↔ CP）と**内部面**（CP ↔ Workspace Agent）。その周りに
Workspace から CP への呼び出しがある（§5.2）。ルートは CP に約 550 本、Agent に約 320 本あり、
多数のファイルに分かれて登録されている。全一覧は各モジュールの `testdata/routes.golden` で、
`TestRouteTableGolden` が生成し直す。ルートを変えればここに差分が出るので、それがレビューの合図になる。
CP の一覧は環境変数で切り替わる登録（MCP・native の自己更新・エンジンゲートウェイ）を
すべて有効にして採っており、一部の配備にしか無いルートも含む。どの行がそれに当たるかは
`TestRouteTableConditionalRoutesAreKnown` が固定している。全列挙は保守不能なので、本書は
「グループ → 代表パス → 処理する側 → 詳細の所在」の**地図**に徹する。

パス・JSON のキー・エラーコード文字列を変えるのはワイヤの変更。形はルート一覧の隣のゴールデンが
固定している: `testdata/wire.golden`（Console が読む DTO のキー集合）と `testdata/wiremap.golden`
（まだ map を直接返す JSON の書き込み箇所）。

## 5.1 公開面（Console ↔ CP）

L1 ゲート（[07 §7.3](07-security.ja.md)）を通った後に到達する。認可は「自分のリソースのみ」＋
membership 検証（§5.4）。「中継」は CP が呼び出しをそのまま Agent へ転送すること（§5.3）、
「CP」は CP 自身が（多くは DB から）答えることを指す。

| グループ | 代表パス | 処理 | 詳細 |
|---|---|---|---|
| identity / tenant | `GET /api/whoami`・`GET /api/tenants`・`GET /api/version`・`GET/DELETE /api/me/login-methods` | CP | [03](03-control-plane.ja.md) |
| events | `GET /api/events`（SSE。下記）| CP | [ADR 0084](../decisions/0084-engine-indicator-and-tenant-gate.ja.md) |
| workspace | `GET /api/workspace`・`POST /api/workspace/{start,stop,recreate,clean-home,attention}`・`GET /api/workspace/{stats,machine}` | CP（Runtime）。`stats` は CP がホストの cgroup を読めない所では Agent に聞く。`machine` は Agent の実測と設定上の宣言を並べて返す | [03](03-control-plane.ja.md) |
| sessions | `GET/POST /api/sessions`・`DELETE /api/sessions/{name}`（ごみ箱へ。`?stop=1` で先に止める。`POST …/stop` はこの旧名）・ライフサイクル `POST …/{halt,recreate,archive,restore,fork,start,lock}`・意味論操作 `POST …/{turn,respond,settings,driver,plan-respond,carried-answer}`・端末操作 `POST …/{input,paste-image}`・`GET …/{status,output,messages,settings,skills,plan-file}`・`POST …/{keep-awake,stop-after-turn}`・タイトル／ブランチ／マーク／翻訳／引き継ぎ提案・`GET /api/sessions/archived` | 生成・fork・start・carried-answer は CP のハンドラ（セッション上限と自動起動）を通ってから中継。他は中継 | [04](04-agent.ja.md) / [ADR 0101](../decisions/0101-session-delete-via-trash.ja.md) |
| ↳ fork の任意ボディ | `POST …/fork` は `{"at": <anchorId>, "include": bool}` を取ると**発言時点からの分岐**になる（[ADR 0039](../decisions/0039-fork-at-message.ja.md)）。省略時は会話まるごとの分岐で後方互換。**壊れた JSON は `400 bad_request`（黙って全体分岐に倒さない）**。分岐点が使えない＝`400 fork_bad_anchor`、その種別／実行方式に時点指定が無い＝`400 fork_at_unsupported` | CP はボディを素通しで中継 | [04](04-agent.ja.md) |
| 共有と引き継ぎ | `/api/session-shares*`・`/api/shared-sessions*`（messages・marks・proposals）・`/api/session-share-proposals*`・`/api/session-handoff-offers*`・`GET /api/sessions/{name}/handoff-recipients` | CP。ACL は CP の DB にあり、所有者の Agent は CP 自身が読みに行く（§5.2）| [ADR 0050](../decisions/0050-transcript-marks.ja.md) / [ADR 0057](../decisions/0057-member-handoff.ja.md) |
| repos (SCM) | `GET/POST /api/repos`・`POST /api/repos/svn`・取り込みジョブ `GET /api/repo-jobs`・`DELETE /api/repo-jobs/{id}`・取り込み元なしの新規作業コピー `POST /api/repos/init`（`{name}` → `201 {repo}`。mkdir + `git init` だけなので**同期**＝取り込みジョブを経由しない）・`/api/repos/{name}/{status,branches,checkout,fetch,ff,parent-ff,changes,diff,log,graph,show,stage,unstage,discard,commit,identity,prompt-templates,recreate,lock}`・`/api/repos/{name}/{svn-update,svn-cleanup,svn-auth}`・プロジェクト MCP `/api/repos/{name}/mcp*`・`GET/PUT /api/git/identity` | 中継 | [04](04-agent.ja.md) / [ADR 0059](../decisions/0059-repo-import-jobs.ja.md) |
| ブランチ命名 | `GET /api/repos/{name}/branch-rule`（効いている規則・`?refresh=1` で Bitbucket を読み直す）・`POST /api/repos/{name}/branch-name`（`{item?, session?, kind?, slug?}` → `{name, name_empty, base, base_branch, kind, provisional, warnings, sources}`）・`POST /api/repos/{name}/branch-name/check`（`{name}` → `{warnings}`、拒否はしない）・`GET/PUT /api/branch-rules/user`（利用者層。ui-prefs とは別のストア）・`POST /api/branch-rules/preview`（`{template, items}` → `{names}`。課題管理の設定のプレビュー用で、利用者層と組み込み既定だけで描く）・`GET /api/repos/{name}/gitflow`（「Git Flow を初期化」の初期状態 `{current, prefill, local, origin, native, committed}`）・`POST /api/repos/{name}/gitflow/init`（`{expected, values}` → `{written, created, state}`。`created` は origin にだけある本番／開発ブランチを追跡させて作ったローカルブランチ。キーが `expected` と違えば 409 `gitflow_changed`、400 `branch_missing` / `invalid_value` は `field` 付き、500 `branch_failed` / `write_failed` は `created` と `written` 付き。上流を設定できずに残ったブランチは `untracked`）。`branch-name` は `gitflow` も返す（`suggest` でダイアログを勧める）。非 ASCII の題の英語スラグをブランチ名の AI 補助が作っている間は `provisional` が真になる（あとで聞き直すと得られ、そのとき `sources.slug` は `ai`）。作業項目からの起動は `POST /api/sessions` に `work_item` を載せてセッションの meta に残し、`POST /api/sessions/{name}/suggest-branch` は `kind` と `slug` も返す | 中継 | [ADR 0103](../decisions/0103-branch-naming-rules.ja.md) |
| fs | `GET /api/fs/{tree,search,file,download,changes,linemarks}`・`PUT /api/fs/file`・`POST /api/fs/{upload,mkdir,newfile,rename,resolve,suggest-edit}`・`DELETE /api/fs/delete` | 中継。`PUT /api/fs/file` だけは CP が先に封筒と稼働状態を検査する | [04](04-agent.ja.md) / [ADR 0027](../decisions/0027-markdown-code-editor.ja.md) |
| connections | `GET /api/connections`・ホスト別 git `PUT/DELETE /api/connections/git/{host}`・エージェント CLI のログイン（`/api/connections/{claude,codex,opencode,agy,cursor,kiro,muse}/…`）・ops／チャットブリッジ／SVN／Jira／llama.cpp の資格情報・`GET /api/git-oauth`（自テナントでどの OAuth ボタンを出せるか）| 中継。**ただし** git プロバイダの OAuth（GitHub の device flow、Bitbucket のコードグラントとコールバック）と Jira の OAuth の開始とコールバックは CP 自身が処理する（[ADR 0052](../decisions/0052-tenant-git-oauth.ja.md)）。ログインの start / poll は、状態が Agent プロセスのメモリにしか無いため、Workspace が落ち着くまで `409 workspace_starting` を返す | [08](08-integrations.ja.md) |
| 作業項目 | `GET /api/work-items`・`POST /api/work-items/{refresh,search,detail,comment}`・`/api/work-item-queries*`・`/api/work-item-sessions*` | CP。保存クエリと台帳は CP の行。取得は CP が Agent を呼ぶ（§5.2）。detail と comment は人の押下のときだけ中継 | [ADR 0061](../decisions/0061-work-item-inbox.ja.md) |
| chat / assistants | `/api/chat/conversations*`（ストリーミングは SSE、削除ロック `POST …/{id}/lock`）・`POST /api/chat/ask`・`/api/assistants*` | 中継 | [04](04-agent.ja.md) |
| 画像生成 | `GET /api/imagegen/{status,jobs,props,history,knowledge}`・`POST /api/imagegen/{jobs,queue,groups/{id}}`・`DELETE /api/imagegen/jobs/{id}`・`POST/PUT /api/imagegen/knowledge`・スタジオ `/api/imagegen/studios*`（`bind`・`press`・`rewind`・`draft-log`・`persona`。スタジオの `PUT` は `If-Match` を運ぶ）| 中継。すべて素の REST で、投入は即答しペインがポーリングする。Agent のブロッキングな `POST /imagegen/generate` は MCP ツールの入口で、CP にルートは無い | [ADR 0081](../decisions/0081-image-generation-pane.ja.md) / [ADR 0100](../decisions/0100-image-generation-studio.ja.md) |
| エンジン（メンバー）| `GET /api/engines/status` | CP。`engines` イベントストリームの REST フォールバック | [ADR 0084](../decisions/0084-engine-indicator-and-tenant-gate.ja.md) |
| AWS ログイン | `GET /api/aws-login`・`POST /api/aws-login/{id}/{start,cancel}`・`GET /api/aws-login/{id}/attempts/{attempt}`・`GET /api/aws-login/profiles`・`POST /api/aws-login/profiles/{name}/start` とその試行のポーリング | 中継。start と試行のポーリングは接続のログインと同じく `409 workspace_starting` を返しうる | [ADR 0102](../decisions/0102-aws-login-through-the-console.ja.md) |
| env / settings | `/api/env/{toolchains,ui-prefs,databases,jdk-install,node-install,tool-versions}`・`GET/PUT /api/env/ws-settings`・`POST /api/env/ws-settings/preview/reissue`・`/api/{claude,codex}/settings`・`GET /api/{claude,codex,copilot,muse}/usage`（agy は `GET /api/connections/agy/usage`）・`/api/agents/rtk`・`GET /api/agents/rtk/gain`・`GET /api/agents/{kind}/models`・`/api/user-notes*`・`GET /api/ai-assist/resolution` | `ws-settings` は CP（停止中も編集でき、起動時に適用）。他は中継 | [04](04-agent.ja.md) |
| memo | `GET/POST/PATCH/DELETE /api/memos*`・`/api/memo-categories*`・`POST /api/memos/flush`・`POST /api/memos/paste-image`・`GET /api/memos/images/{file}`・`POST /api/memos/images/gc` | CP。flush と画像添付は Agent へ届く | [03](03-control-plane.ja.md) |
| notifications | `GET /api/notifications`・`POST /api/notifications/{seen,usage-observations}` | CP。Agent の送信箱を CP が吸い上げる（§5.2）| [03](03-control-plane.ja.md) |
| schedules | `GET /api/schedules`・`GET …/{id}/runs`・`PATCH/DELETE …/{id}`・`POST …/{id}/{pause,resume,run-now}`。**Console は一覧と編集だけで、作成は MCP ツール経由** | CP（DB とスケジューラ）| [ADR 0021](../decisions/0021-scheduled-execution.ja.md) |
| 掃除とごみ箱 | `GET /api/sessions/{usage,cleanup}`・ごみ箱 `GET /api/cleanup/archives`・`POST …/archives/{id}/restore`・`DELETE …/archives/{id}`・`DELETE /api/cleanup/archives?older_than_days=N`・`GET /api/cleanup/usage`・`DELETE /api/cleanup/cache/{feature}`・`/api/cleanup/{tool-caches,leftovers}*` | 中継 | [ADR 0101](../decisions/0101-session-delete-via-trash.ja.md) |
| フリート俯瞰図 | `GET /api/fleet-graph` | 中継 | [ADR 0096](../decisions/0096-fleet-session-graph.ja.md) |
| agent memory | `GET /api/agents/memory/{roots,snapshots,diff,tree,export}`・`POST …/{snapshots,restore,import,import/apply}`・`PUT …/settings` | 中継 | [ADR 0022](../decisions/0022-agent-memory-management.ja.md) |
| MCP レジストリ | `GET/POST /api/mcp-servers`・`PUT/DELETE …/{id}`・`POST …/{test,tenant-refresh}`・`POST …/{id}/enabled`・`PUT …/{id}/secrets` | 中継（実効レジストリは Agent が組む）。テナント配布の定義は admin API の `/api/admin/mcp-servers*` | [ADR 0031](../decisions/0031-mcp-registry.ja.md) |
| 使用量と費用 | `GET /api/usage/series`・`GET /api/usage/me/hourly`・`GET /api/cost/{profile,me}` | 時系列は中継。時間別の稼働とクラウド費用は CP | [ADR 0029](../decisions/0029-usage-accounting.ja.md) / [ADR 0066](../decisions/0066-uptime-heatmap.ja.md) / [ADR 0048](../decisions/0048-member-cloud-cost.ja.md) |
| pat | `GET/POST /api/pat`・`DELETE /api/pat/{id}` | CP | [07 §7.6](07-security.ja.md) |
| ssm | `GET/POST/PUT/DELETE /api/ssm/{profiles,hosts}*`・`POST /api/ssm/instances`・`GET /api/sessions/{name}/ssm-login` | プロファイルとホストは CP。インスタンス照会は CP がプロファイルを解決してから中継。ログイン状態は中継 | [08](08-integrations.ja.md) |
| egress（メンバー）| `GET /api/egress/check`・`POST /api/egress/propose` | CP。提案で作られるのは *proposed* の許可リスト項目だけで、承認は super_admin | [07 §7.8](07-security.ja.md) |
| TTS | `POST /api/tts/{synthesize,wake}`・`GET /api/tts/{status,speakers,dict}` | CP（音声エンジンを呼ぶ）| [ADR 0070](../decisions/0070-tts-ondemand-engine.ja.md) |
| internal git | `/api/internal-git/repos*`（管理と読み取り専用の閲覧）・`/git/{slug}/{repo...}`（smart HTTP）・`/git/{slug}/{repo}/info/lfs/*` | CP（**Agent を経由しない**）| [91](91-internal-git.ja.md) |
| admin | テナント（メンバー・上限・ログイン規則・ネットワーク・スロットクラス・サインイン手段・git OAuth アプリ）・membership と Workspace（ホームの掃除・破棄・メンバーのホームのバックアップ `GET/DELETE /api/admin/tenants/{slug}/members/{key}/home-backups`）・利用者上限とロール・ホスト・EC2 プール・Workspace サイズ・稼働とクラウド費用・セッション・監査・egress・MCP 配布・ブランディング・TTS・エンジン（`/api/admin/engines*`）| CP、ロールで制限（§5.4）| [03](03-control-plane.ja.md) |
| MCP | `/mcp`（Streamable HTTP JSON-RPC・Bearer PAT・セッションゲートの外）。`AF_MCP_ENABLED=true` のときだけ登録 | CP | [03 §3.5](03-control-plane.ja.md) / [ADR 0006](../decisions/0006-mcp-unified.ja.md) |
| preview | `/preview/{port}/{rest...}`（`/preview/{port}` は末尾 `/` を足す 301）・ホストモード `{slug}-{port}.<AF_PREVIEW_DOMAIN>` と `/preview-auth`・`/preview-open`・`GET /api/preview/shared` | CP → Agent `/proxy/{port}/…` | §5.3 / [ADR 0062](../decisions/0062-preview-subdomain.ja.md) |
| browser | `POST /api/browser/pages`・`GET/DELETE /api/browser/pages/{id}`・`GET /api/browser/attach-targets`・`/api/browser/attachments*`・`GET /ws/browser`・`GET /ws/browser-attachments`。`GET /open/browser-attachment/{id}` は Console のシェルを返す | CP → Agent | §5.3 / [ADR 0018](../decisions/0018-container-browser-pane.ja.md) / [ADR 0038](../decisions/0038-chromium-attach-view.ja.md) |
| terminal | `GET /ws/terminal?session=&tenant=` | CP → Agent `/ws/pty` | §5.3 |
| その他 | `GET /api/drawio/stencils*`（CP 側のキャッシュ。[ADR 0046](../decisions/0046-drawio-viewer.ja.md)）・`GET /api/update/status`・`POST /api/update/apply`（native のみ。[ADR 0025](../decisions/0025-native-auto-update.ja.md)）・ログインと OAuth（`/login`・`/login/{slug}`・`/oauth2/{login,callback,logout,link}`）・`GET /healthz` と `GET /readyz`（[09 §9.9](09-deploy.ja.md)）・`/manifest.webmanifest` と `/brand/*`・`/agent-fleet*`（旧ブックマーク向けにルート直下の同じパスへ 302）・`/`（Console）| CP | [07](07-security.ja.md) |

- **`GET /api/events`** はタブごとに 1 本の SSE で、Console の常設ポーリングを置き換える。4 秒ごとに
  JSON が変わったストリームだけを `data: {"stream": <名前>, "data": <対応する REST の本文>}` で送る。
  ストリームは `workspace`・`stats`・`sessions`・`notifications`・`workitems`・`engines` で、
  20 秒黙るとコメントの ping を送る。このルートを持たない CP は 404 を返し、Console は REST の
  ポーリングに戻る。このストリームはアイドル停止の活動に数えない。
- **長い操作は即答してポーリングする。** Workspace の起動は返った後に状態を見張る。clone と svn
  checkout は取り込みジョブ（`202 {job}`、`GET /api/repo-jobs` でポーリング）。画像生成は投入して
  ペインがキューをポーリングする。汎用のジョブキューは無い。
- **自動起動。** `AF_AUTOSTART` が有効（既定）なら、セッションの生成・fork・start・carried-answer と
  `POST /api/ssm/instances` は停止中の Workspace を先に起動する。これ以外は起こさない:
  端末の接続も、`attention` も、イベントストリームも起こさない。
- **保持ルート**: モデルの応答は ingress の idle timeout（60 秒）より長くかかり得るため、
  `POST /api/chat/conversations/{id}/{compact,plan/refresh}`・`POST /api/chat/ask`・
  `POST /api/fs/suggest-edit` は `Accept: text/event-stream` 付きの要求に 200 で応じ、
  20 秒ごとに `: keepalive` コメントを送り、最後に
  `data: {"status": <ステータス>, "body": <JSON 本文>}` を 1 フレーム返す。ヘッダが無ければ
  素の JSON（MCP の `ask_assistant` ツールはこちら）。CP は flush する stream 中継で通す
  （Agent 側は `httpx.HeldOpen`）。
- `GET /api/workspace/stats` は稼働中なら `{running: true, mem_used, mem_max?, cpu_pct?,
  oom_kill_total?, oom_recent?}` を返す。`docker` では CP がホストからコンテナの cgroup を読み、
  それ以外では Agent の `GET /workspace/stats` に聞く。`oom_recent` はどちらの経路でも CP が
  kill カウンタの増加から導く。停止中は `{running: false}` で、停止したコンテナを CP が検査できる
  `docker` でだけ `oom_killed` と `exit_code` が加わる（[ADR 0014](../decisions/0014-agent-exit-recording.ja.md)）。
  セッション単位の終了理由（`exitReason`・`exitCode`・`exitSignal`）は `GET /api/sessions` の各要素に載る。
- `GET /api/sessions` は稼働中は Agent から、停止中は CP が最後に写した一覧から返すので、
  止まったセッションも見えて再開できる。

## 5.2 内部面（CP ↔ Agent）

- **到達性。** Agent は `AGENT_ADDR`（既定 `:7700`）で待ち受ける。`docker` ではそのポートを
  ホストのループバックにだけ publish し、`native` はループバックに bind し、AWS のターゲットは
  セキュリティグループで絞る（[07 §7.2](07-security.ja.md)）。Workspace の中のプロセスも
  ループバックで呼ぶ: af MCP サーバとフックは同じ `AGENT_TOKEN` を使う。
- 全リクエストが per-container の Bearer トークンを運ぶ（CP が起動時に注入）。Agent は `/healthz`
  以外すべてで**定数時間比較**で検証し、合わなければ `401 unauthorized` を返す
  （[07 §7.5](07-security.ja.md)）。
- **パス規約: CP は `/api` を剥がして残りをそのまま転送する**
  （`/api/sessions/x/halt` → `<agent>/sessions/x/halt`）。**CP のルート表は明示的な許可リスト**で、
  `control-plane/routes.go` に行が無い Agent のルートには Console から届かない。`console/src` に
  書かれた `api/...` のパスを受けられる CP のルートが無ければ `TestConsoleAPIPathsHaveCPRoutes`
  （`control-plane/console_routes_test.go`）が落ちる。`${…}` の段はルートのパラメータに、
  パラメータが無い位置ではどのリテラルの段にも一致する。中継は `.`・`..`・途中の空セグメントを
  含むパスを 400 で断り、Agent のリダイレクトは追わない。付ける `X-AF-Relay: cp` は Agent の
  ログ用のヒントで、Agent はこれで何も判断しない。
- Agent 固有の面は `/ws/pty`・`/ws/browser`・`/ws/browser-attachments`・`/browser/*`・
  `/proxy/{port}/{rest...}`。

Session API は**意味論**操作と**端末**操作を分ける。turn・respond・settings は driver に依らず、
Agent が managed driver の構造化 API か TUI へのキー入力に振り分ける。`/input` はどちらの driver でも
プロンプトを取るが、生の `keys`・`seq` は TUI だけ。`/output` は転写を持つ種別が要り、PTY ソケットは
pane を持つセッションにしか無い。`/driver` は 1 つの会話を停止→再開で別の実行方式へ移す。移し先の
方式が無い種別は `400 driver_unsupported`、ターンの途中は `409 busy_switch` で断る。codex を managed から
Terminal へ移せるのは停止後だけで（`409 codex_stop_first`）、共有 app-server がまだ読み込んでいる codex の
スレッドを Terminal で起動する要求は、`/driver` でも `/start` でも `409 codex_releasing` で断る。

**CP が自分で呼ぶもの。** これらに通じる Console のルートは無い:

- `GET /sessions` — CP が自分の DB に写す一覧。
- `GET /workspace/{stats,machine}`。
- `GET /notifications` と `POST /notifications/ack` — CP が吸い上げる通知の送信箱。
- `POST /work-items/fetch` — CP が持つ保存クエリの、タイマー駆動の取得。
- `GET /sessions/catalog`・`GET /share-operations/{key}`・`GET /sessions/{name}/handoff-context`
  — 共有と引き継ぎ。
- `POST /engine/usage` と `POST /engine/catalog-changed` — エンジンゲートウェイが数えたトークンと、
  モデルカタログの変更通知。
- `POST /assistant-turns` — スケジュール実行のアシスタントのターン。

Workspace 内の af MCP サーバには、CP がルートを持たない専用の入口がある（`POST /imagegen/generate`・
`GET /sessions-idempotency/{key}`・`POST /chat/report` など）。

**Workspace から CP への呼び出し。** どれもセッションゲートの外にあり、自分で認証する。
membership ごとのトークンは 1 つの membership 向けに署名されており、CP は呼び出しのたびにその
membership を解決し直す。

| パス | 資格情報 | 用途 |
|---|---|---|
| `/internal/memos*`・`/internal/memo-categories*` | `AF_MEMO_TOKEN` | オペレーターのメモツール |
| `/internal/schedules*` | `AF_SCHEDULE_TOKEN` | オペレーターのスケジュールツール（作成を含む）|
| `GET /internal/mcp-servers` | `AF_MCP_TOKEN` | テナント配布の MCP サーバのポーリング |
| `GET /internal/docs` | `AF_DOCS_TOKEN` | ユーザーガイドを tar.gz で（[04 §4.9](04-agent.ja.md)）|
| `GET /internal/aws-profiles` | `AF_AWS_PROFILES_TOKEN` | メンバーのプロファイルを `~/.aws/config` に書く |
| `POST /internal/git-oauth/{bitbucket,jira}/refresh` | `AF_GIT_OAUTH_TOKEN` | refresh grant を CP が代行し、テナントの client secret を CP に残す（[ADR 0052](../decisions/0052-tenant-git-oauth.ja.md)）|
| `POST /internal/engine/token`・`GET /internal/engine/catalog` | `AF_ENGINE_ISSUE_TOKEN` | セッション単位のエンジントークンと、起動メニューが読むエンジンのカタログ |
| `/engine/{key}/v1/{path...}`・`GET /engine/{key}/props` | セッション単位のエンジントークン（セッションの `AF_ENGINE_TOKEN`）| **エンジンゲートウェイ**。Workspace が自前エンジンに届く唯一の道で、エンジンのある配備でだけ登録される。ストリーミングの要求には即 200 を返し、エンジンが起きるまで 10 秒ごとにコメント行を送る。非ストリーミングの要求は ingress の idle timeout 未満だけ保持し、その後 `Retry-After` 付きの `503 engine_waking` を返す（[ADR 0071](../decisions/0071-self-hosted-inference-engines.ja.md)）|
| `/mcp` | PAT | MCP エンドポイント（§5.1）|
| `/git/{slug}/{repo...}` | git トークン（Basic 認証）| 内部 git プロバイダ（[91](91-internal-git.ja.md)）|

`POST /internal/egress` へ送り `GET /internal/egress/policy` を読むのは Workspace ではなく egress
プロキシで、配備全体で 1 つの `AF_EGRESS_TOKEN` を使う（[07 §7.8](07-security.ja.md)）。

## 5.3 中継の 5 経路

| 経路 | 入口 → 出口 | 特性 |
|---|---|---|
| **REST** | `/api/*` → Agent の同じパス | 活動に数えるのは変更系だけで、GET のポーリングは Workspace を温めない。変更系は 2xx で監査に記録する（§5.5）。中継自身は状態を見ないので、稼働していない Workspace は `502`（"workspace agent unreachable"）になる。停止処理中の書き込みは `409 workspace_stopping`。スケジュールがまだ使っているセッションやリポジトリの削除は `409 schedule_in_use` |
| **SSE** | チャットのストリームと保持ルート → Agent | チャンクごとに flush |
| **WS** | `/ws/terminal` → Agent の `/ws/pty` | 先に状態を見る（**自動起動しない**）: `409 workspace_starting` か `409 workspace_stopped`。その後は双方向に中継する（binary＝PTY 出力、text＝入力と resize）。どちら側の close フレームも素通しする。アイドル停止の在席に数えるのはキー入力のフレームだけで、ping・resize・ソケットが開いていることは数えない |
| **browser** | ページとアタッチの API とそのソケット → Agent の同名 | membership と稼働を検査した後は Bearer を足すだけ。**本文と text フレームは解釈せず**、binary の JPEG フレームは最新だけを中継する。Workspace を温めるのは*表示中の*ビューアだけ |
| **preview** | `/preview/{port}/…` または `{slug}-{port}.<AF_PREVIEW_DOMAIN>` → Agent の `/proxy/{port}/…` → コンテナ内の `127.0.0.1:{port}` | 2 つの URL の形に 1 つのリバースプロキシを使うので、WebSocket とストリーミング応答も通る。`X-Forwarded-{Host,Proto}` を付け、パスのルートでは `X-Forwarded-Prefix` も付ける（アプリがこのプレフィクスを尊重する必要がある）。Agent は `Authorization` を外し、CP 自身のログイン cookie はアプリに届かない。パスのルートでは新しいタブがヘッダを運べないので、テナントは `?tenant=`、次にポートごとの cookie から決まる。ホストのルートは Console のセッションゲートの外にあり、専用のハンドシェイクと cookie（`/preview-auth`）を持つ。これを通した Vite の HMR はまだ確かめていない（#968・[ADR 0062](../decisions/0062-preview-subdomain.ja.md)）|

## 5.4 横断規約

- **テナント選択**: `X-AF-Tenant` ヘッダ。WebSocket・preview・新しいタブは `?tenant=` に落ちる。
  所属 1 件なら自動で決まり、複数で未指定は `409 tenant_selection_required`、所属していない
  テナントは `403 forbidden_tenant`。テナントのネットワーク規則が呼び出し元のアドレスを拒むと
  `403 ip_not_allowed`。
- **エラー形**: CP も Agent も `{"error": {"code": <文字列>, "message": <文字列>}}`。
  **契約はコードの方**で、Console は `err.<code>`（`console/src/lib/i18n/locales/*/errors.ts`）で
  訳すので、1 つ改名すれば両側にまたがるワイヤの変更になる。多くは各モジュールの `errcodes.go` に
  宣言されている。主なステータス: 401 `unauthenticated`／403（membership・ネットワーク・ロール）／
  409（テナント選択・Workspace の状態・実行中のセッション）／429（`quota_sessions` ほかの上限。
  既定は無制限。いくつかの流量制限）／中継が Agent に届かないときの 502／エンジンゲートウェイの
  503 `engine_waking`・`engine_off`。
- **認可**: 自分の Workspace・リポジトリ・セッションのみ。admin API はロールで制限する。
  super_admin は配備全体を、tenant_admin は自テナントだけを見る（テナント別のルートではハンドラの中で
  検査する）。エンジンの一覧と取り込みのルートは、オペレーターが取り込みを許したテナントの
  tenant_admin も通す。
- **キャッシュ**: 通常の `200` の JSON `GET` には弱い `ETag` を付け、本文が変わらなければ `304` を返す
  （`etagJSON`）。`no-store` の応答、4 MiB を超える本文、途中で flush するハンドラ、SSE、
  ファイルのダウンロードは素通し。Console のハッシュ無しの入口（`index.html`
  など）は `no-store` で配るので、デプロイは次の読み込みで効く。`/assets/` 以下のハッシュ付き
  ファイルは immutable として 1 年キャッシュさせる。

## 5.5 監査の書き込み点

REST 中継は**変更系**の操作を 2xx のときに記録する（`auditActionTarget`）。対象は URL のパスか
クエリから取る。例外は `PUT /api/fs/file` で、CP が検証済みの JSON 本文の `path` を使う。
ファイルの内容は監査に残さない:

- ファイルシステムへの書き込み（`fs.*`）
- リポジトリの clone・svn checkout・削除と、取り込みジョブの取り消し（`repo.*`）
- git の commit・discard・checkout・fetch・ff・parent-ff（`git.*`）
- memory の snapshot・import・restore（`memory.*`）
- AWS ログインの start と cancel（`aws.login.*`）
- セッションの生成・fork・削除（`session.*`。`POST …/stop` は実態どおり削除として記録）

特別な場合が 2 つある:

- `GET /api/agents/memory/export` — 個人の memory を環境の外へ持ち出す唯一の経路なので、
  監査する唯一の読み取り
- `write_state_unknown` で失敗した `PUT /api/fs/file`

これに加えて、admin API・MCP の書き込みツール・reaper などのシステム動作が各ハンドラの中で記録する。
スキーマは [06](06-data.ja.md)、運用の視点は [07 §7.7](07-security.ja.md)。
