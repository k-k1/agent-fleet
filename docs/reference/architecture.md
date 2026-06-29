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
- Connections（Claude / GitHub / Bitbucket の接続・状態表示）、Claude `/login` 状況表示

スタック（確定）: **React + xterm.js**。実装は **React + Vite**（`console/dist` を CP が `no-store` 配信）。
当初構想の Next.js から no-build 系を経て React+Vite に着地した経緯は [decisions/0004](../decisions/0004-vanilla-to-react.md)。
状態は Control Plane API 経由。

### Control Plane（バックエンド / オーケストレータ）
Workspace の外側で動く常駐サービス。

- 認証セッション検証（ALB/OIDC からのアイデンティティを信頼）
- Workspace ライフサイクル（ECS RunTask/Service 制御、scale-to-zero）
- メタデータ管理（ユーザー、Workspace、Session、監査ログ）
- API: REST/JSON（操作系）+ WebSocket（ターミナル・ステータス購読）
- Workspace Agent への中継（ターミナル WS をプロキシ、git/セッション操作 RPC）

スタック（確定）: **Go**（常駐 / WS プロキシ / ECS 制御に好適、単体バイナリで運用が軽い）。AWS SDK for Go。
API 詳細は [06](../reference/api-agent.md)。

### Workspace Agent（各コンテナ内の常駐プロセス）
Control Plane と Workspace の間の薄い仲介層。コンテナ内で最小権限ユーザーとして動く。

- **ターミナル**: PTY を生成し tmux にアタッチして WS でストリーム（ttyd 相当を内製 or ttyd 採用）
- **git 操作**: clone / checkout / branch / status を構造化 API で実行し結果を返す
- **セッション制御**: `tmux-claude.sh` のロジックを継承し、Working copy 毎に決定論的セッション ID で claude を起動 / resume / 停止
- **状態取得**: 稼働セッション一覧、Claude `/login` 状態、working copy 状況
- **設定**: `~/.claude/settings.json` の読み書き

> Control Plane から各 Agent へは、VPC 内部のみで到達可能にする（ALB のターゲットにしない／
> あるいは ECS Service Connect / 内部 NLB 経由）。外部公開しない。

### データストア（`aws` ターゲットの具体。`local` の対応は [ポータビリティ §9.4](portability.md#94-プロファイル別アダプタ対応表)）
- **EFS** — ユーザー毎ホーム（`~/.claude` working copy ほか）。アクセスポイントで uid/gid と
  ルートディレクトリをユーザー毎に固定し相互不可視にする（`local` は bind mount）。
- **MetadataStore** — `aws`=RDS(Postgres) / `local`=SQLite 既定（§2.3）。
- **ユーザー資格情報（Claude/git OAuth・トークン）** — Workspace home の暗号ストア `secrets.enc`
  （AES-256-GCM）。鍵は **封筒暗号 + custodian 抽象**で provisioning する：per-workspace DEK を
  per-tenant KEK で wrap し、CP が起動時に unwrap して注入。custodian は `local`=ファイル/Vault・
  `aws`=KMS（[decisions/0005](../decisions/0005-envelope-custodian.md) / [security §4.4](security.md#44-シークレット管理)）。
- **システムシークレット** — Google OAuth クライアントシークレット等は `aws`=Secrets Manager/SSM・
  `local`=`oauth.env`（git 管理外）。
- **S3** — バックアップ、監査ログのアーカイブ（`aws`）。
- **ECR** — Workspace イメージ（`aws`）。

## 2.3 データモデル（Control Plane）

Phase 3（P3-1 SQLite 化 / P3-2 identity↔tenant 多対多）で確定した現行モデル。MetadataStore は
`local`=SQLite 既定 / `aws`=RDS で同一スキーマ（[ポータビリティ](portability.md)）。**人（Identity）と
テナント（部署）は多対多**で、`Membership` ごとに Workspace が完全分離される。階層の意図と RBAC は
[ロードマップ §12.1](../roadmap.md#121-アイデンティティ階層パッケージセルフホスト版)、移行履歴は
[P3-1 プラン](../history/p3-1-metadatastore.md)（migration 0001〜0005）。

```
Tenant      { id, slug, name, status, limits(json: max_workspaces/max_sessions/disk_gb/mem_mb/cpu),
              isolation(shared|dedicated), key_ref, placement_ref, created_at }   -- 既定 1 テナント=全社
Identity    { id, email(unique), user_key(unique), role(super_admin|user), status, last_login_at }
Membership  { id, identity_id, tenant_id, role(tenant_admin|member), status,
              unique(identity_id, tenant_id) }                                     -- 多対多の結節
Workspace   { id, tenant_id, membership_id(unique), container_name, network, data_dir,
              agent_port, agent_token, state, last_active_at }                     -- = identity×tenant ごと
Repository  { id, workspace_id, name, remote_url, default_branch, last_status }
Session     { id, workspace_id, repository_id, tmux_name, claude_session_id,
              state, model, started_at, last_active_at }                           -- DB ミラー(0005)
UserLimit   { membership_id, max_sessions, disk_gb, mem_mb }                       -- 管理者がテナント枠内で設定
UsageCounter{ tenant_id, running_workspaces, running_sessions, used_disk_gb, allocated_mem_mb, sampled_at }
WrappedDEK  { workspace_id, ciphertext, key_ref, key_version, created_at }         -- 封筒暗号(P3-3)
AuditLog    { id, tenant_id, actor_user_id, actor_kind(user|admin|mcp|system), action, target, detail, at }
```

- **SshKey は廃止**。git 認証は SSH 鍵から HTTPS トークン/OAuth（Connections）へ格下げした
  （[decisions/0003](../decisions/0003-ssh-to-connections.md)）。資格情報は暗号ストア `secrets.enc` に集約。
- **ClaudeAuth はテーブルでなく実行時プローブ**（`claude auth status`）。`/login` の最終結論は
  [decisions/0002](../decisions/0002-claude-auth-onboarding.md)。

## 2.4 認証は 2 層に分離する（重要）

混同しやすいので明確に分ける。

| 層 | 対象 | 方式 | 保存先 |
|----|------|------|--------|
| L1 コンソール認証 | 「誰がコンソールを使えるか」 | Google OAuth + `hd` ドメイン制限 | セッション Cookie / JWT |
| L2 Claude 認証 | 「各ユーザーの Claude を誰として動かすか」 | 各自 `claude /login`（claude.ai OAuth）| Workspace の `~/.claude/.credentials.json`（EFS）|

L1 は3通り: **CP ネイティブ Google OAuth**（`AUTH=oauth`、外部依存なし＝local/小規模デプロイの既定。CP が `authGate` で署名セッションを検証、許可リストでドメイン/メール制限。設計 [auth.md](auth.md)）／ALB の OIDC 認証（Google、`aws` ターゲット）／`oauth2-proxy` 等の外部ゲートウェイ（`AUTH=proxy`）。L2 はユーザー本人の作業で、コンソールは**状態の可視化と再ログイン誘導**を担う。

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

ヘッドレスコンテナでの認証が要点。Claude Code 公式の挙動（v2.1.x, 出典は §2.6 末尾）に基づき、
取り得る方式は 2 つ。**remote-control 要件（E2）の都合で主経路は方式 A** とする。

### 方式 A（採用・主経路）— 対話 OAuth のコード貼り戻し
ブラウザを開けない環境（SSH / コンテナ / WSL2）では `/login` が**自動でコード方式に切替**わる。

1. Workspace Agent がターミナルで `claude`（または `/login`）を起動。
2. CLI が認証 URL を表示（`c` でクリップボードへコピー可）。ユーザーが URL を**自分のブラウザ**で開く。
3. claude.ai で本人アカウントで認可 → 画面に**ログインコード**（自動リダイレクトではない）。
4. そのコードをターミナルの CLI に**貼り戻す**。貼付けが効かない端末は `echo "<code>" | claude auth login`（stdin）。
5. `~/.claude/.credentials.json`（Linux, パーミッション 600, 不透明）に保存。EFS/ボリュームで永続。

- **利点**: サブスク（Pro/Max/Team/Enterprise）の本認証。**remote-control セッションを張れる**。
- コールバックのローカルホスト到達に依存しないため、本番（コンテナ非公開）でも成立する。

### 方式 B（補助）— `claude setup-token`（1 年トークン）
ブラウザのある端末で 1 回だけ `claude setup-token` を実行 → 1 年有効トークンを取得し、
`CLAUDE_CODE_OAUTH_TOKEN` で注入。ファイル同期不要でローテーションしやすい。

- **重大な制約**: このトークンは**推論専用で Remote Control セッションを張れない**（要件 E2 と衝突）。
  → remote-control が不要なユーザー向けの補助に限定。既定経路にはしない。
- 生成時にブラウザが要るため、コンテナ内では生成不可（外部端末で生成して注入）。

### コンソールの役割
- **状態表示** — 公式の状態取得 API は無い。`.credentials.json` の有無 + 軽量プローブ
  （`claude -p` をタイムアウト付き）で `active / expired / none` を推定し、`checked_at` 付きで表示
  （[06 §6.7](../reference/api-agent.md)）。
- **誘導** — `expired/none` のとき「再ログイン」ボタンでターミナルに方式 A を流す。
- 認証情報の精度（期限・リフレッシュの有無）は不透明。Phase 0 で実体を観察し設計に反映（[10](../history/phase0-poc.md)）。

> 出典（Claude Code 公式, v2.1.x）: authentication / troubleshoot-install / devcontainer / headless。
> 確認: 方式 A のコード貼り戻しと setup-token の存在は確定。Remote Control 非対応の制約も同上。
> 不透明: コールバックポート番号、`.credentials.json` のフォーマット、状態取得の公式 API（いずれも非公開/非提供）。

### 検証結果（実機確認, 2026-06-26 / claude v2.1.193）
ヘッドレスコンテナ内 + Tailscale Funnel 越しの Web ターミナルで方式 A を実走させ、次を確定。

- ログイン方法の選択肢は 3 つ:
  `1. Claude account with subscription` / `2. Anthropic Console account` / `3. 3rd-party platform`。
  各自アカウント運用なので **1 を採用**。
- 1 を選ぶと CLI は認証 URL を表示し、その **`redirect_uri` は `https://platform.claude.com/oauth/code/callback`**
  （`code=true`）。**localhost コールバックに一切依存しない**＝ヘッドレス/リモートで無条件に成立する。
  → 設計最大のリスク（localhost コールバック到達性）は**消滅**。方式 A は「フォールバック」ではなく本流。
- 認可後 `platform.claude.com` 側にコードが表示され、それをターミナルに貼り戻すと完了。
  `~/.claude/.credentials.json`（`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-…"}}`, 600）に保存され、
  **永続ホームのため再起動後も再ログイン不要**。
- 観測したスコープ: `org:create_api_key user:profile user:inference user:sessions claude_code
  user:mcp_servers user:file_upload`（`user:sessions` 等を含む本認証）。

### コンソールの URL 受け渡し（実装上の注意）
claude の UI（Ink）は認証 URL を**端末幅でハード改行**するため、端末内の選択コピーは改行が混入し、
xterm の web-links も 1 行目しかリンク化できない。→ Console は **xterm バッファから折返し行を連結して
URL 全体を復元**し、ヘッダの「⧉ sign-in URL」ボタンでオンデマンドにコピーさせる（自動バナーは誤検出の
温床になり廃止）。詳細は [11 §11.10](../history/phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)。

## 2.7 サンドボックス設計（C2）

二段構え。

1. **ユーザー間**: コンテナ境界（確定）。EFS アクセスポイントで他ユーザーのホームに到達不可。
2. **ユーザー内 / ディレクトリ単位**: 任意で強化。
   - working copy 毎に `CLAUDE_CONFIG_DIR` を分け、設定・履歴・認証プロファイルを分離
     （既存 `update_claude_config_dir` の発想を踏襲）。
   - Claude Code のコマンド実行サンドボックス（権限制御）を `settings.json` で有効化。
   - 破壊的操作の既定許可は Workspace 単位の方針として管理。

## 2.8 デプロイ層の分離（ポート & アダプタ）

本構成図は `aws` ターゲットの具体像。プラットフォーム依存（Runtime / Volume / AuthGateway /
MetadataStore / SecretStore / Ingress）は**ポート**として抽象化し、`local`（Docker）と `aws`（ECS）の
アダプタを差し替える。Console / Control Plane コア / Workspace Agent / Workspace イメージは両ターゲット共通。
詳細は [09 ポータビリティ](../reference/portability.md)。

## 2.9 既存資産の写像

| 既存（個人運用） | サービス版 |
|------------------|-----------|
| `oauth2-proxy` + `emails.txt` | L1 コンソール認証 = **CP ネイティブ Google OAuth（`AUTH=oauth`、許可リスト）**／ALB OIDC／外部 oauth2-proxy（`AUTH=proxy`）|
| `tmux-claude.sh`（決定論的 session-id, resume/renew/reset）| Workspace Agent のセッション制御ロジック |
| `CLAUDE_CONFIG_DIR` プロファイル分離 | ディレクトリ単位サンドボックス（2.7）|
| `~/.claude/settings.json` | ユーザー毎設定 + 管理者テンプレート（E1-E3）|
| ローカルの working copy 群 | EFS 上の per-user repos/ |
