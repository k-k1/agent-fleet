# 05. API 境界と中継 — 契約はコードが正

> 正: コード（本書は地図と普遍設計。逐一の請求/応答形は code-as-contract）/
> 主な更新トリガ: API グループの追加・中継経路の変更 / 最終確認: 2026-07

境界は 2 つ: **公開面**（Console ↔ CP、`/api/*` ほか）と**内部面**（CP ↔ Workspace Agent）。
ルート定義は CP 約 300 本・Agent 約 200 本あり（増え続けるので概数。実数は
`grep -c HandleFunc control-plane/routes.go workspace/agent/routes.go` で計測できる）
全列挙は保守不能なので、本書は「グループ → 代表パス → 詳細の所在」の**地図**に徹する。**内部リファクタ（docs/23）はワイヤ完全互換（パス・JSON 形・
エラーコード文字列を変えない）がハード制約のため、本書の記述はリファクタで陳腐化しない。**

## 5.1 公開面の地図（Console ↔ CP）

L1 認証（authGate）通過後に到達。認可は「自分のリソースのみ」＋ membership 検証が原則（§5.4）。

| グループ | 代表パス | 処理場所 | 詳細 |
|----------|---------|----------|------|
| identity / tenant | `GET /api/whoami`・`GET /api/tenants` | CP | [03](03-control-plane.md) |
| workspace | `GET /api/workspace`・`POST /api/workspace/{start,stop,recreate,clean-home}`・`GET /api/workspace/stats` | CP（Runtime）| [03](03-control-plane.md) |
| sessions | `GET/POST /api/sessions`・lifecycle `POST …/{stop,halt,recreate,archive,restore,fork,start}`・意味論操作 `POST …/{turn,respond,settings,driver}`・端末操作 `POST …/{input,paste-image}`・`GET …/{status,output,messages,settings}`・title/branch 系・`GET /api/sessions/archived`・削除ロック `POST …/lock`（docs/45）| 生成/fork/start=CP→Agent、他は中継 | [04](04-workspace-agent.md) |
| ↳ fork の任意ボディ | `POST …/fork` は `{"at": <anchorId>, "include": bool}` を取ると**発言時点からの分岐**になる（docs/55）。省略時は従来の会話まるごと分岐で後方互換。壊れた JSON は `400 bad_request`（黙って全体分岐に倒さない）。分岐点が使えない＝`400 fork_bad_anchor`、その種別/起動方式に機能が無い＝`400 fork_at_unsupported` | CP はボディを素通し中継 | [04](04-workspace-agent.md) |
| repos (SCM) | `GET/POST /api/repos`・取り込み元なしの新規作業コピー `POST /api/repos/init`（`{name}` → `201 {repo}`。mkdir + `git init` だけなので**同期**＝取り込みジョブを経由しない）・`/api/repos/{name}/{status,branches,checkout,fetch,ff,changes,diff,log,graph,show,stage,unstage,discard,commit,identity,prompt-templates}`・削除ロック `POST /api/repos/{name}/lock`（docs/45）| 中継 | [04](04-workspace-agent.md) |
| fs | `GET /api/fs/{tree,file,download,changes,linemarks}`・`PUT /api/fs/file`・`POST /api/fs/{upload,mkdir,newfile,rename,delete,suggest-edit}` | 中継 | [04](04-workspace-agent.md) / [docs/44](../44-markdown-code-editor.md) |
| connections | `GET /api/connections`・git `PUT/DELETE /api/connections/git/{host}`（+ GitHub Device / Bitbucket OAuth / claude / codex / opencode）・`GET /api/git-oauth`（自テナントでどの OAuth ボタンを出せるか）| 中継。ただし **git プロバイダの OAuth は両方とも CP**（GitHub の device flow `start`/`poll`・Bitbucket の `start` と callback・`/api/git-oauth`）＝[71](../71-tenant-git-oauth.md) | [08](08-integrations.md) |
| chat / assistants | `/api/chat/conversations*`（stream は SSE、削除ロック `POST …/{id}/lock`）・`POST /api/chat/ask`・`/api/assistants*` | 中継 | [04](04-workspace-agent.md) |
| env / settings | `GET/PUT /api/env/{toolchains,ui-prefs}`・`POST/GET /api/env/jdk-install`（JDK ワンボタン導入・[09 §JDK](09-deploy.md)）・`GET/PUT /api/env/ws-settings`・`GET/PUT /api/claude/settings`・`GET /api/{claude,codex,copilot}/usage`（各 WsBar 使用量チップ。claude/codex=サブスク枠、copilot=アカウント
クレジット残量。応答にプランと利用アカウントを含む。agy は `GET /api/connections/agy/usage`）・`GET/PUT /api/agents/rtk`・`GET /api/agents/rtk/gain`（rtk 節約履歴＝使用量タブ「rtk 効果」カード、`rtk gain --all --format json` 素通し）| ws-settings=CP、他は中継 | [04](04-workspace-agent.md) |
| memo | `GET/POST/PATCH/DELETE /api/memos*`・`POST /api/memos/flush` | CP（flush 時のみ Agent へ）| [03](03-control-plane.md) |
| notifications | `GET /api/notifications`・`POST /api/notifications/{seen,usage-observations}` | CP（DB）| [03](03-control-plane.md) |
| schedules | `GET /api/schedules`・`GET …/{id}/runs`・`PATCH/DELETE …/{id}`・`POST …/{id}/{pause,resume,run-now}`（Console は一覧・管理のみ。作成は MCP ツール経由）| CP（DB + scheduler）| [docs/38](../38-scheduled-execution.md) |
| cleanup | `GET /api/sessions/{usage,cleanup}`・`DELETE /api/sessions/{name}`・`GET /api/cleanup/archives`・`POST /api/cleanup/archives/{id}/restore`・`DELETE /api/cleanup/archives/{id}` | 中継 | [04](04-workspace-agent.md) |
| agent memory | `GET /api/agents/memory/{roots,snapshots,diff,tree,export}`・`POST …/{snapshots,restore,import,import/apply}`・`PUT …/settings` | 中継 | [docs/39](../39-agent-memory-management.md) |
| MCP レジストリ | `GET/POST /api/mcp-servers`・`PUT/DELETE …/{id}`・`POST …/{test,tenant-refresh}`・`POST …/{id}/enabled`・`PUT …/{id}/secrets` | 中継（実体は Agent）| [docs/48](../48-mcp-registry.md) |
| usage 時系列 | `GET /api/usage/series`（機能別トークン台帳の時系列）| 中継 | [docs/46](../46-usage-accounting.md) |
| pat | `GET/POST/DELETE /api/pat*` | CP | [07 §7.6](07-security.md) |
| ssm | `GET/POST/PUT/DELETE /api/ssm/{profiles,hosts}*`・`GET /api/sessions/{name}/ssm-login` | CP（DB）+ Agent（セッション）| [08](08-integrations.md) |
| internal git | `GET/POST/DELETE /api/internal-git/repos*`（管理）・`/git/{slug}/{repo...}` smart-HTTP・`/git/…/info/lfs/*` | CP（Agent を経由しない）| [91](91-internal-git.md) |
| admin | `GET /api/admin/{tenants,sessions,usage,audit,host,egress*}`・`POST /api/admin/{tenants,memberships,stop-workspace,clean-home}`・`PUT /api/admin/{tenants/{slug}/limits,user-limits,membership-role,egress/mode}`・git プロバイダ OAuth `GET /api/admin/tenants/{slug}/git-oauth`＋`PUT/DELETE …/git-oauth/{provider}`（tenant_admin・承認なし・[71](../71-tenant-git-oauth.md)）| CP（super_admin / tenant_admin gate）| [03](03-control-plane.md) |
| MCP | `POST /mcp`（Streamable HTTP JSON-RPC・Bearer PAT・authGate 除外）| CP | [03](03-control-plane.md) / [decisions/0006](../decisions/0006-mcp-unified.md) |
| preview | `GET /preview/{port}/{rest...}`（`/preview/{port}` は 301 で末尾 `/` 付与）| CP → Agent `/proxy/{port}` | §5.3 |
| browser | `POST/GET/DELETE /api/browser/pages*`・`GET /ws/browser?id=&tenant=` | CP → Agent `/browser/pages*`・`/ws/browser` | §5.3 / [設計31](../31-container-browser-pane.md) |
| WebSocket | `GET /ws/terminal?session=&tenant=` | CP → Agent `/ws/pty` | §5.3 |
| internal（Agent → CP・per-membership トークン）| `GET /internal/{memos,schedules,mcp-servers,docs}`・**`POST /internal/git-oauth/bitbucket/refresh`**（`AF_GIT_OAUTH_TOKEN`。テナントの client_secret を CP に残したまま refresh grant を代行＝[71](../71-tenant-git-oauth.md) §71.8）| CP | [08](08-integrations.md) |
| auth / その他 | `GET /login`・`/oauth2/{login,callback,logout}`・`GET /api/oauth/bitbucket/callback`・`GET /healthz`・`/internal/egress{,/policy}`（`AF_EGRESS_TOKEN`）・`GET /internal/docs`（`AF_DOCS_TOKEN`・ロール別 docs の tar.gz／[04 §4.9](04-workspace-agent.md)）・`/` = Console 静的配信（no-store）| CP | [07](07-security.md) |

- 旧 `/agent-fleet` プレフィクスは**廃止**（ルート配信）。`/agent-fleet*` は互換リダイレクトのみ。
- 非同期操作（起動・clone）は**同期 + ポーリング**で運用（`/jobs` 構想は未採用）。
- `GET /api/workspace/stats` の応答は CP がホストから cgroup v2 を直読みして組む（`metrics.go`）:
  稼働時 `{running:true, mem_used, mem_max?, cpu_pct?, oom_kill_total?, oom_recent?}`、
  停止時 `{running:false, oom_killed?, exit_code?}`。`oom_recent`/`oom_killed` は OOM 検知
  （コンテナ内子プロセスの OOM kill／コンテナ丸ごとの OOM 落ち）で、設計は [26](../26-agent-exit-recording.md)。
  セッション単位の終了理由（`exitReason`/`exitCode`/`exitSignal`）は `GET /api/sessions` の各要素に載る（同 docs/26 Phase 2）。

## 5.2 内部面（CP ↔ Agent）

- Agent は外部公開されず、CP からのみ内部到達（`127.0.0.1` publish + `af-net-<user>`）。
- 全リクエストに `Authorization: Bearer <AGENT_TOKEN>`（per-container、CP が起動時注入）。
  Agent の `requireToken` が `/healthz` 以外を定数時間比較で検証（[07 §7.5](07-security.md)）。
- パス規約: CP は公開パスから **`/api` を剥がして**そのまま Agent へ転送する
  （例 `/api/sessions/x/stop` → `<agent>/sessions/x/stop`）。Agent 固有の面は
  `/ws/pty`（PTY）、`/ws/browser`（browser描画/操作）、`/browser/pages*`（ephemeral Page）、
  `/proxy/{port}/{rest...}`（preview 下請け）。
- Agent のグループ構成は公開面と同型: `/sessions`・`/repos`・`/connections`・
  `/fs`(10)・`/chat`(10)・`/assistants`・`/env`・`/claude`・`/codex`・`/git`・`/agents`。

Session API は論理操作と端末操作を分ける。`/turn`（start/steer/interrupt）・`/respond`・
`/settings` は driver 非依存の意味論で、Agent が managed の構造化 API または tui のキー入力へ
振り分ける。`/input`・`/output`・`/ws/pty` は pane を持つ tui 専用。`/driver` は Codex / OpenCode の
同一会話を stop→resume で切り替え、busy 中は `409 busy_switch` を返す。

## 5.3 中継の5経路（CP の proxy 層）

| 経路 | 入口 → 出口 | 特性 |
|------|-------------|------|
| **REST**（proxyAgentREST）| `/api/*` → Agent 同パス（`/api` 剥がし）| 変更系は 2xx 応答時に監査記録（§5.5）。workspace が running でなければ 409 |
| **SSE**（proxyAgentStream）| `POST /api/chat/conversations/{id}/stream` → Agent | チャンク毎 flush。チャットのストリーミング用 |
| **WS**（proxyTerminal）| `GET /ws/terminal` → Agent `/ws/pty` | running 確認（stopped/starting=409、自動起動しない）→ 双方向リレー（binary=PTY 出力 / text=入力・resize）。接続追跡が workspace を warm 維持（reaper のアイドル判定に使用）|
| **browser** | `/api/browser/pages*` → Agent `/browser/pages*`、`/ws/browser` → Agent同名 | membership/running検査後にAgent bearerだけを付与。REST本文/応答とWS textは解釈せず、binary JPEGはlatest-onlyで中継。visible viewerだけがworkspaceをwarm維持。wire v1は[設計31](../31-container-browser-pane.md) |
| **preview** | `GET /preview/{port}/{rest...}` → Agent `/proxy/{port}/{rest...}` → コンテナ内 `127.0.0.1:{port}` | `X-Forwarded-{Prefix,Host,Proto}` 付与、Agent 側は Authorization を除去して ReverseProxy。新タブは cookie 以外のヘッダを運べないため `?tenant=<slug>` の query fallback で解決。**制約: HTTP のみ（WS/SSE 不可＝HMR 不可）**。アプリ側は `X-Forwarded-Prefix` 尊重の設定（例 Spring Boot `server.forward-headers-strategy`）が必要 |

## 5.4 横断規約

- **テナント選択**: `X-AF-Tenant` ヘッダ（WS/preview/新タブは `?tenant=` query fallback。header→query の順で解決）。
  membership 検証: 所属 1 件は自動 / 複数で未指定=409 / 非所属=403。
- **エラー形**: JSON + 安定したエラーコード文字列（const 化済み・ワイヤ互換の一部）。
  代表: 401（未認証）/ 403（membership 外）/ 404 / 409（`workspace_stopped`・`starting`・テナント未指定・状態競合）/
  429（クォータ超過、既定は無制限）。
- **認可の原則**: 自分の workspace / repos / sessions のみ。admin API は role gate
  （super_admin はデプロイ全体、tenant_admin は自テナントのみ）。
- Console のビルド成果物（`console/dist`）は CP が `no-store` で配信（デプロイ即反映）。

## 5.5 監査の書き込み点

proxyAgentREST は**変更系**操作の 2xx 応答時に `audit_log` へ記録する（分類は auditActionTarget）:
`fs.*`（upload/mkdir/rename/delete…）、`repo.*`（clone/delete）、`git.{commit,checkout,fetch,ff,discard}`、
`session.{create,fork,stop}` など。加えて admin API・MCP write ツール（`actor_kind=mcp`）・
システム動作（reaper 等）が各処理内で記録する。スキーマは [06](06-data-model.md)、運用視点は [07 §7.7](07-security.md)。
