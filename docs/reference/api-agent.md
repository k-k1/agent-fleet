# API & Workspace Agent — 契約はコードが正

Console ↔ Control Plane（公開）と CP ↔ Workspace Agent（内部）の 2 つの境界。
**実装が進み API 表面はこの文書のスナップショットより大きい。エンドポイントの正は
コードと [HANDOFF §6.10](../HANDOFF.md)**——本書は (1) 現行の API 表面の地図、(2) 普遍的な設計
（Agent の位置づけ・認証モデル・セッション制御・PTY）を残す。逐一の請求/応答形は code-as-contract。

> 旧 `../reference/api-agent.md`（`/api/v1` ドラフト）はここに統合した。当時の SSH/`/sshkey` 系・`/api/v1` 前置は
> 実装で置き換わっている（Connections は [decisions/0003](../decisions/0003-ssh-to-connections.md)）。

## 7.0 現行 API 表面（地図 / 詳細はコード + HANDOFF §6.10）

**公開: Console ↔ Control Plane**（`/api/*`、ALB OIDC / oauth2-proxy で L1 終端、`X-AF-Tenant` でテナント選択）

| グループ | 代表エンドポイント | 詳細 |
|----------|--------------------|------|
| whoami / tenants | `GET /api/whoami`・`GET /api/tenants` | HANDOFF §6.7 / §6.10.1 |
| workspace | `GET /api/workspace`・`POST /api/workspace/{start,stop,recreate}` | §6.10.1 |
| sessions | `GET/POST /api/sessions`・`POST /api/sessions/{name}/{stop,halt,recreate,archive,restore}`・`GET /api/sessions/archived` | §6.10.2 |
| repos | `GET/POST /api/repos`・`/api/repos/{name}/{status,branches,checkout,fetch,changes,diff,log,stage,unstage,discard,commit,show}` | §6.10.5 |
| connections | `GET /api/connections`・git/claude/opencode の `PUT/DELETE` + OAuth（GitHub Device / Bitbucket Auth Code）| §6.6 / §6.10.3-4 |
| fs / env / settings | `GET /api/fs/{tree,file}`・`GET/PUT /api/env/{toolchains,ui-prefs}`・`PUT /api/claude/settings` | §6.10.5-7 |
| admin（super_admin）| `GET /api/admin/tenants`・`/tenants/{slug}/members`・`POST /api/admin/{tenants,memberships,stop-workspace}`・`PUT /api/admin/{tenants/{slug}/limits,user-limits}` | §6.9 / §6.10.6 |
| WebSocket | `GET /ws/pty?session=&tenant=`（PTY 中継）| §6.10.1 |

- 認証: L1（Google OIDC）を前段で終端し CP は検証済み ID を信頼。`AUTH=proxy` ではヘッダ欠落＝401、CP は
  `127.0.0.1` 束縛（HANDOFF §6.8 B1）。原則「自分のリソースのみ」＋ membership 検証（403/409）。
- 非同期（起動/clone）は同期 + ポーリングで運用（当初の `/jobs` 構想は未採用）。

**内部: Control Plane ↔ Workspace Agent**（per-container Bearer。§7.5）。Agent は CP 公開 API の下請けで、
`/sessions`・`/repos`・`/connections`・`/fs`・`/env` ＋ `/ws/pty`。CP は `/api` を剥がして proxy する。

## 7.1 位置づけ

```
Control Plane ──内部── Workspace Agent ──┬── PTY(tmux attach)
   (Go)                     (Go, 同梱)    ├── git (clone/checkout/status/diff/commit…)
                                          ├── claude / opencode セッション制御
                                          ├── claude settings / env / ui-prefs 読み書き
                                          └── connections（git/claude 資格情報の暗号保存）
```

- 外部公開しない。Control Plane からのみ内部到達（[03 §3.3](../reference/aws.md#33-ネットワーク--認証ゲート)）。
- 非特権ユーザーで起動。コンテナ内のユーザー領域のみ操作。

## 7.2 インターフェース方式

- **制御系（要求/応答）**: HTTP/JSON もしくは gRPC（VPC 内）。冪等・短命。
- **ストリーム系**: WebSocket（PTY 入出力）。Control Plane が Console の `/ws/terminal` を透過プロキシ。
- ポートは固定（例 `:8800` 制御 / `:8801` PTY）。コンテナ外へは出さない。

## 7.3 制御 API（Agent 内部）

Control Plane が呼ぶ内部エンドポイント（§7.0 の地図の下請け）。下表は普遍的な機能群。実体の正はコード。

| 機能 | 概要 | 主な実体 |
|------|------|----------|
| `git.clone` | `git clone <url> <path>` | 統一 cred helper で透過認証（Connections）|
| `git.status` | 構造化 status | `git status --porcelain=v2 --branch` を解析 |
| `git.branches` | ブランチ一覧 | `git for-each-ref` |
| `git.checkout` | checkout / ブランチ作成 | `git checkout [-b] <ref>` |
| `git.fetch` | fetch/prune | `git fetch [--prune]` |
| `git.view` | changes/diff/log/stage/commit/show | read/write SCM（traversal 防御・サイズ上限）|
| `session.list/create/stop/resume` | セッション制御 | §7.4。kind=claude/opencode/shell |
| `connections.*` | git/claude/opencode の資格情報 upsert | 暗号ストア `secrets.enc` に保存（平文を作らない）|
| `claudeAuth.status` | Claude 接続状態 | `claude auth status`（[decisions/0002](../decisions/0002-claude-auth-onboarding.md)）|
| `settings/env` | claude settings・toolchains・ui-prefs | home 永続（denylist 配下）|
| `fs.tree/file` | ファイルブラウザ（read-only・denylist）| traversal 防御・サイズ上限 |
| `health` | 生存確認 | `/healthz`（認証除外）|

## 7.4 セッション制御（tmux-claude.sh の継承）

既存スクリプトの要点をそのまま設計に取り込む。

- **決定論的セッション ID**: `uuidgen --sha1 --namespace @url --name "<salt>|<dir>|<name>"` 相当。
  同じ Working copy + スロット名なら毎回同じ ID。claude.ai 側に新エントリを増やさず resume する。
- **起動規則**: 履歴 jsonl が在れば `--resume`、無ければ `--session-id` で新規。
- **作業ディレクトリ**: Working copy の `clone_path`。
- **設定プロファイル**: 既定は Workspace 共通の `~/.claude`。ディレクトリ単位サンドボックスを使う場合は
  `CLAUDE_CONFIG_DIR` を Working copy ごとに割り当て（[02 §2.7](../reference/architecture.md#27-サンドボックス設計c2)）。
- **共通フラグ**: `--dangerously-skip-permissions` 等は Workspace ポリシーとして管理（[04](../reference/security.md)）。
- **世代操作**: `renew`（全スロット新 ID）/ `restart`（同 ID 再起動）/ `reset`（履歴のみ削除）に相当する
  操作を Agent の API として提供。フリート全体版は管理者 API（[06 §6.9](../reference/api-agent.md)）。

```
session.create(repository_id, name, model?, config_profile?):
  dir   = repo.clone_path
  sid   = deterministic_id(salt, dir, name)
  tmux  = "claude_" + slug(repo + name)
  if jsonl_exists(sid): launch `claude --resume <sid> …`
  else:                 launch `claude --session-id <sid> …`
  inside `tmux new-session -d -s <tmux>`
  return { sid, tmux, state: running }
```

## 7.5 Control Plane との認証

Agent は内部公開でも認証必須（多層防御）。候補:

- **mTLS** — Control Plane と Agent が相互証明書検証。VPC + SG で到達制限の上に暗号認証。
- **署名付きトークン** — 起動時に Workspace ごとの短命トークンを注入し、各要求で検証。

→ [01 未決 #6](../reference/requirements.md#17-未決事項今後詰める)。MVP は SG 制限 + 署名トークン、本番で mTLS を推奨。

**実装済（Phase 2 A2）**: per-container `AGENT_TOKEN` を CP が起動時に `-e` 注入し、proxy(REST/WS) と
Bitbucket callback で `Authorization: Bearer` を付与。Agent の `requireToken` ミドルウェアが `/healthz`
以外を定数時間比較で検証（未設定時は dev 用に開放）。A1 のネットワーク分離（`af-net-<user>`）と多層で、
到達制限 + トークン認証を満たす。詳細は `docs/HANDOFF.md` §6.7/§6.8。

## 7.6 PTY ストリーム

- Agent は `forkpty` で擬似端末を生成し、`tmux attach -t <session>`（または shell）を実行。
- 上り `input`/`resize`、下り `output` を WS で中継（[06 §6.10](../reference/api-agent.md)）。
- 切断時も tmux セッションは存続。再接続で同一画面に復帰。
- 複数タブから同一 tmux への同時アタッチを許容（共有ビュー）。

## 7.7 ライフサイクルと永続

- Agent はコンテナ起動時に常駐開始。`~`（EFS）をマウント済み前提。
- 起動時処理: `~/.ssh` 鍵の存在確認（無ければ生成）、known_hosts 整備、tmux サーバ起動。
- コンテナ停止: tmux セッションはプロセスとして消えるが、会話履歴 jsonl は EFS に残り resume 可能。
- イメージ更新で Agent/claude を入れ替えてもユーザーデータ（EFS）は不変。
