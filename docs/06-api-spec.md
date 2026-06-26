# 06. Control Plane API 仕様（ドラフト）

Console（Next.js）↔ Control Plane（Go）間の契約。REST（操作系）と WebSocket（ストリーム系）で構成。
本書は v1 ドラフト。実装着手前に各エンドポイントの確定を行う。

## 6.1 共通事項

- ベース URL: `/api/v1`
- 認証: L1（Google OIDC）を ALB で終端。Control Plane は ALB が付与する検証済み ID
  （`x-amzn-oidc-identity` / `x-amzn-oidc-data` JWT）を信頼し、内部セッションへ写像する。
- 認可: 原則「自分のリソースのみ」。`workspace_id` はトークンのユーザーに紐づくものに固定し、
  パスでの他人指定を拒否。管理者ロールのみ `/admin/*` を許可。
- 形式: リクエスト/レスポンスは JSON（`Content-Type: application/json`）。
- エラー: `{ "error": { "code": "string", "message": "string", "detail": {...} } }` + 適切な HTTP ステータス。
- 冪等性: clone / セッション作成など副作用のある POST は `Idempotency-Key` ヘッダを受け付ける。
- 非同期操作: 時間のかかる処理（Workspace 起動・clone）は「ジョブ」を返し、状態は WS または
  `GET /jobs/:id` で購読する。

## 6.2 認証・自分情報

| Method | Path | 説明 |
|--------|------|------|
| GET | `/me` | 現在のユーザー（id, email, display_name, role）|
| GET | `/me/workspace` | 自分の Workspace 概要（state, last_active_at）|

## 6.3 Workspace ライフサイクル

| Method | Path | 説明 |
|--------|------|------|
| GET | `/workspace` | 状態取得（`creating/running/stopped`、リソース、起動時刻）|
| POST | `/workspace/start` | 起動（stopped→running）。ジョブを返す。 |
| POST | `/workspace/stop` | 停止（running→stopped）。tmux/セッションは EFS に残り resume 可。 |
| POST | `/workspace/restart` | 再起動（イメージ更新反映など）。 |

レスポンス例（GET /workspace）:
```json
{ "id": "ws_ab12", "state": "running", "cpu": 1, "mem_mb": 2048,
  "started_at": "2026-06-26T01:00:00Z", "last_active_at": "2026-06-26T03:12:00Z" }
```

## 6.4 リポジトリ / Working copy

| Method | Path | 説明 |
|--------|------|------|
| GET | `/repos` | clone 済みリポジトリ一覧（name, branch, status 概要）|
| POST | `/repos` | clone 実行（body: `remote_url`, 任意 `branch`）。ジョブを返す。 |
| GET | `/repos/:id` | 単一リポジトリ詳細 |
| DELETE | `/repos/:id` | working copy 削除 |
| GET | `/repos/:id/status` | `git status` 相当（現在ブランチ・dirty・ahead/behind・変更ファイル数）|
| GET | `/repos/:id/branches` | ローカル/リモートブランチ一覧 |
| POST | `/repos/:id/checkout` | checkout（body: `branch` or `ref`、任意 `create: true`）|
| POST | `/repos/:id/fetch` | `git fetch`（任意 `prune`）|

status レスポンス例:
```json
{ "branch": "feature/x", "detached": false, "dirty": true,
  "ahead": 2, "behind": 0, "staged": 1, "unstaged": 3, "untracked": 4 }
```

> git 認証は Workspace 内の `~/.ssh` の鍵で行う（[08](08-bitbucket.md)）。
> Control Plane は鍵を持たず、Agent に操作を委譲する。

## 6.5 Claude セッション

| Method | Path | 説明 |
|--------|------|------|
| GET | `/sessions` | セッション一覧（state, repo, model, last_active）|
| POST | `/sessions` | 作成（body: `repository_id`, 任意 `model`, `config_profile`）|
| GET | `/sessions/:id` | 単一セッション詳細（tmux_name, claude_session_id, state）|
| POST | `/sessions/:id/stop` | 停止（tmux セッション終了。会話履歴は残る）|
| POST | `/sessions/:id/resume` | 再開（決定論的 session-id で resume）|
| DELETE | `/sessions/:id` | セッション破棄（履歴 jsonl 削除は別フラグ）|

作成レスポンス例:
```json
{ "id": "se_77", "repository_id": "re_3", "tmux_name": "claude_re3_01",
  "claude_session_id": "9f1c…", "state": "running", "model": "default" }
```

セッション制御の内部実装は `tmux-claude.sh` のロジックを継承（[07](07-workspace-agent.md)）。

## 6.6 SSH 鍵（Bitbucket 用）

| Method | Path | 説明 |
|--------|------|------|
| GET | `/sshkey` | 公開鍵と fingerprint を取得（Bitbucket へ手動登録するため）|
| POST | `/sshkey/rotate` | 鍵ローテーション（新公開鍵を返す。再登録が必要）|

詳細は [08](08-bitbucket.md)。

## 6.7 Claude 認証状態（`/login`）

| Method | Path | 説明 |
|--------|------|------|
| GET | `/claude-auth` | 状態（`active/expired/none`, method, checked_at）|
| POST | `/claude-auth/login` | ターミナルに方式 A（対話コード貼り戻し）を投入し WS で続行 |
| POST | `/claude-auth/token` | 方式 B: `CLAUDE_CODE_OAUTH_TOKEN`（setup-token）を登録（remote-control 不可な点を警告）|
| POST | `/claude-auth/logout` | `claude /logout` 相当 |

- **状態取得に公式 API は無い**（[02 §2.6](02-architecture.md#26-claude-login-フロー)）。Agent が次で推定する:
  1. `~/.claude/.credentials.json` の存在（無ければ `none`）。
  2. 軽量プローブ `claude -p`（タイムアウト付き）の成否で `active/expired` を判別。
  3. 結果を `checked_at` 付きでキャッシュし、UI は「最終確認時刻」を併記。
- `method` は `subscription`（方式 A）/ `token`（方式 B）を区別。`token` のとき remote-control 不可を UI に明示。

## 6.8 設定（settings.json）

| Method | Path | 説明 |
|--------|------|------|
| GET | `/settings` | 現在の `~/.claude/settings.json` を取得 |
| PUT | `/settings` | 更新（スキーマ検証後に書き込み）|
| GET | `/settings/template` | 管理者既定テンプレート取得 |
| POST | `/settings/remote-control` | remote-control の有効/無効トグル |

- 検証: 既知キー（`remoteControlAtStartup`, `skipDangerousModePermissionPrompt`, `hooks`, `theme` 等）を
  ホワイトリスト + 型チェック。未知キーは警告。
- 適用タイミング: 一部設定は次回セッション起動から有効。UI で明示。

## 6.9 管理者

| Method | Path | 説明 |
|--------|------|------|
| GET | `/admin/users` | ユーザー一覧 |
| POST | `/admin/users` | ユーザー追加（email、許可リストへ）|
| PATCH | `/admin/users/:id` | role/status 変更 |
| DELETE | `/admin/users/:id` | 無効化 |
| GET | `/admin/audit` | 監査ログ検索 |
| POST | `/admin/fleet/restart` | 全 Workspace 再起動（イメージ一括更新）|

## 6.10 WebSocket

### `/ws/terminal`
ターミナル PTY ストリーム。Console の xterm.js が接続。

- クエリ: `?session=<id>`（tmux セッションへアタッチ）または `?shell=1`（素の shell）
- 上り: `{ "type": "input", "data": "..." }` / `{ "type": "resize", "cols": N, "rows": N }`
- 下り: `{ "type": "output", "data": "..." }`（PTY 出力。バイナリフレーム最適化も検討）
- Control Plane は本 WS を Agent の PTY エンドポイントへ透過プロキシする（[07](07-workspace-agent.md)）。

### `/ws/events`
状態プッシュ（購読型）。ポーリング削減。

- イベント: `workspace.state`, `session.state`, `repo.status`, `claude_auth.status`, `job.progress`
- 例: `{ "type": "session.state", "id": "se_77", "state": "stopped", "at": "…" }`

## 6.11 ジョブ（非同期処理）

| Method | Path | 説明 |
|--------|------|------|
| GET | `/jobs/:id` | ジョブ状態（`queued/running/succeeded/failed`, progress, result）|

Workspace 起動・clone・fetch 等の長時間処理に使用。進捗は `/ws/events` の `job.progress` でも配信。
