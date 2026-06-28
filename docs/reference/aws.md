# 03. AWS デプロイ構成

本書は `aws` ターゲット（[09 ポータビリティ](../reference/portability.md)）の具体像。
ローカル（Docker）で動かす構成は [09](../reference/portability.md) を参照。コア（Console / Control Plane /
Workspace Agent / Workspace イメージ）は両者で共通。

## 3.1 実行基盤の選定

ユーザー毎の長寿命コンテナ（tmux + claude が常駐）を 〜20 個、永続ホーム付きで動かす。

| 基盤 | 長所 | 短所 | 評価 |
|------|------|------|------|
| **ECS on Fargate** | サーバ管理不要、per-task 隔離、EFS マウント可 | コンテナ単価がやや高い、常駐 20 個は割高 | ◎ MVP に推奨 |
| **ECS on EC2** | 大きめ EC2 にコンテナ集約で安い、bursty CPU に強い | キャパシティ管理が要る | ○ コスト重視時 |
| EKS | スケール・標準化に強い | 初期構築と運用が重い | △ 20 人には過剰 |

**推奨**: MVP は **ECS on Fargate**。コスト最適化フェーズで **ECS on EC2**（または Fargate + scale-to-zero）に寄せる。

### Workspace の単位
- 1 ユーザー = 1 ECS **Service**（desiredCount 0/1）。アイドルで 0、利用開始で 1（scale-to-zero）。
- もしくは 1 ユーザー = オンデマンド **RunTask**。Control Plane が起動/停止を制御。
- 長寿命 tmux セッションを保つため、コンテナ停止≠会話消失（履歴は EFS、resume で復帰）。

## 3.2 永続ストレージ: EFS を主とする

| 方式 | 特徴 | 採否 |
|------|------|------|
| **EFS + アクセスポイント** | 1 ファイルシステムを per-user アクセスポイントで分割。uid/gid 固定、ルート限定。コンテナがどのノードに移っても同じホームに再アタッチ。Fargate 対応。 | **採用** |
| EBS（タスク専用ボリューム） | 単一 AZ 高速。ただしタスク移動・複数アタッチに難。 | 高 I/O が要る場合の補助 |

- 構成: 1 つの EFS、ユーザー毎にアクセスポイント（root = `/workspaces/<user>`、posix uid/gid = ユーザー固有）。
- マウント先 = コンテナ内のホーム（`~`）。`.claude/` `.ssh/` `repos/` を内包。
- バックアップ: AWS Backup で EFS を日次。重要物（`.claude` 設定等）は S3 へも。
- 注意: EFS はメタデータ操作が多い git で遅くなりうる。大規模 repo は EBS 補助 or キャッシュを検討。

## 3.3 ネットワーク / 認証ゲート

```
Internet
  │ 443
  ▼
[ALB]  ── OIDC 認証(Google, hd=会社ドメイン) ──┐  ※または oauth2-proxy を常設
  │ 認証済みのみ転送                            │
  ├──▶ Console (静的配信 / S3+CloudFront or ECS)│
  └──▶ Control Plane (API + WS)                 │
                │ VPC 内部のみ                   │
                ▼                                │
        Workspace コンテナ群（外部公開しない）   │
                │                                │
                ▼ EFS / Secrets / Metadata       │
```

- **L1 認証**: ALB の OIDC（Google）か、既存 `oauth2-proxy` を ECS サービスとして常設。
  ドメイン制限は Google の `hd` クレーム検証で行う。
- Workspace コンテナは ALB のターゲットにしない。Control Plane からのみ内部到達
  （ECS Service Connect / 内部 NLB / awsvpc 同一 SG 内）。
- **Egress 制御**: Workspace から外部は Bitbucket と Anthropic/claude.ai に限定（NAT + 制限 or VPC エンドポイント方針は [04](../reference/security.md) で）。

## 3.4 主要 AWS サービス一覧

| 用途 | サービス |
|------|----------|
| コンテナ実行 | ECS (Fargate / EC2) |
| 永続ホーム | EFS（アクセスポイント）+ AWS Backup |
| 入口 / 認証 | ALB（ACM 証明書, OIDC）|
| メタデータ | RDS(PostgreSQL, t4g.micro) または DynamoDB |
| シークレット | Secrets Manager / SSM Parameter Store |
| イメージ | ECR |
| ログ・監査 | CloudWatch Logs, S3 |
| 静的配信 | S3 + CloudFront（Console を静的化する場合）|
| IaC | Terraform もしくは AWS CDK |

## 3.5 コスト試算（〜20 人, 月額 / おおよそ）

前提: 1 Workspace = 1 vCPU / 2GB、平日日中の稼働を想定。為替・リージョンで変動。

### 常時稼働（20 個を 24/7 Fargate）
- Fargate: 1 vCPU≈$0.04/h, 2GB≈$0.008/h → 約 $0.05/h ×24×30 ≈ **$36/台/月** → 20 台 ≈ **$720/月**
- 上振れしやすい。常時稼働は非推奨。

### scale-to-zero（平日 8h 稼働を想定）
- 20 台 × $0.05/h × 8h × 22 日 ≈ **$176/月**
- ALB ≈ $20、RDS(t4g.micro) ≈ $15〜30、EFS(50GB) ≈ $15、NAT ≈ $35、その他（S3/ECR/CW）≈ $10〜20
- **合計目安: 約 $270〜300/月**

### ECS on EC2 に集約（コスト最適）
- m6i.2xlarge（8vCPU/32GB）×1〜2 台に 20 コンテナを密集 → EC2 ≈ $250〜500/月（RI/Savings で削減可）
- Fargate 比でアイドルコストを吸収しやすいが、キャパシティ運用の手間が増える。

> 注: Claude 本体の利用料は**各ユーザーの個人サブスク**（`/login`）で、上記 AWS コストには含まない。

## 3.6 イメージ更新（フリート一括更新）

- Workspace イメージに claude CLI / git / 開発ツールを同梱し ECR で版管理。
- 更新は新タグを push → Service の task definition 更新 → 各 Workspace を順次入替。
- ホーム（EFS）はイメージと分離されるため、更新でユーザーデータは消えない。
- 既存 `tmux-claude.sh --renew/--restart` に相当する「全 Workspace 再起動」操作を管理画面に用意。
