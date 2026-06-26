# 09. デプロイ層の分離（ポート & アダプタ）

AWS だけでなくローカルでも同じコードで動かす。プラットフォーム依存（オーケストレーション・
ストレージ・認証・メタデータ・シークレット）を**ポート（インターフェース）**として切り出し、
`local` / `aws` の**アダプタ**を差し替える。コア（Console / Control Plane / Workspace Agent /
Workspace イメージ）は移植可能なまま保つ。

## 9.1 確定方針

| 論点 | 決定 |
|------|------|
| 対応ターゲット | `local` と `aws` の両方（同一アダプタ層で切替）|
| ローカル実行方式 | **Docker**（AWS と同一イメージ・同一挙動・隔離維持）|
| ローカルの形態 | 開発/単一ユーザー（軽量）と オンプレ共有（複数ユーザー）の両方 |

## 9.2 移植可能なコア vs 差し替える周縁

```
┌──────────────────────── 移植可能コア（両ターゲット共通）────────────────────────┐
│  Console(Next.js)   Control Plane(Go, コアロジック)   Workspace Agent(Go)        │
│                                                       Workspace イメージ(Docker)  │
└───────────────────────────────────┬───────────────────────────────────────────┘
                                     │ ポート（Go インターフェース）
        ┌────────────────────────────┼────────────────────────────┐
        ▼                            ▼                            ▼
   Runtime 港                  Volume 港                  AuthGateway 港 …
   ├ local: Docker Engine      ├ local: bind mount        ├ local: oauth2-proxy / dev固定
   └ aws:   ECS                └ aws:   EFS AP            └ aws:   ALB OIDC
```

**重要**: ローカルも Docker のため、**Workspace イメージと Workspace Agent はターゲット間で同一物**。
差分は Control Plane が呼ぶ周縁アダプタだけに閉じる。

## 9.3 ポート定義（Go インターフェース・概略）

```go
// Workspace（コンテナ）のライフサイクル
type Runtime interface {
    Start(ctx, WorkspaceSpec) (WorkspaceHandle, error)
    Stop(ctx, workspaceID) error
    Status(ctx, workspaceID) (WorkspaceState, error)
    Endpoint(ctx, workspaceID) (AgentEndpoint, error) // Agent への内部到達情報
}

// per-user 永続ホームの払い出しとマウント定義
type VolumeProvider interface {
    Ensure(ctx, userID) (VolumeRef, error)
    MountSpec(VolumeRef) MountSpec   // Runtime に渡す
}

// L1 アイデンティティの取り出し
type AuthGateway interface {
    Identify(req) (Identity, error)  // ALB OIDC / oauth2-proxy ヘッダ / dev 固定
}

type MetadataStore interface { /* User/Workspace/Repo/Session/Audit の CRUD */ }
type SecretStore   interface { Get(name) ([]byte, error); Put(name string, v []byte) error }
```

起動時に `PROFILE`（`local|aws`）でアダプタをファクトリ生成。コアは具象を知らない。

## 9.4 プロファイル別アダプタ対応表

| ポート | `local` アダプタ | `aws` アダプタ |
|--------|------------------|----------------|
| Runtime | Docker Engine API（`/var/run/docker.sock`）| ECS（RunTask / Service）|
| Volume | ホストディレクトリ bind mount（`./data/workspaces/<user>`）| EFS アクセスポイント |
| AuthGateway | oauth2-proxy ヘッダ（共有）/ dev 固定 ID（単一）| ALB OIDC ヘッダ |
| MetadataStore | SQLite（埋め込み）| RDS(Postgres) / DynamoDB |
| SecretStore | 暗号化ファイル / OS keychain / `.env` | Secrets Manager / SSM |
| Ingress / TLS | Caddy or Traefik（localhost, 自己署名）/ 直 http | ALB + ACM |
| Agent 認証 | 同一ホスト + 署名トークン | SG 制限 + 署名トークン → mTLS |

## 9.5 ローカルの 2 形態（AuthGateway で切替）

| 形態 | AuthGateway | ユーザー数 | 用途 |
|------|-------------|-----------|------|
| dev（軽量）| 固定 ID バイパス（OAuth 無し）| 1 | 開発・オフライン・AWS 前検証 |
| shared（オンプレ）| oauth2-proxy（Google, `hd` 制限）| 複数 | 社内 1 台のサーバで共有 |

- 既存資産 `oauth2-proxy`（`emails.txt` 運用）を shared 形態にそのまま流用。
- dev は認証を外して即起動。`PROFILE=local AUTH=dev` のように選択。

## 9.6 ローカル構成（compose 概略）

```
docker compose（ホスト 1 台）
  ├─ reverse-proxy (Caddy)            :443  自己署名/社内CA
  ├─ oauth2-proxy                     （shared 形態のみ）
  ├─ control-plane (Go)               /var/run/docker.sock をマウント
  │     └─ Docker 経由で per-user Workspace コンテナを起動
  ├─ SQLite ファイル（control-plane に同梱ボリューム）
  └─ ./data/workspaces/<user>/        各ユーザーの永続ホーム（bind mount）
        .claude/  .ssh/  repos/
```

- Control Plane は Docker ソケット経由で同一ホスト上に Workspace を spawn。
- ユーザー毎ホームは `./data/workspaces/<user>` を Workspace の `~` に bind mount。
- ネットワークは専用 Docker network で分離し、Workspace は外部公開しない。

## 9.7 パリティと相違点（明示しておく差分）

| 観点 | local | aws | 備考 |
|------|-------|-----|------|
| Workspace イメージ / Agent | 同一 | 同一 | 差分なし（移植の肝）|
| scale-to-zero | `docker stop/start`（同じアイドル判定ロジック）| ECS desired 0/1 | Control Plane のロジックは共通、Runtime が実体差を吸収 |
| 隔離強度 | コンテナ境界（同一カーネル共有）| コンテナ + タスク分離 | local は隔離が相対的に緩い。dev は信頼ホスト前提 |
| Egress 制限 | docker network + ホスト firewall | SG / NACL / VPCe | local は運用者責任で設定 |
| 認証情報メタデータ盗用 | Docker ソケット = ホスト root 相当 | IMDS 遮断 + Task Role 最小化 | §9.8 |
| ストレージ性能 | ローカルディスク（速い）| EFS（メタデータ操作で遅延しうる）| 大規模 git は local が快適 |

## 9.8 ローカル特有のセキュリティ留意

- **Docker ソケット権限**: Control Plane に `docker.sock` を渡すとホスト root 相当。dev/信頼された
  オンプレでは許容、untrusted 環境では rootless Docker / ソケットプロキシ（権限絞り）を検討。
- shared 形態は AWS と同じ脅威モデル（[04](04-security.md)）を適用。dev 形態は単一信頼ユーザー前提で簡略化。

## 9.9 ロードマップへの影響（local-first）

- Phase 0/1 を **ローカル（dev → shared）で先に完成**させ、AWS アダプタを後付けする方が速い。
- ローカルが「AWS の縮小版」ではなく「同一コアの別アダプタ」になるため、開発・デモ・オフライン検証が
  そのまま本番設計の検証になる。
- [05 ロードマップ](05-roadmap.md) を local-first に更新済み。
