# 02. アーキテクチャ

## 2.1 全体構成

```
                          ┌─────────────────────────────────────────────┐
   Browser                │                  AWS / VPC                    │
  ┌────────┐   HTTPS      │  ┌───────────┐                               │
  │ Console│──────────────┼─▶│   ALB     │  (ACM 証明書, HTTPS)          │
  │(xterm) │   WSS        │  └─────┬─────┘                               │
  └────────┘              │        │ OIDC (Google, hd 制限)              │
                          │        ▼                                     │
                          │  ┌───────────────┐    ┌──────────────────┐  │
                          │  │ Control Plane │───▶│ Metadata 　      │  │
                          │  │  (API +       │    │ (RDS / DynamoDB) │  │
                          │  │ Orchestrator) │    └──────────────────┘  │
                          │  └──┬─────────┬──┘    ┌──────────────────┐  │
                          │     │ ECS API │       │ Secrets Manager  │  │
                          │     ▼         │       │ / SSM Param      │  │
                          │  ┌──────────────────────────────────┐   │  │
                          │  │     ECS Cluster (Workspaces)      │   │  │
                          │  │  ┌────────────┐  ┌────────────┐   │   │  │
                          │  │  │ Workspace  │  │ Workspace  │ … │   │  │
                          │  │  │  user A    │  │  user B    │   │   │  │
                          │  │  │ ┌────────┐ │  │ ┌────────┐ │   │   │  │
                          │  │  │ │WS Agent│ │  │ │WS Agent│ │   │   │  │
                          │  │  │ │ tmux+  │ │  │ │ tmux+  │ │   │   │  │
                          │  │  │ │ claude │ │  │ │ claude │ │   │   │  │
                          │  │  │ └────────┘ │  │ └────────┘ │   │   │  │
                          │  │  └─────┬──────┘  └─────┬──────┘   │   │  │
                          │  └────────┼───────────────┼─────────┘   │  │
                          │           ▼               ▼              │  │
                          │     ┌────────────────────────────┐      │  │
                          │     │  EFS (per-user 永続ホーム)  │      │  │
                          │     │  /workspaces/<user>/        │      │  │
                          │     │    .claude/ .ssh/ repos/    │      │  │
                          │     └────────────────────────────┘      │  │
                          └─────────────────────────────────────────┘  │
                          外部: Bitbucket (SSH/HTTPS), api.anthropic /  │
                                claude.ai (各ユーザーの /login)         │
```

## 2.2 コンポーネント

### Console（フロントエンド）
ブラウザで動く SPA。

- リポジトリ管理 UI（clone / checkout / ブランチ / status）
- セッション一覧・起動・停止
- ターミナル（xterm.js を WSS で Workspace Agent の PTY に接続）
- `settings.json` エディタ、remote-control トグル
- SSH 公開鍵の表示・登録、Claude `/login` 状況表示

候補スタック: Next.js (React) + xterm.js。状態は Control Plane API 経由。

### Control Plane（バックエンド / オーケストレータ）
Workspace の外側で動く常駐サービス。

- 認証セッション検証（ALB/OIDC からのアイデンティティを信頼）
- Workspace ライフサイクル（ECS RunTask/Service 制御、scale-to-zero）
- メタデータ管理（ユーザー、Workspace、Session、監査ログ）
- API: REST/JSON（操作系）+ WebSocket（ターミナル・ステータス購読）
- Workspace Agent への中継（ターミナル WS をプロキシ、git/セッション操作 RPC）

候補スタック: Go か Node(TypeScript)。ECS 制御に AWS SDK。

### Workspace Agent（各コンテナ内の常駐プロセス）
Control Plane と Workspace の間の薄い仲介層。コンテナ内で最小権限ユーザーとして動く。

- **ターミナル**: PTY を生成し tmux にアタッチして WS でストリーム（ttyd 相当を内製 or ttyd 採用）
- **git 操作**: clone / checkout / branch / status を構造化 API で実行し結果を返す
- **セッション制御**: `tmux-claude.sh` のロジックを継承し、Working copy 毎に決定論的セッション ID で claude を起動 / resume / 停止
- **状態取得**: 稼働セッション一覧、Claude `/login` 状態、working copy 状況
- **設定**: `~/.claude/settings.json` の読み書き

> Control Plane から各 Agent へは、VPC 内部のみで到達可能にする（ALB のターゲットにしない／
> あるいは ECS Service Connect / 内部 NLB 経由）。外部公開しない。

### データストア
- **EFS** — ユーザー毎ホーム（`~/.claude` `~/.ssh` working copy）。アクセスポイントで uid/gid と
  ルートディレクトリをユーザー毎に固定し相互不可視にする。
- **RDS(Postgres) もしくは DynamoDB** — Control Plane メタデータ。
- **Secrets Manager / SSM Parameter Store** — Google OAuth クライアントシークレット、
  Bitbucket known_hosts、（任意の）Bitbucket API トークン。
- **S3** — バックアップ、監査ログのアーカイブ。
- **ECR** — Workspace イメージ。

## 2.3 データモデル（Control Plane）

```
User        { id, email, display_name, role, status, created_at }
Workspace   { id, user_id, ecs_task_arn, efs_ap_id, state(running/stopped/creating),
              cpu, mem, last_active_at }
Repository  { id, workspace_id, name, remote_url, default_branch, clone_path, last_status }
Session     { id, workspace_id, repository_id, tmux_name, claude_session_id,
              state(running/stopped), model, started_at, last_active_at }
SshKey      { id, workspace_id, fingerprint, public_key, created_at }
ClaudeAuth  { workspace_id, status(active/expired/none), checked_at }   // 状態キャッシュ
AuditLog    { id, user_id, action, target, detail, at }
```

## 2.4 認証は 2 層に分離する（重要）

混同しやすいので明確に分ける。

| 層 | 対象 | 方式 | 保存先 |
|----|------|------|--------|
| L1 コンソール認証 | 「誰がコンソールを使えるか」 | Google OAuth + `hd` ドメイン制限 | セッション Cookie / JWT |
| L2 Claude 認証 | 「各ユーザーの Claude を誰として動かすか」 | 各自 `claude /login`（claude.ai OAuth）| Workspace の `~/.claude/.credentials.json`（EFS）|

L1 は ALB の OIDC 認証（Google）か `oauth2-proxy`（既存資産）を VPC 端に置いて実現。
L2 はユーザー本人の作業で、コンソールは**状態の可視化と再ログイン誘導**を担う。

## 2.5 主要フロー

### コンソールログイン（L1）
```
Browser → ALB(OIDC: Google, hd=会社ドメイン) → 検証OK → Control Plane
  → User 照合（許可リスト）→ セッション発行 → Console 表示
```

### Workspace 起動 / アタッチ
```
Console「自分の環境を開く」
  → Control Plane: Workspace.state を確認
      stopped → ECS で起動（EFS アクセスポイントをマウント）→ running 待ち
      running → そのまま
  → Workspace Agent と内部接続確立
  → Console にターミナル / セッション一覧を表示
```

### リポジトリ clone
```
Console: remote_url 入力（git@bitbucket.org:org/repo.git）
  → Control Plane → Workspace Agent: git clone（~/.ssh の鍵を使用）
  → 成功で Repository レコード作成、clone_path 確定
  → status 取得して表示
```

### Claude セッション作成（D1）
```
Console: Repository を選び「新しいセッション」
  → Workspace Agent: tmux セッション作成（決定論的 session-id）
      claude --session-id <id>（無ければ新規）/ --resume（あれば再開）
      作業ディレクトリ = clone_path、CLAUDE_CONFIG_DIR は Workspace 既定
  → Session レコード作成
  → Console: ターミナルでアタッチ
```

### ターミナル接続（D3）
```
Browser xterm.js ──WSS──▶ ALB ──▶ Control Plane(WSプロキシ) ──内部──▶ Workspace Agent(PTY)
  Agent は `tmux attach -t <session>` を PTY 上で実行しストリーム双方向中継
```

## 2.6 Claude `/login` フロー

ヘッドレスコンテナでの対話ログインが要点。Claude Code の `/login` は次の流れ。

1. Workspace Agent がターミナル内で `claude`（または `/login`）を起動。
2. CLI が認証 URL を表示。ユーザーはコンソール上のターミナルからその URL を**自分のブラウザ**で開く。
3. claude.ai で本人の Google/Anthropic アカウントで認可。
4. 返ってきたコード/コールバックを CLI に貼り戻す（ターミナル経由）。
5. `~/.claude/.credentials.json` に保存（EFS 永続）。

コンソールの役割:
- **状態表示** — `.credentials.json` の有無・期限から `active / expired / none` を算出。
- **誘導** — `expired/none` のとき「再ログイン」ボタンでターミナルに `/login` を流す。
- 注意: コールバック URL がローカルホスト前提の場合は、URL 手動オープン + コード貼り付け方式に倒す。
  （ここは PoC で実機確認が必要。→ [01 未決 #6](01-requirements.md#17-未決事項今後詰める)）

## 2.7 サンドボックス設計（C2）

二段構え。

1. **ユーザー間**: コンテナ境界（確定）。EFS アクセスポイントで他ユーザーのホームに到達不可。
2. **ユーザー内 / ディレクトリ単位**: 任意で強化。
   - working copy 毎に `CLAUDE_CONFIG_DIR` を分け、設定・履歴・認証プロファイルを分離
     （既存 `update_claude_config_dir` の発想を踏襲）。
   - Claude Code のコマンド実行サンドボックス（権限制御）を `settings.json` で有効化。
   - 破壊的操作の既定許可は Workspace 単位の方針として管理。

## 2.8 既存資産の写像

| 既存（個人運用） | サービス版 |
|------------------|-----------|
| `oauth2-proxy` + `emails.txt` | L1 コンソール認証（ALB OIDC か oauth2-proxy 常設）|
| `tmux-claude.sh`（決定論的 session-id, resume/renew/reset）| Workspace Agent のセッション制御ロジック |
| `CLAUDE_CONFIG_DIR` プロファイル分離 | ディレクトリ単位サンドボックス（2.7）|
| `~/.claude/settings.json` | ユーザー毎設定 + 管理者テンプレート（E1-E3）|
| ローカルの working copy 群 | EFS 上の per-user repos/ |
