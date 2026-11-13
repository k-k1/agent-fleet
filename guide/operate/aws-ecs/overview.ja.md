---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）に配備を構築して運用する配備管理者"
source_of_truth: "テンプレートは `deploy/aws/ecs/cfn/*.yaml`、パラメータの詳細は `deploy/aws/ecs/cfn/PARAMETERS.md`"
---

# ECS 構成の全体像

[English](overview.md) | 日本語

AWS の ECS に構築する配備（`ecs` / `ecs-ec2`）が、どのスタックでできていて、どの順番に構築するかを知ります。
ワークスペースは 1 つが 1 つの ECS サービスで、止めると `desiredCount` が 0 になります（スケールトゥゼロ）。Control Plane とは Service Connect でつながります。

AWS で手軽にセルフホストしたいだけなら、これではなく EC2 1 台の compose を選んでください。違いは [デプロイ先を選ぶ](../choose.ja.md) にあります。

## 全体像

| あなたがやること | 起きること |
|---|---|
| CloudFormation のスタックを順番にデプロイする | VPC・EFS・RDS・ECR・ECS クラスター・ALB・Control Plane のサービスが、配備ごとに 1 回だけできる |
| メンバーがワークスペースを起動する | Control Plane が、そのワークスペースの ECS サービス・タスク定義・EFS アクセスポイント・SSM の SecureString パラメータを、決まった名前で作る。スタックは変わらない |
| リリースを上げる | `ImageTag` を変えて `30-ingress` だけをデプロイし直す（[アップグレードする](upgrade.ja.md)） |
| 撤去する | スタックを `delete-stack` で消す |

必要なのは AWS CLI だけです。Terraform や CDK は使いません。コマンドは、リポジトリの `deploy/aws/ecs/` で実行します。

## CloudFormation が作るもの・Control Plane が作るもの

| 層 | 作る人 | リソース |
|---|---|---|
| 静的な土台 | CloudFormation（`deploy/aws/ecs/cfn/`） | VPC・サブネット・NAT・セキュリティグループ、EFS ファイルシステム、RDS、ECR、ECS クラスター、Service Connect の名前空間、IAM ロール、ALB（ACM、TLS の終端だけ）、Control Plane と Console のサービス |
| ワークスペースごと | 動いている Control Plane | ECS サービス、タスク定義、EFS アクセスポイント、SSM の SecureString パラメータ |

- Control Plane はワークスペースのリソースを、決まった名前とタグで見つけます。それらについての状態を自分では持ちません
- スタックは配備ごとに 1 回デプロイするだけです。ワークスペースが増えても減っても、スタックは変わりません

## スタックの分けかた

作り直すのに時間がかかるネットワークを長く残し、よく変えるところだけを更新できるように、スタックを層に分けています。

| テンプレート | 要るとき | 中身 |
|---|---|---|
| `cfn/00-network.yaml` | いつも | VPC、2 つの AZ のパブリック・プライベートサブネット、インターネットゲートウェイ、NAT、基本のセキュリティグループ（`alb` / `cp` / `ws`） |
| `cfn/10-data.yaml` | いつも | EFS ファイルシステムとマウントターゲット、RDS（Postgres、シングル AZ、既定は db.t4g.micro、マスターパスワードは RDS が管理）。`CustodianKmsKey=create` なら、Control Plane のキーに使う KMS キーも |
| `cfn/20-platform.yaml` | いつも | ECR（Control Plane とワークスペース、任意のエンジン用の `af-voicevox`・`af-llamacpp`・`af-comfyui`・`af-engine-tools`）、ECS クラスター、Service Connect の名前空間（`af.internal`）、IAM ロール（`cp-task` / `exec` / `ws-task`） |
| `cfn/40-ec2-pool.yaml` | `WsRuntime=ecs-ec2` のときだけ | ワークスペースのスロットの起動テンプレート、スロットのインスタンスロールとセキュリティグループ。インスタンスは作らず、Control Plane が必要なときに起動する |
| `cfn/50-tts.yaml` | 日本語の読み上げを使うときだけ | VOICEVOX のエンジン（ふだんはゼロにしておく Fargate のサービス）、Cloud Map の名前、専用のセキュリティグループ |
| `cfn/60-engines.yaml` | 自前の推論エンジンを使うときだけ | llama.cpp（`llm`）と ComfyUI（`image`）の GPU のサービス、Control Plane が EC2 Fleet で起動するインスタンスの起動テンプレート、モデルを置く S3 Bucket、取り込みのタスク |
| `cfn/30-ingress.yaml` | いつも | ACM の証明書（DNS 検証）、ALB、Control Plane と Console の Fargate のサービス、Route 53 のエイリアス |

### デプロイする順番

各スタックは、前のスタックのエクスポートを読みます。次の順番でデプロイしてください。括弧の中は、そのスタックが要るときです。

```
00 → 10 → 20 → (40: ecs-ec2 only) → (50: speech only) → (60: self-hosted inference only) → 30
```

`50-tts` と `60-engines` が `30-ingress` より先なのは、`30-ingress` がその出力を受け取るからです。

### パラメータの詳細

配備で調整するパラメータのほとんどは `30-ingress.yaml` にあります。テンプレートの `Description:` は 1〜2 行だけで、測った値・トレードオフ・落とし穴は [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md) にあります。`60-engines` のパラメータは [`cfn/PARAMETERS-60-engines.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS-60-engines.md) です。
説明をテンプレートの外に置く理由は [テンプレートの大きさの上限に気をつける](template-size.ja.md) にあります。

## 名前の付けかた

すべてのリソースの名前は `af-` で始まります。ほかの作業と共有するアカウントでも、`af-` で探せば配備のリソースだけを見つけて消せます。

## 関連

- [ECS に構築する](install.ja.md)
- [パラメータで任意の機能をオンにする](options.ja.md)
- [デプロイに必要な IAM 権限](iam.ja.md)
- [ECS で困ったとき](troubleshooting.ja.md)
- [配備形態](../../ref/deploy-targets.ja.md)
